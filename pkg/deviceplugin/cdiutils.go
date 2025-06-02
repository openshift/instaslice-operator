package deviceplugin

import (
	"fmt"
	"path/filepath"

	klog "k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	"tags.cncf.io/container-device-interface/pkg/cdi"
	parser "tags.cncf.io/container-device-interface/pkg/parser"
)

// BuildCDIDevices builds a CDI spec and returns the spec object, spec name, spec path, and the corresponding CDIDevices slice.
func BuildCDIDevices(kind, sanitizedClass, vendor, id string) (*cdispec.Spec, string, string, []*pluginapi.CDIDevice) {
	specNameBase := fmt.Sprintf("%s_%s", sanitizedClass, id)
	specName := specNameBase + ".cdi.json"
	specPath := filepath.Join(cdi.DefaultDynamicDir, specName)

	specObj := &cdispec.Spec{
		Version: cdispec.CurrentVersion,
		Kind:    kind,
		Devices: []cdispec.Device{
			{
				Name: "dev0",
				ContainerEdits: cdispec.ContainerEdits{
					Env: []string{"ABCD=test"},
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

// WriteCDISpecForResource parses the given resource name, generates a CDI spec and writes it to cache.
// It returns the CDIDevices slice and environment variables to set for the container.
func WriteCDISpecForResource(resourceName string, id string) ([]*pluginapi.CDIDevice, error) {
	vendor, class := parser.ParseQualifier(resourceName)
	sanitizedClass := class
	if err := parser.ValidateClassName(sanitizedClass); err != nil {
		sanitizedClass = "c" + sanitizedClass
	}
	kind := sanitizedClass
	if vendor != "" {
		kind = vendor + "/" + sanitizedClass
	}

	specObj, specName, _, cdiDevices := BuildCDIDevices(kind, sanitizedClass, vendor, id)
	if err := cdi.GetDefaultCache().WriteSpec(specObj, specName); err != nil {
		klog.ErrorS(err, "failed to write CDI spec", "name", specName)
		return nil, fmt.Errorf("failed to write CDI spec %q: %w", specName, err)
	}
	klog.InfoS("wrote CDI spec", "name", specName)

	return cdiDevices, nil
}
