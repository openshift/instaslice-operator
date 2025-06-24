package deviceplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"k8s.io/utils/ptr"

	instaclient "github.com/openshift/instaslice-operator/pkg/generated/clientset/versioned"
	kubernetes "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	nvml "github.com/NVIDIA/go-nvml/pkg/nvml"

	"tags.cncf.io/container-device-interface/pkg/cdi"
	parser "tags.cncf.io/container-device-interface/pkg/parser"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	// UUID generator for unique spec filenames
	utiluuid "k8s.io/apimachinery/pkg/util/uuid"

	instav1 "github.com/openshift/instaslice-operator/pkg/apis/dasoperator/v1alpha1"
)

var _ pluginapi.DevicePluginServer = (*Server)(nil)

type Server struct {
	pluginapi.UnimplementedDevicePluginServer
	Manager          *Manager
	SocketPath       string
	InstasliceClient instaclient.Interface
	KubeClient       kubernetes.Interface
	NodeName         string
	EmulatedMode     instav1.EmulatedMode
	allocMutex       sync.Mutex
}

const allocationAnnotationKey = "mig.das.com/allocation"

func NewServer(mgr *Manager, socketPath string, kubeConfig *rest.Config, emulatedMode instav1.EmulatedMode) (*Server, error) {
	cfg := rest.CopyConfig(kubeConfig)
	cfg.AcceptContentTypes = "application/json"
	cfg.ContentType = "application/json"
	client, err := instaclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create instaslice client: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	nodeName := os.Getenv("NODE_NAME")
	return &Server{Manager: mgr, SocketPath: socketPath, InstasliceClient: client, KubeClient: kubeClient, NodeName: nodeName, EmulatedMode: emulatedMode}, nil
}

func (s *Server) Start(ctx context.Context) error {
	klog.InfoS("Starting device plugin server", "socket", s.SocketPath)
	// CDI cache is configured during daemonset initialization

	// remove existing socket file, if any
	if err := os.Remove(s.SocketPath); err != nil {
		if os.IsNotExist(err) {
			klog.InfoS("Socket file does not exist, skipping removal", "socket", s.SocketPath)
		} else {
			klog.ErrorS(err, "Failed to remove existing socket file", "socket", s.SocketPath)
			return fmt.Errorf("failed to remove existing socket %q: %w", s.SocketPath, err)
		}
	} else {
		klog.InfoS("Removed existing socket file", "socket", s.SocketPath)
	}
	lis, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		klog.ErrorS(err, "Failed to listen on socket", "socket", s.SocketPath)
		return fmt.Errorf("failed to listen on socket %q: %w", s.SocketPath, err)
	}
	klog.InfoS("Listening on socket", "socket", s.SocketPath)
	grpcServer := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(grpcServer, s)
	klog.InfoS("Registered device plugin server", "socket", s.SocketPath)
	klog.InfoS("Starting device manager", "resource", s.Manager.ResourceName)
	go s.Manager.Start(ctx)
	klog.InfoS("Starting gRPC server", "socket", s.SocketPath)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			klog.ErrorS(err, "gRPC server stopped unexpectedly", "socket", s.SocketPath)
		} else {
			klog.InfoS("gRPC server stopped", "socket", s.SocketPath)
		}
	}()
	go func() {
		<-ctx.Done()
		klog.InfoS("Shutting down device plugin server", "socket", s.SocketPath)
		grpcServer.Stop()
	}()
	return nil
}

