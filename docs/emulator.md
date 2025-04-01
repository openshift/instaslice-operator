# Emulator mode in InstaSlice

Finding MIGable GPUs with cloud provider is expensive and hard especially for development activities Hence InstaSlice emulator mode was created. When emulator mode on users create a hypothetical capacity in the cluster by setting GPU profiles in the InstaSlice CR. The controller and daemonset both operator on InstaSlice object. This shows InstaSlice unique placement capabilities and enables development of InstaSlice without any physical GPUs on the cluster.

# Steps to use emulator

We use Kustomize to enabled emulator mode.

- Ensure the cert-manager is deployed
```console
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml
```

- Install the Instaslice CRDs using command

```console
make install
```

- Deploy the controller using command

```console
make deploy-emulated
```

- Wait for controllers to be ready

```console
kubectl get pods -n instaslice-system --watch
```


```
NAME                                                      READY   STATUS    RESTARTS   AGE
instaslice-operator-controller-daemonset-fbwmk            1/1     Running   0          118s
instaslice-operator-controller-manager-579bc859cf-pb9t6   2/2     Running   0          118s
```

-  Check if InstaSlice object exists using command

```console
kubectl describe instaslice -n instaslice-system
```

> [!IMPORTANT]
> Make sure no dangling instaslice.redhat.com/* extended resources exist on the cluster before you submit a pod

- Submit emulator pod using command

```console
kubectl apply -f samples/emulator-pod.yaml
```

OR

- Use minimal pod for other architectures such as, arm64

```console
kubectl apply -f samples/minimal-pod.yaml
```

- Check allocations section and prepared sections of the InstaSlice object.
    - Allocation section shows placement decisions made by InstaSlice using firstfit algorithm
    - Prepared section is a mock, as no real GPU exists CI and GI for any pod submissions are always 0

Example:
```
Allocations:
    d54cdcfe-74b6-42c5-89cf-dc9caac2c714:
      Allocation Status:    ungated
      Cpu:                  0
      Gpu UUID:             GPU-31cfe05c-ed13-cd17-d7aa-c63db5108c24
      Memory:               0
      Namespace:            default
      Nodename:             kind-control-plane
      Pod Name:             minimal-pod-1
      Pod UUID:             d54cdcfe-74b6-42c5-89cf-dc9caac2c714
      Profile:              1g.5gb
      Resource Identifier:  414f6507-6118-405a-9ffa-20c6ca63803a
      Size:                 1
      Start:                0
Prepared:
    d54cdcfe-74b6-42c5-89cf-dc9caac2c714:
      Ciinfo:    0
      Giinfo:    0
      Parent:    GPU-31cfe05c-ed13-cd17-d7aa-c63db5108c24
      Pod UUID:  d54cdcfe-74b6-42c5-89cf-dc9caac2c714
      Profile:   1g.5gb
      Size:      1
      Start:     0
```

- Undeploy using command

```console
make undeploy-emulated
```
