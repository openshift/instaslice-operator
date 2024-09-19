# Emulator mode in InstaSlice

Finding MIGable GPUs with cloud provider is expensive and hard especially for development activities Hence InstaSlice emulator mode was created. When emulator mode on users create a hypothetical capacity in the cluster by setting GPU profiles in the InstaSlice CR. The controller and daemonset both operator on InstaSlice object. This shows InstaSlice unique placement capabilities and enables development of InstaSlice without any physical GPUs on the cluster.

# Steps to use emulator

We use Kustomize to enabled emulator mode.

- Ensure the cert-manager is deployed
```console
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml
```
- Deploy the controller using command

```console
make deploy-emulated
```
- Add GPU capacity to the cluster using command

```console
kubectl apply -f test/e2e/resources/instaslice-fake-capacity.yaml
```

- Check if InstaSlice object exists using command

```console
kubectl describe instaslice
```

- Add accelerator memory resource to the fake node object named kind-control-plane, accelerator memory is 80GB as we have two fake A100s GPUs each having 40 GB of GPU memory

```console
kubectl patch node kind-control-plane --subresource=status --type=json -p='[{"op":"add","path":"/status/capacity/nvidia.com~1accelerator-memory","value":"80Gi"}]'
```

- Submit emulator pod using command

```console
kubectl apply -f samples/emulator-pod.yaml
```

- Check allocations section and prepared sections of the InstaSlice object.
    - Allocation section shows placement decisions made by InstaSlice using firstfit algorithm
    - Prepared section is a mock, as no real GPU exists CI and GI for any pod submissions are always 0

- Undeploy using command

```console
make undeploy-emulated
```