// GetDevicePluginOptions returns the options supported by the device plugin.
func (s *Server) GetDevicePluginOptions(ctx context.Context, req *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

// ListAndWatch streams the list of devices, sending initial list and subsequent updates.
func (s *Server) ListAndWatch(req *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	klog.InfoS("ListAndWatch started", "resource", s.Manager.ResourceName)

	// Refresh the CDI cache to make sure we see all specs on disk.
	if err := cdi.Refresh(); err != nil {
		klog.ErrorS(err, "failed to refresh CDI cache")
	}

	// build initial device list from existing CDI spec files
	var initDevices []*pluginapi.Device
	cache := cdi.GetDefaultCache()
	for _, vendor := range cache.ListVendors() {
		klog.V(4).InfoS("Enumerating CDI specs", "vendor", vendor)
		for _, spec := range cache.GetVendorSpecs(vendor) {
			id := filepath.Base(spec.GetPath())
			klog.V(4).InfoS("Adding device from spec", "path", spec.GetPath(), "id", id)
			initDevices = append(initDevices, &pluginapi.Device{ID: id, Health: pluginapi.Healthy})
		}
	}

	// Detect system reboot by comparing in-use allocations with on-disk CDI specs
	// TODO : filter out CDI specs and AllocationClaims that are not relevant to this server's resource
	// because it will race with other servers that will be running on the same node.
	migProfile := profileFromResourceName(s.Manager.ResourceName)
	migProfile = unsanitizeProfileName(migProfile)
	key := fmt.Sprintf("%s/%s", s.NodeName, migProfile)

	var inUseAllocs []*instav1.AllocationClaim
	if allocationIndexer != nil {
		s.allocMutex.Lock()
		objs, err := allocationIndexer.ByIndex("node-MigProfile", key)
		s.allocMutex.Unlock()
		if err != nil {
			klog.ErrorS(err, "failed to lookup allocations by index", "key", key)
		} else {
			for _, obj := range objs {
				if a, ok := obj.(*instav1.AllocationClaim); ok {
					if a.Status.State == instav1.AllocationClaimStatusInUse {
						inUseAllocs = append(inUseAllocs, a)
					}
				}
			}
		}
	}

	if len(inUseAllocs) != len(initDevices) {
		klog.InfoS("Detected reboot, resetting allocations", "resource", s.Manager.ResourceName, "inUse", len(inUseAllocs), "specs", len(initDevices))
		for _, alloc := range inUseAllocs {
			alloc.Status.State = instav1.AllocationClaimStatusCreated
			updated := alloc
			if s.InstasliceClient != nil {
				var err error
				updated, err = UpdateAllocationStatus(stream.Context(), s.InstasliceClient, alloc, instav1.AllocationClaimStatusCreated)
				if err != nil {
					klog.ErrorS(err, "failed to update allocation status", "allocation", alloc.Name)
					continue
				}
			} else {
				cond := metav1.Condition{
					Type:               "State",
					Status:             metav1.ConditionTrue,
					Reason:             string(instav1.AllocationClaimStatusCreated),
					Message:            fmt.Sprintf("Allocation is %s", instav1.AllocationClaimStatusCreated),
					ObservedGeneration: alloc.Generation,
				}
				meta.SetStatusCondition(&updated.Status.Conditions, cond)
			}

			s.allocMutex.Lock()
			if err := allocationIndexer.Update(updated); err != nil {
				klog.ErrorS(err, "failed to update allocation in indexer", "allocation", updated.Name)
			}
			s.allocMutex.Unlock()
		}
	}

	// send initial device list
	if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: initDevices}); err != nil {
		return fmt.Errorf("failed to send initial device list: %w", err)
	}

	// stream updates
	for {
		select {
		case <-stream.Context().Done():
			klog.InfoS("ListAndWatch stopped", "resource", s.Manager.ResourceName)
			return nil
		case devs := <-s.Manager.Updates():
			klog.InfoS("ListAndWatch sending update", "resource", s.Manager.ResourceName, "devices", devs)
			if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: devs}); err != nil {
				return fmt.Errorf("failed to send device update: %w", err)
			}
		}
	}
}

func (s *Server) Allocate(ctx context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	klog.InfoS("Received Allocate request", "containerRequests", req.GetContainerRequests(), "emulatedMode", s.EmulatedMode)

	total := 0
	for _, cr := range req.GetContainerRequests() {
		ids := cr.GetDevicesIDs()
		if len(ids) == 0 {
			total++
		} else {
			total += len(ids)
		}
	}

	resp := &pluginapi.AllocateResponse{
		ContainerResponses: make([]*pluginapi.ContainerAllocateResponse, len(req.GetContainerRequests())),
	}

	allocations, err := s.getAllocationsByNodeGPU(ctx, s.NodeName, s.Manager.ResourceName, total)
	if err != nil {
		klog.ErrorS(err, "failed to get allocations", "node", s.NodeName, "profile", s.Manager.ResourceName)
	} else {
		klog.InfoS("Fetched allocations for Allocate", "count", len(allocations), "node", s.NodeName, "profile", s.Manager.ResourceName)
		klog.V(5).InfoS("Allocations", "allocations", allocations)
	}

	allocIndex := 0
	for i, cr := range req.GetContainerRequests() {
		resp.ContainerResponses[i] = &pluginapi.ContainerAllocateResponse{}

		ids := cr.GetDevicesIDs()
		if len(ids) == 0 {
			ids = []string{string(utiluuid.NewUUID())}
		}

		for _, id := range ids {
			var alloc *instav1.AllocationClaim
			if allocIndex < len(allocations) {
				alloc = allocations[allocIndex]
				allocIndex++
			}

			envVar, annotations, err := s.prepareEnv(ctx, alloc)
			if err != nil {
				return nil, err
			}

			_, cdiDevices, err := WriteCDISpecForResource(s.Manager.ResourceName, id, annotations, envVar)
			if err != nil {
				return nil, err
			}
			resp.ContainerResponses[i].CDIDevices = append(resp.ContainerResponses[i].CDIDevices, cdiDevices...)

			if alloc != nil {
				if err := s.markAllocationInUse(ctx, alloc); err != nil {
					return nil, err
				}
			}
		}
	}

	return resp, nil
}

