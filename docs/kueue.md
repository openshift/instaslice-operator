# Kueue + InstaSlice Integration Demo

The following setup uses a [Kind](https://kind.sigs.k8s.io) cluster with fake
MIG-enabled GPUs and InstaSlice running in emulator mode to confirm that
InstaSlice allocates MIG slices for queued pods only once admitted by
[Kueue](https://kueue.sigs.k8s.io).

Create a Kind cluster:
```sh
kind create cluster
```

Deploy cert manager:
```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml
```

Wait for cert manager to be ready.

Deploy InstaSlice in emulator mode using Kueue-enabled images:
```sh
IMG=quay.io/tardieu/instaslicev2-controller:kueue IMG_DMST=quay.io/tardieu/instaslicev2-daemonset:kueue make deploy-emulated
```

Wait for InstaSlice to be ready.

Add fake GPU capacity to the cluster:
```sh
kubectl apply -f test/e2e/resources/instaslice-fake-capacity.yaml
kubectl patch node kind-control-plane --subresource=status --type=json -p='[{"op":"add","path":"/status/capacity/nvidia.com~1accelerator-memory","value":"80Gi"}]'
```

Deploy Kueue:
```sh
kubectl apply --server-side -f docs/kueue/manifests.yaml
```
The provided [manifests.yaml](../docs/kueue/manifests.yaml) deploys Kueue
[v0.8.1](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.8.1). It
enables the optional, opt-in [pod
integration](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) and adds
`org.instaslice/` and `nvidia.com/accelerator-memory` to Kueue's
[excludeResourcePrefixes](https://kueue.sigs.k8s.io/docs/reference/kueue-config.v1beta1/#Resources).

Wait for Kueue to be ready.

Configure a default flavor, a cluster queue, and a local queue in the default
namespace with quota of 3 `nvidia.com/mig-1g.5gb` slices:
```sh
kubectl apply -f docs/kueue/sample-queues.yaml
```

Queue 7 pods:
```sh
kubectl apply -f docs/kueue/sample-pods.yaml
```

Check that at most 3 pods are running at a time:
```sh
kubectl get pods
```
```
NAME   READY   STATUS            RESTARTS   AGE
p1     0/1     SchedulingGated   0          15s
p2     1/1     Running           0          15s
p3     1/1     Running           0          15s
p4     1/1     Running           0          15s
p5     0/1     SchedulingGated   0          15s
p6     0/1     SchedulingGated   0          15s
p7     0/1     SchedulingGated   0          15s
```

Confirm that InstaSlice does not create 7 slices ahead of time:
```sh
kubectl get node kind-control-plane -o json | jq .status.capacity
```
```
{
  "cpu": "8",
  "ephemeral-storage": "102625208Ki",
  "hugepages-1Gi": "0",
  "hugepages-2Mi": "0",
  "hugepages-32Mi": "0",
  "hugepages-64Ki": "0",
  "memory": "16351912Ki",
  "nvidia.com/accelerator-memory": "80Gi",
  "nvidia.com/mig-1g.5gb": "3",
  "org.instaslice/358bb6d7-b65b-4a0c-9585-2567c1ce89e2": "1",
  "org.instaslice/358d2198-eab4-4ac8-9e25-5c7b67187dac": "1",
  "org.instaslice/79fcac9e-3be1-4fc2-892c-78238c2c405c": "1",
  "org.instaslice/99ba54ca-dfcd-4942-a770-6e144d69fd9b": "1",
  "pods": "110"
}
```

To cleanup, delete the Kind cluster:
```sh
kind delete cluster
```
