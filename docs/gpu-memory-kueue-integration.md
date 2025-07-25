# GPU Memory Integration with Kueue

This document describes how the Dynamic Accelerator Slicer (DAS) operator integrates GPU memory as an extended resource for Kueue workload scheduling.

## Overview

The DAS operator now supports exposing GPU memory as an extended resource (`gpu.das.com/mem`) that can be used by Kueue for workload scheduling. This allows Kueue to dispatch workloads based on GPU memory availability without modifying node allocatable fields.

## Architecture

The GPU memory integration follows the same pattern as MIG profile resources:

1. **Webhook Injection**: When users request MIG profiles (e.g., `nvidia.com/mig-1g.5gb`), the webhook automatically injects the corresponding GPU memory resource (`gpu.das.com/mem: 5`).

2. **Device Plugin**: The device plugin advertises GPU memory as an extended resource, where each "device" represents 1GB of memory.

3. **Kueue Integration**: Kueue can use the `gpu.das.com/mem` resource for workload queuing and scheduling decisions.

## Resource Mapping

| MIG Profile | GPU Memory (GB) | Extended Resource |
|-------------|-----------------|-------------------|
| `1g.5gb`    | 5               | `gpu.das.com/mem: 5` |
| `2g.10gb`   | 10              | `gpu.das.com/mem: 10` |
| `3g.20gb`   | 20              | `gpu.das.com/mem: 20` |

## Usage Examples

### Basic Pod with GPU Memory

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gpu-example
spec:
  containers:
  - name: gpu-app
    image: nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8
    resources:
      limits:
        nvidia.com/mig-1g.5gb: "2"  # 2 instances of 5GB each
        # Webhook automatically injects: gpu.das.com/mem: "10"
      requests:
        nvidia.com/mig-1g.5gb: "2"
        # Webhook automatically injects: gpu.das.com/mem: "10"
```

### Kueue Configuration

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: gpu-cluster-queue
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory", "gpu.das.com/mem"]
    flavors:
    - name: default
      resources:
      - name: cpu
        nominalQuota: 100
      - name: memory
        nominalQuota: 100Gi
      - name: gpu.das.com/mem
        nominalQuota: 50  # 50GB total GPU memory
```

## Implementation Details

### Webhook Changes

The webhook now includes logic to:
- Extract memory requirements from MIG profile names using regex pattern `(\d+)g\.(\d+)gb`
- Automatically inject `gpu.das.com/mem` resources based on the extracted memory value
- Multiply the memory requirement by the requested quantity

### Device Plugin Changes

The device plugin now:
- Creates a separate manager for GPU memory resources
- Advertises devices representing 1GB memory units
- Tracks total available GPU memory across all GPUs on the node

## Benefits

1. **Kueue Integration**: Enables Kueue to schedule workloads based on GPU memory availability
2. **No Node Modifications**: Avoids the need to patch node allocatable fields
3. **Automatic Resource Injection**: Users only need to specify MIG profiles, GPU memory is handled automatically
4. **Consistent Resource Management**: GPU memory resources work alongside existing MIG profile resources

## Limitations

1. **Memory Granularity**: GPU memory is tracked in 1GB units
2. **Profile Dependency**: GPU memory injection only works for MIG profiles with memory specifications
3. **Single Resource Type**: Currently only supports `gpu.das.com/mem` as the GPU memory resource

## Troubleshooting

### Check GPU Memory Resource Availability

```bash
# Check if GPU memory devices are advertised
kubectl describe node <node-name> | grep gpu.das.com/mem

# Check device plugin logs
kubectl logs -n das-operator -l app.kubernetes.io/name=das-operator-daemonset
```

### Verify Webhook Injection

```bash
# Check webhook logs for GPU memory injection
kubectl logs -n das-operator -l app.kubernetes.io/name=das-operator-webhook

# Verify pod mutation
kubectl get pod <pod-name> -o yaml | grep gpu.das.com/mem
```

## Future Enhancements

1. **Multiple Memory Resources**: Support for different memory resource types (e.g., `gpu.das.com/mem-hbm`, `gpu.das.com/mem-ddr`)
2. **Dynamic Memory Allocation**: Support for variable memory allocation within MIG profiles
3. **Memory Reservation**: Support for reserving GPU memory for specific workloads
4. **Memory Monitoring**: Integration with GPU memory monitoring and metrics