func (s *Server) PreStartContainer(ctx context.Context, req *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

// prepareEnv creates the environment variable and annotations for a container
// based on the given AllocationClaim. If a claim is provided, it also ensures a
// ConfigMap with the NVIDIA_VISIBLE_DEVICES value exists on the node.
func (s *Server) prepareEnv(ctx context.Context, alloc *instav1.AllocationClaim) (string, map[string]string, error) {
	envVar := "NVIDIA_VISIBLE_DEVICES=test"
	var annotations map[string]string

	if alloc != nil {
		if s.EmulatedMode == instav1.EmulatedModeDisabled {
			uuid, err := s.createMigSlice(alloc)
			if err != nil {
				klog.ErrorS(err, "failed to create MIG slice")
				return "", nil, err
			}
			envVar = fmt.Sprintf("NVIDIA_VISIBLE_DEVICES=%s", uuid)
		}

		if data, err := json.Marshal(alloc); err == nil {
			annotations = map[string]string{allocationAnnotationKey: string(data)}
		} else {
			klog.ErrorS(err, "failed to marshal allocation")
		}

		if s.KubeClient != nil {
			s.ensureEnvConfigMap(ctx, alloc, envVar)
		}
	}

	return envVar, annotations, nil
}

// ensureEnvConfigMap creates or updates a ConfigMap storing the environment
// variable for the pod referenced by the AllocationClaim.
func (s *Server) ensureEnvConfigMap(ctx context.Context, alloc *instav1.AllocationClaim, envVar string) {
	specObj, err := getAllocationClaimSpec(alloc)
	if err != nil {
		klog.ErrorS(err, "failed to decode allocation spec for configmap", "allocation", alloc.Name)
		return
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      specObj.PodRef.Name,
			Namespace: specObj.PodRef.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Pod",
					Name:       specObj.PodRef.Name,
					UID:        specObj.PodRef.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Data: map[string]string{
			"NVIDIA_VISIBLE_DEVICES": strings.TrimPrefix(envVar, "NVIDIA_VISIBLE_DEVICES="),
			"CUDA_VISIBLE_DEVICES":   strings.TrimPrefix(envVar, "NVIDIA_VISIBLE_DEVICES="),
		},
	}

	if _, err := s.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, getErr := s.KubeClient.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
			if getErr != nil {
				klog.ErrorS(getErr, "failed to get existing ConfigMap", "configMap", cm.Name)
				return
			}

			nvidiaValue := strings.TrimPrefix(envVar, "NVIDIA_VISIBLE_DEVICES=")
			cudaValue := strings.TrimPrefix(envVar, "NVIDIA_VISIBLE_DEVICES=")

			if v, ok := existing.Data["NVIDIA_VISIBLE_DEVICES"]; ok && v != "" {
				nvidiaValue = fmt.Sprintf("%s,%s", v, nvidiaValue)
			}
			if v, ok := existing.Data["CUDA_VISIBLE_DEVICES"]; ok && v != "" {
				cudaValue = fmt.Sprintf("%s,%s", v, cudaValue)
			}

			existing.Data["NVIDIA_VISIBLE_DEVICES"] = nvidiaValue
			existing.Data["CUDA_VISIBLE_DEVICES"] = cudaValue

			if _, err := s.KubeClient.CoreV1().ConfigMaps(existing.Namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
				klog.ErrorS(err, "failed to update ConfigMap", "configMap", existing.Name)
			}
		} else {
			klog.ErrorS(err, "failed to create ConfigMap", "configMap", cm.Name)
		}
	} else {
		klog.InfoS("created ConfigMap for env var", "configMap", cm.Name)
	}
}

