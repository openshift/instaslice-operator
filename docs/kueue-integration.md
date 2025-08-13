# Kueue Integration with Dynamic Accelerator Slicer (DAS) Operator for Serving Use Cases
---

## Summary

This document describes how the DAS operator integrates with Kueue to enable GPU memory-aware scheduling for AI/ML serving workloads. By exposing GPU memory as an extended resource (`gpu.das.openshift.io/mem`), Kueue can enforce quotas and admission control while DAS continues to manage MIG slice allocation. The integration focuses on long-running workloads (Pods, Deployments, StatefulSets) commonly used for inference and serving applications.

---

## Problem Statement

Organizations deploying AI/ML serving workloads face a fundamental challenge: GPUs are extremely expensive and scarce, yet multiple teams need to deploy inference services simultaneously. Without proper resource management, this leads to resource contention, underutilized GPUs, and failed deployments. Addressing this requires both better utilization of GPU hardware and smarter coordination of serving workload scheduling.

The DAS Operator addresses the utilization side by using Multi-Instance GPU (MIG) partitioning to split a single GPU into smaller slices, allowing multiple inference services to run in parallel on the same hardware. Kueue addresses the coordination side by providing intelligent resource management, enforcing fair sharing policies and quotas across teams deploying serving workloads.

For AI/ML serving use cases, the primary workloads are long-running services:
- **Inference Deployments**: Scalable model serving endpoints
- **Stateful Inference Services**: Services requiring persistent state or connections
- **Single-Instance Pods**: Specialized inference services

### Current Gap

Kueue treats all MIG profiles as opaque resources and cannot differentiate their GPU memory sizes. As a result, scheduling and quota enforcement are not memory-aware.

---

## Solution Overview

Enable Kueue to understand GPU memory requirements from MIG profiles for serving workloads by automatically exposing GPU memory as a separate resource (`gpu.das.openshift.io/mem`). This integration focuses on long-running workloads (Pods, Deployments, StatefulSets) that are commonly used for AI/ML inference and serving applications.

---

## Implementation Details

### 1. Webhook Resource Management

**Kueue-managed Pods** (have `kueue.x-k8s.io/queue-name` label):
- Inject only: `gpu.das.openshift.io/mem` (for Kueue quotas)
- Store profiles in: `das.openshift.io/mig-profiles` annotation (for DAS scheduler)

### 2. DAS Scheduler Integration

**Profile Detection:**
- **Primary**: Read from `das.openshift.io/mig-profiles` annotation (Kueue-managed Pods)
- **Fallback**: Extract from `mig.das.com/*` resources (non-Kueue Pods)

**AllocationClaim Creation:**
- Creates AllocationClaims from detected profiles
- Reserves actual MIG slices for GPU access

### MIG Profile to GPU Memory Mapping

The following table shows how NVIDIA MIG profiles are mapped to GPU memory resources:

| User Request | DAS Resource | GPU Memory Resource | Memory (GB) |
|--------------|--------------|-------------------|-------------|
| `nvidia.com/mig-1g.5gb: "1"` | `mig.das.com/1g.5gb: "1"` | `gpu.das.openshift.io/mem: "5"` | 5 GB |
| `nvidia.com/mig-2g.10gb: "1"` | `mig.das.com/2g.10gb: "1"` | `gpu.das.openshift.io/mem: "10"` | 10 GB |
| `nvidia.com/mig-3g.20gb: "1"` | `mig.das.com/3g.20gb: "1"` | `gpu.das.openshift.io/mem: "20"` | 20 GB |
| `nvidia.com/mig-4g.20gb: "1"` | `mig.das.com/4g.20gb: "1"` | `gpu.das.openshift.io/mem: "20"` | 20 GB |
| `nvidia.com/mig-7g.40gb: "1"` | `mig.das.com/7g.40gb: "1"` | `gpu.das.openshift.io/mem: "40"` | 40 GB |

---

## Testing Strategy

### Integration Tests
- End-to-end workflow validation
- Kueue integration with GPU memory quotas
- MIG slice allocation verification
---

