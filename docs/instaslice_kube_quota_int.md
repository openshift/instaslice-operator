# Kubernetes quota management and InstaSlice

Quota management in Kubernetes is essential for controlling and managing the allocation and consumption of resources within a cluster. It ensures fair and efficient resource utilization, prevents resource contention, and has a promise to provide stability in a multi-tenant environment. 

We use GPU memory for quota management because GPU memory is dominant factor to create new MIG, for instance check out profile 5 in this document: https://docs.nvidia.com/datacenter/tesla/mig-user-guide/index.html#a100-profiles . Single CI is lost due to unavailability of GI or GPU memory, also most inference servers will keep KV cache and inference requests in GPU memory making GPU memory a scarce resource.

# Integration steps

- Apply sample quota present

```console
    kubectl apply -f samples/resource-quota.yaml
```

- Submit sample pod

```console
    kubectl apply -f samples/test-pod.yaml
```

- When the pod starts running quota should be exhausted, submit the same pod with different name

```console
    kubectl apply -f samples/test-pod.yaml
```
- Kubernetes quota's should block the request until the previous pod completes or is deleted from the system.