// markAllocationInUse updates the AllocationClaim status to InUse and stores it
// in the informer indexer.
func (s *Server) markAllocationInUse(ctx context.Context, alloc *instav1.AllocationClaim) error {
	alloc.Status.State = instav1.AllocationClaimStatusInUse
	updated := alloc
	if s.InstasliceClient != nil {
		var err error
		updated, err = UpdateAllocationStatus(ctx, s.InstasliceClient, alloc, instav1.AllocationClaimStatusInUse)
		if err != nil {
			klog.ErrorS(err, "failed to update allocation status", "allocation", alloc.Name, "status", instav1.AllocationClaimStatusInUse)
			return err
		}
	} else {
		cond := metav1.Condition{
			Type:               "State",
			Status:             metav1.ConditionTrue,
			Reason:             string(instav1.AllocationClaimStatusInUse),
			Message:            fmt.Sprintf("Allocation is %s", instav1.AllocationClaimStatusInUse),
			ObservedGeneration: alloc.Generation,
		}
		meta.SetStatusCondition(&updated.Status.Conditions, cond)
	}

	s.allocMutex.Lock()
	if err := allocationIndexer.Update(updated); err != nil {
		klog.ErrorS(err, "failed to update allocation in indexer", "allocation", updated.Name)
	}
	s.allocMutex.Unlock()
	return nil
}

// createMigSlice uses the NVML library to create a MIG slice as specified by
// the AllocationClaim. It looks up the GI and CI profile IDs from the
// discovered node resources stored in the Manager.
func (s *Server) createMigSlice(alloc *instav1.AllocationClaim) (string, error) {
	spec, err := getAllocationClaimSpec(alloc)
	if err != nil {
		return "", err
	}
	mig, ok := s.Manager.resources.MigPlacement[spec.Profile]
	if !ok {
		return "", fmt.Errorf("profile %s not found", spec.Profile)
	}

	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return "", fmt.Errorf("nvml init failed: %v", ret)
	}
	defer func() {
		if ret := nvml.Shutdown(); ret != nvml.SUCCESS {
			klog.ErrorS(fmt.Errorf("nvml shutdown failed: %v", ret), "nvml shutdown")
		}
	}()

	dev, ret := nvml.DeviceGetHandleByUUID(spec.GPUUUID)
	if ret != nvml.SUCCESS {
		return "", fmt.Errorf("get device %s: %v", spec.GPUUUID, ret)
	}

	giInfo, ret := dev.GetGpuInstanceProfileInfo(int(mig.GIProfileID))
	if ret != nvml.SUCCESS {
		return "", fmt.Errorf("get GI profile info: %v", ret)
	}

	placement := nvml.GpuInstancePlacement{Start: uint32(spec.MigPlacement.Start), Size: uint32(spec.MigPlacement.Size)}
	gpuInst, ret := dev.CreateGpuInstanceWithPlacement(&giInfo, &placement)
	if ret != nvml.SUCCESS {
		return "", fmt.Errorf("create GPU instance: %v", ret)
	}

	ciInfo, ret := gpuInst.GetComputeInstanceProfileInfo(int(mig.CIProfileID), 0)
	if ret != nvml.SUCCESS {
		if ret := gpuInst.Destroy(); ret != nvml.SUCCESS {
			klog.ErrorS(fmt.Errorf("destroy gpu instance: %v", ret), "destroy gpu instance")
		}
		return "", fmt.Errorf("get CI profile info: %v", ret)
	}

	ci, ret := gpuInst.CreateComputeInstance(&ciInfo)
	if ret != nvml.SUCCESS {
		if ret := gpuInst.Destroy(); ret != nvml.SUCCESS {
			klog.ErrorS(fmt.Errorf("destroy gpu instance: %v", ret), "destroy gpu instance")
		}
		return "", fmt.Errorf("create compute instance: %v", ret)
	}

	uuid, err := migUUIDFromInstance(dev, gpuInst, ci)
	if err != nil {
		if ret := ci.Destroy(); ret != nvml.SUCCESS {
			klog.ErrorS(fmt.Errorf("destroy compute instance: %v", ret), "destroy compute instance")
		}
		if ret := gpuInst.Destroy(); ret != nvml.SUCCESS {
			klog.ErrorS(fmt.Errorf("destroy gpu instance: %v", ret), "destroy gpu instance")
		}
		return "", err
	}

	klog.InfoS("Created MIG slice", "gpu", spec.GPUUUID, "profile", spec.Profile, "migUUID", uuid)
	return uuid, nil
}

