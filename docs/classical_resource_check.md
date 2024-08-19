# Resource check mechanism for InstaSlice

InstaSlice has implemented slice aware resource check for devices like A100s and H100s to place the next slice. Resource checking for slices is crucial due to limited profiles that are supported by vendors like Nvidia and the number of supported profiles change as the hardware changes from the same vendor, for instance, A30 has 4 profiles while A100 has 7 profiles.

For some form of guarantee to execute workloads it is important to perform resource checks on classical resources like CPU and memory at the minimum.

From an overall consistency point of view, since we already do check slice availability on the node/GPU, it could make sense to continue the trend and perform the same check for classical resources like CPU and memory.

We have settled down on resource checks for correctness and it has the path to integrate with Kueue in the future.

# Use case

Interactive workloads like Jupyter notebooks or serving workloads like vLLM may require more RAM that the GPU memory for caching or loading the data as a common use case. checking classical resources will help InstaSlice get a better placement for such workloads.

# Implementation details:

## Change in CRD:
- Daemonset at boot time gets CPU and memory available to allocate on node
- Allocation section for each allocation to include two fields
    - CPU
    - Memory 
- As we “soft” reserve a GPU slice for allocation that is not in a deleted state, the same will be the case for classical resources per pod.
- Next pod allocation will be delta between resources reported by the daemonset and all the previous pod allocations.
- Nodes managed by InstaSlice should be tainted.


## Use case for resource check:

- Node Autoscaling:
    -  Perform resource checks for classical and GPU slices availability it would be easier to get a node shape to trigger node autoscaling.
    - Guaranteed job execution

## Issues:
- Failed state not implemented:
- Pod could fail and eventually becomes a hogger in the system 
- GPU or a node could fail causing allocation to not be deleted from the InstaSlice Object.