// migUUIDFromInstance finds the UUID of the MIG device corresponding to the
// given compute instance.
func migUUIDFromInstance(parent nvml.Device, gi nvml.GpuInstance, ci nvml.ComputeInstance) (string, error) {
	ciInfo, ret := ci.GetInfo()
	if ret != nvml.SUCCESS {
		return "", fmt.Errorf("get compute instance info: %v", ret)
	}

	giInfo, ret := gi.GetInfo()
	if ret != nvml.SUCCESS {
		return "", fmt.Errorf("get gpu instance info: %v", ret)
	}

	count, ret := parent.GetMaxMigDeviceCount()
	if ret != nvml.SUCCESS {
		return "", fmt.Errorf("get MIG device count: %v", ret)
	}

	for i := 0; i < count; i++ {
		migDev, ret := parent.GetMigDeviceHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}
		giID, ret := nvml.DeviceGetGpuInstanceId(migDev)
		if ret != nvml.SUCCESS {
			continue
		}
		ciID, ret := nvml.DeviceGetComputeInstanceId(migDev)
		if ret != nvml.SUCCESS {
			continue
		}
		if uint32(giID) == giInfo.Id && uint32(ciID) == ciInfo.Id {
			uuid, ret := migDev.GetUUID()
			if ret != nvml.SUCCESS {
				return "", fmt.Errorf("get MIG UUID: %v", ret)
			}
			return uuid, nil
		}
	}

	return "", fmt.Errorf("matching MIG device not found")
}

func profileFromResourceName(res string) string {
	parts := strings.Split(res, "/")
	return parts[len(parts)-1]
}

func (s *Server) getAllocationsByNodeGPU(ctx context.Context, nodeName, profileName string, count int) ([]*instav1.AllocationClaim, error) {
	if count <= 0 {
		return nil, fmt.Errorf("requested allocation count must be greater than zero")
	}
	if allocationIndexer == nil {
		return nil, fmt.Errorf("allocation indexer not initialized")
	}

	migProfile := profileFromResourceName(profileName)
	migProfile = unsanitizeProfileName(migProfile)
	key := fmt.Sprintf("%s/%s", nodeName, migProfile)
	var result []*instav1.AllocationClaim

	// First fetch the requested AllocationClaims from the indexer. We retry
	// a few times in case the informer cache hasn't synced yet.
	err := wait.ExponentialBackoff(wait.Backoff{Duration: 100 * time.Millisecond, Factor: 2, Steps: 5}, func() (bool, error) {
		s.allocMutex.Lock()
		defer s.allocMutex.Unlock()

		objs, err := allocationIndexer.ByIndex("node-MigProfile", key)
		if err != nil {
			return false, err
		}

		out := make([]*instav1.AllocationClaim, 0, len(objs))
		for _, obj := range objs {
			if a, ok := obj.(*instav1.AllocationClaim); ok {
				spec, err := getAllocationClaimSpec(a)
				if err != nil {
					klog.ErrorS(err, "failed to decode allocation spec")
					continue
				}
				if spec.Profile == migProfile && a.Status.State == instav1.AllocationClaimStatusCreated {
					out = append(out, a)
					if len(out) == count {
						break
					}
				}
			}
		}
		result = out
		return len(result) >= count, nil
	})
	if err != nil {
		if wait.Interrupted(err) {
			return nil, fmt.Errorf("requested %d allocations but only found %d", count, len(result))
		}
		return nil, err
	}

	// Update each claim status to Processing outside of the fetch loop so
	// failures here don't masquerade as cache lookup errors.
	for _, a := range result[:count] {
		a.Status.State = instav1.AllocationClaimStatusProcessing
		if s.InstasliceClient != nil {
			if _, err := UpdateAllocationStatus(ctx, s.InstasliceClient, a, instav1.AllocationClaimStatusProcessing); err != nil {
				klog.ErrorS(err, "failed to update allocation status", "allocation", a.Name, "status", instav1.AllocationClaimStatusProcessing)
				return nil, fmt.Errorf("failed to update allocation status for %q: %w", a.Name, err)
			}
		}

		s.allocMutex.Lock()
		if err := allocationIndexer.Update(a); err != nil {
			klog.ErrorS(err, "failed to update allocation in indexer", "allocation", a.Name)
		}
		s.allocMutex.Unlock()

		klog.InfoS("Updated allocation status to Processing", "allocation", a.Name, "status", a.Status.State)
	}

	return result[:count], nil
}

// BuildCDIDevices builds a CDI spec and returns the spec object, spec name,
// spec path, and the corresponding CDIDevice slice. This helper is exported so
// that other packages (and tests) can generate CDI specs in a consistent way.
func BuildCDIDevices(kind, sanitizedClass, id string, annotations map[string]string, envVar string) (*cdispec.Spec, string, string, []*pluginapi.CDIDevice) {
	specNameBase := fmt.Sprintf("%s_%s", sanitizedClass, id)
	specName := specNameBase + ".cdi.json"

	dynamicDir := cdi.DefaultDynamicDir
	dirs := cdi.GetDefaultCache().GetSpecDirectories()
	if len(dirs) > 0 {
		dynamicDir = dirs[len(dirs)-1]
	}
	specPath := filepath.Join(dynamicDir, specName)

	// TODO - Do we need to create a CDI spec for each device Allocate request? can we not use a single spec for all devices of the same kind?
	env := []string{"NVIDIA_VISIBLE_DEVICES=test", "CUDA_VISIBLE_DEVICES=test"}
	if envVar != "" {
		env = []string{envVar}
		if eq := strings.Index(envVar, "="); eq != -1 {
			val := envVar[eq+1:]
			env = append(env, fmt.Sprintf("CUDA_VISIBLE_DEVICES=%s", val))
		}
	}
	specObj := &cdispec.Spec{
		Version: cdispec.CurrentVersion,
		Kind:    kind,
		Devices: []cdispec.Device{
			{
				Name:        id,
				Annotations: annotations,
				ContainerEdits: cdispec.ContainerEdits{
					Env: env,
					Hooks: []*cdispec.Hook{
						{
							HookName: "poststop",
							Path:     "/bin/rm",
							Args:     []string{"-f", specPath},
						},
					},
				},
			},
		},
	}

	cdiDevices := make([]*pluginapi.CDIDevice, len(specObj.Devices))
	for j, dev := range specObj.Devices {
		cdiDevices[j] = &pluginapi.CDIDevice{
			Name: fmt.Sprintf("%s=%s", kind, dev.Name),
		}
	}
	return specObj, specName, specPath, cdiDevices
}

// WriteCDISpecForResource parses the given resource name, generates a CDI spec
// using BuildCDIDevices and writes it to the CDI cache. It returns the path to
// the written spec along with the generated CDIDevices.
func WriteCDISpecForResource(resourceName string, id string, annotations map[string]string, envVar string) (string, []*pluginapi.CDIDevice, error) {
	vendor, class := parser.ParseQualifier(resourceName)
	sanitizedClass := class
	if err := parser.ValidateClassName(sanitizedClass); err != nil {
		sanitizedClass = "c" + sanitizedClass
	}
	kind := sanitizedClass
	if vendor != "" {
		kind = vendor + "/" + sanitizedClass
	}

	specObj, specName, specPath, cdiDevices := BuildCDIDevices(kind, sanitizedClass, id, annotations, envVar)

	// Wait for any previous spec with the same name to be removed. This is
	// important for transient specs tied to container lifecycles. The
	// removal is triggered by the poststop hook in the spec itself.
	// Wait up to three minutes, checking every second, for the old spec
	// file to disappear before writing the replacement.
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		_, statErr := os.Stat(specPath)
		if statErr == nil {
			return false, nil
		}
		if errors.Is(statErr, os.ErrNotExist) {
			return true, nil
		}
		return false, statErr
	})
	if err != nil {
		return "", nil, fmt.Errorf("timed out waiting for old CDI spec %q to be removed: %w", specName, err)
	}

	if err := cdi.GetDefaultCache().WriteSpec(specObj, specName); err != nil {
		klog.ErrorS(err, "failed to write CDI spec", "name", specName)
		return "", nil, fmt.Errorf("failed to write CDI spec %q: %w", specName, err)
	}
	klog.InfoS("wrote CDI spec", "name", specName)

	return specPath, cdiDevices, nil
}

// writeCDISpecForResource is kept for backwards compatibility with older code.
// It simply calls the exported WriteCDISpecForResource function and discards the
// returned spec path.
