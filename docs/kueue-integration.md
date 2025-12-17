# DAS + Kueue Integration

This document provides a comprehensive overview of how the Dynamic Accelerator Slicer (DAS) operator integrates with Kueue for GPU quota management and workload scheduling.

## TL;DR

When a user submits a Kueue-managed workload with MIG resources, DAS webhook transforms it:

| BEFORE | AFTER | WHY |
|--------|-------|-----|
| `nvidia.com/mig-1g.10gb: 1` | `gpu.das.openshift.io/mem: 10` | Kueue quota resource (exists before MIG slices) |
| `schedulerName: default` | `schedulerName: das-scheduler` | Knows how to find MIG placements |
| *(none)* | `runtimeClassName: nvidia-legacy` | Avoids CDI conflict with dynamic MIG |
| *(none)* | `das.openshift.io/mig-profiles: "1g.10gb:1"` | Preserves profile for slice creation |

**Flow:** User → DAS Webhook (transform) → Kueue (quota check) → DAS Scheduler (place) → DAS Daemonset (create MIG slice) → Pod runs

---

## Table of Contents
1. [Kueue + DAS Integration](#kueue--das-integration-1)
2. [Current DAS Architecture](#current-das-architecture)
3. [Kueue Integration Flow](#kueue-integration-flow)
4. [Webhook Transformation Details](#webhook-transformation-details)
5. [Test Plan](#test-plan)
6. [Supported Workload Types](#supported-workload-types)
7. [Sequence Diagrams](#sequence-diagrams)
8. [Known Limitations](#known-limitations)
9. [Implementation Status](#implementation-status)
10. [Setup and Configuration](#setup-and-configuration)

---

## Kueue + DAS Integration

### The Problem: Dynamic MIG + Resource Quotas

DAS (Dynamic Accelerator Slicer) enables **dynamic MIG partitioning**, which creates GPU slices on-demand when workloads are scheduled rather than pre-partitioning GPUs. This provides flexibility but creates a challenge for quota management.

Traditional Kubernetes quota management relies on resources being declared on nodes. With static MIG, GPUs are pre-partitioned and nodes report available slices. Kueue or ResourceQuotas can track these directly.

With dynamic MIG (DAS), GPUs are unpartitioned until a workload needs a slice. There's nothing to track.

### What Kueue Provides

Kueue is a Kubernetes-native job queuing system that provides:
- **Quota Management:** Define how much of each resource teams/namespaces can use
- **Fair Scheduling:** Queue workloads and admit them based on available quota
- **Preemption:** Reclaim resources from lower-priority workloads
- **Multi-tenancy:** Isolate teams' resource usage

### Integration Benefits

- **Unified GPU memory quota:** Track GPU resources as memory (GB) rather than individual MIG profiles
- **Dynamic slice creation:** Create MIG slices when pods are scheduled, not before
- **Multi-workload support:** Jobs, PyTorchJob, RayJob, MPIJob, JobSet, and more

---

## Current DAS Architecture

### Why `gpu.das.openshift.io/mem`?

With dynamic MIG, `nvidia.com/mig-*` resources don't exist on nodes until DAS creates them. Kueue can't track resources that don't exist yet.

**Solution:** The DAS webhook injects `gpu.das.openshift.io/mem` (introduced for Kueue integration) as a virtual quota resource that Kueue can track for admission decisions.

| Resource | Purpose |
|----------|---------|
| `nvidia.com/mig-1g.10gb` | Actual MIG slice (exists after creation) |
| `gpu.das.openshift.io/mem` | Kueue quota accounting (exists before scheduling) |

### DAS Component Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         DAS OPERATOR COMPONENTS                         │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────────────┐ │
│  │  DAS Operator   │  │  DAS Webhook    │  │    DAS Scheduler        │ │
│  │  (Controller)   │  │  (Mutating)     │  │    (das-scheduler)      │ │
│  ├─────────────────┤  ├─────────────────┤  ├─────────────────────────┤ │
│  │                 │  │ Transforms:     │  │                         │ │
│  │ Manages CRDs:   │  │ • Pod           │  │ Scheduling:             │ │
│  │ • NodeAccel     │  │ • Job           │  │ • Filter by MIG placement│ │
│  │ • AllocClaim    │  │ • PyTorchJob    │  │ • Score by utilization  │ │
│  │                 │  │ • MPIJob        │  │ • Read mig-profiles     │ │
│  │ Reconciles      │  │ • TFJob         │  │   annotation            │ │
│  │ allocations     │  │ • RayJob        │  │ • Create AllocationClaim│ │
│  │                 │  │ • JobSet        │  │ • Bind pod to node      │ │
│  │                 │  │ • Workload      │  │                         │ │
│  │                 │  │ • etc.          │  │                         │ │
│  └─────────────────┘  └─────────────────┘  └─────────────────────────┘ │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                      DAS Daemonset                               │   │
│  ├─────────────────────────────────────────────────────────────────┤   │
│  │ Per-node agent:                                                  │   │
│  │ • Discovers GPUs via nvidia-smi                                  │   │
│  │ • Creates/updates NodeAccelerator CR                             │   │
│  │ • Watches AllocationClaims                                       │   │
│  │ • Executes MIG slice creation (nvidia-smi mig -cgi/-ci)         │   │
│  │ • Reports gpu.das.openshift.io/mem capacity                      │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

**Implementation notes:**

- **Kueue admission/quota** is enforced using `gpu.das.openshift.io/mem`.
- The **DAS scheduler plugin does not filter directly on** `gpu.das.openshift.io/mem`; it places Pods using:
  - `NodeAccelerator` discovered MIG placement data
  - existing `AllocationClaim` objects (via informer cache)
  - requested profile list (from `das.openshift.io/mig-profiles` or legacy resource keys)
- **MIG slice creation + CDI injection happens in the node device-plugin Allocate() path** (triggered by kubelet for device-plugin-backed resources).

### Webhook Architecture

The DAS webhook handles multiple resource types through a unified transformation:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                       DAS WEBHOOK HANDLERS                              │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  SOURCE RESOURCE              WEBHOOK HANDLER           TRANSFORMATION  │
│  ───────────────              ───────────────           ──────────────  │
│                                                                         │
│  Pod ─────────────────────► webhook.go ──────────────┐                 │
│  Job ─────────────────────► job_webhook.go ──────────┤                 │
│  PyTorchJob ──────────────► kubeflow_webhook.go ─────┤                 │
│  MPIJob ──────────────────► kubeflow_webhook.go ─────┤                 │
│  TFJob ───────────────────► kubeflow_webhook.go ─────┼──► TransformPodTemplateForDAS()
│  XGBoostJob ──────────────► kubeflow_webhook.go ─────┤       │         │
│  PaddleJob ───────────────► kubeflow_webhook.go ─────┤       │         │
│  JAXJob ──────────────────► kubeflow_webhook.go ─────┤       ▼         │
│  RayJob ──────────────────► ray_webhook.go ──────────┤  ┌───────────┐  │
│  RayCluster ──────────────► ray_webhook.go ──────────┤  │ Mutated   │  │
│  JobSet ──────────────────► jobset_webhook.go ───────┤  │ Resource  │  │
│  AppWrapper ──────────────► appwrapper_webhook.go ───┤  └───────────┘  │
│  Deployment ──────────────► longrunning_webhook.go ──┤                 │
│  StatefulSet ─────────────► longrunning_webhook.go ──┤                 │
│  LeaderWorkerSet ─────────► longrunning_webhook.go ──┤                 │
│  Workload (Kueue) ────────► workload_webhook.go ─────┘                 │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Core Transformation: TransformPodTemplateForDAS()

The core transformation function (in `template_mutator.go`) converts MIG resource requests to DAS-compatible format:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                 TransformPodTemplateForDAS()                            │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  INPUT (User's Pod Template):         OUTPUT (Transformed):             │
│  ────────────────────────────         ─────────────────────             │
│                                                                         │
│  containers:                          containers:                       │
│  - resources:                         - resources:                      │
│      limits:                              limits:                       │
│        nvidia.com/mig-1g.10gb: 1  ──►     gpu.das.openshift.io/mem: 10 │
│                                                                         │
│  spec:                                spec:                             │
│    schedulerName: default      ──►      schedulerName: das-scheduler   │
│                                         runtimeClassName: nvidia-legacy│
│                                                                         │
│  metadata:                            metadata:                         │
│    annotations: {}             ──►      annotations:                    │
│                                           das.openshift.io/mig-profiles:│
│                                             "1g.10gb:1"                 │
│                                                                         │
│  WHAT HAPPENS:                                                          │
│  1. Remove nvidia.com/mig-* from container resources                   │
│  2. Calculate total GPU memory from MIG profiles                       │
│  3. Add gpu.das.openshift.io/mem with total memory                     │
│  4. Store original profiles in annotation (for DAS scheduler)          │
│  5. Set schedulerName to das-scheduler                                 │
│  6. Set runtimeClassName to nvidia-legacy                              │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Kueue Integration Flow

### High-Level Flow

```
┌─────────────────────────────────────────────────────────────────────────┐
│                      DAS + KUEUE INTEGRATION FLOW                       │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│   ┌─────────────────┐                                                   │
│   │ User submits    │  Job with:                                        │
│   │ workload        │  • nvidia.com/mig-1g.10gb: 1                      │
│   └────────┬────────┘  • kueue.x-k8s.io/queue-name: my-queue            │
│            │                                                            │
│            ▼                                                            │
│   ┌─────────────────┐                                                   │
│   │ DAS Webhook     │  Transforms to:                                   │
│   │ (source level)  │  • gpu.das.openshift.io/mem: 10                   │
│   └────────┬────────┘  • das.openshift.io/mig-profiles: "1g.10gb:1"     │
│            │                                                            │
│            ▼                                                            │
│   ┌─────────────────┐                                                   │
│   │ Kueue           │  Creates Workload CR                              │
│   │ Controller      │  Checks ClusterQueue quota                        │
│   └────────┬────────┘  ADMITS if quota available                        │
│            │                                                            │
│            ▼                                                            │
│   ┌─────────────────┐                                                   │
│   │ DAS Scheduler   │  1. Filter/Score: MIG placement availability       │
│   │                 │  2. Score: prefer bin-packing                     │
│   └────────┬────────┘  3. Read mig-profiles annotation                  │
│            │           4. Create AllocationClaim                        │
│            ▼                                                            │
│   ┌─────────────────┐                                                   │
│   │ DAS Daemonset   │  1. Device-plugin Allocate() creates MIG slice     │
│   │ (on node)       │  2. Writes CDI spec + returns CDI devices          │
│   └────────┬────────┘  3. AllocationClaim marked InUse (where applicable)│
│            │                                                            │
│            ▼                                                            │
│   ┌─────────────────┐                                                   │
│   │ Pod runs with   │  Has access to dynamically created MIG slice      │
│   │ MIG slice       │                                                   │
│   └─────────────────┘                                                   │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Key Integration Points

1. **Webhook Transformation:** DAS webhook converts MIG requests to `gpu.das.openshift.io/mem` before Kueue sees the workload
2. **Kueue Admission:** Kueue checks quota using the virtual memory resource
3. **Scheduler Awareness:** DAS scheduler reads `das.openshift.io/mig-profiles` annotation to know actual profile needed
4. **On-demand Creation:** MIG slices are created only when pods are scheduled to nodes

### Clarification: what filters on `gpu.das.openshift.io/mem`

- **Kueue**: uses `gpu.das.openshift.io/mem` for admission/quota decisions.
- **DAS scheduler plugin**: does **not** currently filter directly on `gpu.das.openshift.io/mem`; it uses MIG placement availability and `AllocationClaim` state.

### Important: `gpu.das.openshift.io/mem` is for quota (not device access)

`gpu.das.openshift.io/mem` is used for **Kueue quota/admission**. It represents GPU memory capacity for accounting and does not, by itself, grant access to a GPU/MIG device inside the container.

To run with a MIG device, workloads still need to request a **profile-backed** GPU resource (for example `nvidia.com/mig-<profile>` or `mig.das.com/<profile>`), so the node can allocate the actual slice at runtime.

---

## Webhook Transformation Details

This section explains exactly what the DAS webhook does when a user submits a Kueue-managed workload.

### Example: User Submits a Job

```yaml
# User runs: kubectl apply -f job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: my-mig-job
  labels:
    kueue.x-k8s.io/queue-name: my-queue    # <-- Triggers Kueue management
spec:
  template:
    spec:
      containers:
      - name: cuda-app
        image: nvidia/cuda:12.0-base
        resources:
          limits:
            nvidia.com/mig-1g.10gb: 1       # <-- User requests MIG profile
      restartPolicy: Never
```

### What DAS Webhook Transforms

| Field | BEFORE | AFTER | WHY |
|-------|--------|-------|-----|
| Container Resources | `nvidia.com/mig-1g.10gb: 1` | `gpu.das.openshift.io/mem: 10` | Kueue needs a quota resource that exists before MIG slices are created |
| `schedulerName` | `default-scheduler` | `das-scheduler` | DAS scheduler knows how to find nodes with available MIG placements |
| `runtimeClassName` | *(none)* | `nvidia-legacy` | Avoids CDI conflict; DAS injects device access via its own mechanism |
| Annotations | *(none)* | `das.openshift.io/mig-profiles: "1g.10gb:1"` | Preserves original MIG profile so DAS scheduler can create the correct slice |

---

## Test Plan

This section outlines what needs to be verified for DAS + Kueue integration.

### Unit/Integration Tests

| Test Area | What to Verify |
|-----------|----------------|
| **Webhook Transformation** | MIG resource removed, `gpu.das.openshift.io/mem` added with correct value |
| **Scheduler/Runtime Set** | `schedulerName: das-scheduler` and `runtimeClassName: nvidia-legacy` set |
| **Annotation Added** | `das.openshift.io/mig-profiles` contains original profile info |
| **Non-Kueue Jobs Skipped** | Jobs without `kueue.x-k8s.io/queue-name` label are not transformed |

### End-to-End Tests

| Test Scenario | Expected Behavior |
|---------------|-------------------|
| **Job admitted within quota** | Job unsuspended, pod scheduled, MIG slice created, pod runs successfully |
| **Job exceeds quota** | Job stays suspended, Workload shows "Pending" with quota exceeded message |
| **Multiple workload types** | PyTorchJob, RayJob, JobSet all transform correctly and run with MIG slices |
| **Quota accounting** | Total `gpu.das.openshift.io/mem` across admitted workloads ≤ ClusterQueue nominalQuota |

### Manual Verification Steps

1. **Apply a Kueue-managed Job:**
   ```bash
   kubectl apply -f samples/kueue/01-mig-job.yaml
   ```

2. **Verify webhook transformation:**
   ```bash
   kubectl get job <job-name> -o yaml | grep -A5 "resources:"
   # Should show gpu.das.openshift.io/mem, NOT nvidia.com/mig-*

   kubectl get job <job-name> -o yaml | grep schedulerName
   # Should show: das-scheduler

   kubectl get job <job-name> -o yaml | grep runtimeClassName
   # Should show: nvidia-legacy
   ```

3. **Verify Kueue Workload created:**
   ```bash
   kubectl get workloads -n <namespace>
   # Should show Workload with Admitted=True (if quota available)
   ```

4. **Verify MIG slice created:**
   ```bash
   kubectl get allocationclaims -A
   # Should show claim for the pod

   nvidia-smi  # on the node
   # Should show the MIG instance
   ```

---

## Supported Workload Types

### Currently Implemented

| Workload Type | Webhook File | Notes |
|---------------|--------------|-------|
| Pod | `webhook.go` | Direct pod submissions |
| Job | `job_webhook.go` | Kueue-managed Jobs only |
| PyTorchJob | `kubeflow_webhook.go` | Kubeflow Training Operator |
| MPIJob | `kubeflow_webhook.go` | Kubeflow Training Operator |
| TFJob | `kubeflow_webhook.go` | Kubeflow Training Operator |
| XGBoostJob | `kubeflow_webhook.go` | Kubeflow Training Operator |
| PaddleJob | `kubeflow_webhook.go` | Kubeflow Training Operator |
| JAXJob | `kubeflow_webhook.go` | Kubeflow Training Operator |
| RayJob | `ray_webhook.go` | KubeRay Operator |
| RayCluster | `ray_webhook.go` | KubeRay Operator |
| JobSet | `jobset_webhook.go` | JobSet Controller |
| AppWrapper | `appwrapper_webhook.go` | CodeFlare |
| Deployment | `longrunning_webhook.go` | Long-running services |
| StatefulSet | `longrunning_webhook.go` | Stateful services |
| LeaderWorkerSet | `longrunning_webhook.go` | Leader-worker pattern workloads |
| Workload | `workload_webhook.go` | Kueue Workload CR (safety net) |

---

## Sequence Diagrams

### Simple Job Flow

```
┌──────┐     ┌──────────┐     ┌───────┐     ┌─────────────┐     ┌──────────┐
│ User │     │DAS       │     │ Kueue │     │DAS          │     │DAS       │
│      │     │Webhook   │     │       │     │Scheduler    │     │Daemonset │
└──┬───┘     └────┬─────┘     └───┬───┘     └──────┬──────┘     └────┬─────┘
   │              │               │                │                 │
   │ Create Job   │               │                │                 │
   │ nvidia.com/  │               │                │                 │
   │ mig-1g.10gb  │               │                │                 │
   │──────────────>               │                │                 │
   │              │               │                │                 │
   │              │ Transform:    │                │                 │
   │              │ gpu.das.mem   │                │                 │
   │              │ + annotation  │                │                 │
   │              │───────────────>                │                 │
   │              │               │                │                 │
   │              │               │ Create Workload│                 │
   │              │               │ Check quota    │                 │
   │              │               │ ADMIT          │                 │
   │              │               │────────────────>                 │
   │              │               │                │                 │
   │              │               │                │ Read annotation │
   │              │               │                │ Filter nodes    │
   │              │               │                │ Create AllocClaim
   │              │               │                │─────────────────>
   │              │               │                │                 │
   │              │               │                │                 │ nvidia-smi
   │              │               │                │                 │ mig -cgi
   │              │               │                │                 │ 1g.10gb
   │              │               │                │                 │
   │              │               │                │ Claim Ready     │
   │              │               │                │<─────────────────
   │              │               │                │                 │
   │              │               │                │ Bind Pod        │
   │              │               │<────────────────                 │
   │              │               │                │                 │
   │ Pod Running  │               │                │                 │
   │<──────────────────────────────                │                 │
   │              │               │                │                 │
```

### PyTorchJob with Multiple Workers

```
┌──────┐     ┌──────────┐     ┌───────┐     ┌───────────┐     ┌──────────┐
│ User │     │DAS       │     │ Kueue │     │DAS        │     │DAS       │
│      │     │Kubeflow  │     │       │     │Scheduler  │     │Daemonset │
│      │     │Webhook   │     │       │     │           │     │          │
└──┬───┘     └────┬─────┘     └───┬───┘     └─────┬─────┘     └────┬─────┘
   │              │               │               │                │
   │ PyTorchJob   │               │               │                │
   │ Master: 1x   │               │               │                │
   │   2g.20gb    │               │               │                │
   │ Worker: 2x   │               │               │                │
   │   2g.20gb    │               │               │                │
   │──────────────>               │               │                │
   │              │               │               │                │
   │              │ Transform     │               │                │
   │              │ ALL replicas: │               │                │
   │              │ Master:       │               │                │
   │              │  gpu.mem: 20  │               │                │
   │              │ Worker:       │               │                │
   │              │  gpu.mem: 20  │               │                │
   │              │───────────────>               │                │
   │              │               │               │                │
   │              │               │ Workload:     │                │
   │              │               │ Total: 60GB   │                │
   │              │               │ (20+20+20)    │                │
   │              │               │               │                │
   │              │               │ Quota: 80GB   │                │
   │              │               │ 60 <= 80 ✓    │                │
   │              │               │ ADMIT         │                │
   │              │               │───────────────>                │
   │              │               │               │                │
   │              │               │               │ Schedule       │
   │              │               │               │ 3 pods         │
   │              │               │               │ Create 3       │
   │              │               │               │ AllocationClaims
   │              │               │               │────────────────>
   │              │               │               │                │
   │              │               │               │                │ Create 3x
   │              │               │               │                │ 2g.20gb
   │              │               │               │                │ slices
   │              │               │               │<────────────────
   │              │               │               │                │
   │ PyTorchJob   │               │               │                │
   │ Running      │               │               │                │
   │<──────────────────────────────               │                │
   │              │               │               │                │
```

---

## Known Limitations

### 1. MIG + NCCL Limitation

**CRITICAL: NCCL does not work with MIG instances on the same physical GPU**

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     MIG + NCCL LIMITATION                               │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  NCCL (NVIDIA Collective Communications Library) is used for           │
│  GPU-to-GPU communication in distributed training.                     │
│                                                                         │
│  PROBLEM: NCCL does NOT support multiple ranks per GPU.                │
│           MIG instances on the SAME physical GPU cannot use NCCL.      │
│                                                                         │
│  ┌────────────────────────────────────────────────────────────────┐    │
│  │  GPU 0 (80GB A100)                                             │    │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐                       │    │
│  │  │ Worker-0 │ │ Worker-1 │ │ Worker-2 │                       │    │
│  │  │ 2g.20gb  │ │ 2g.20gb  │ │ 2g.20gb  │                       │    │
│  │  └──────────┘ └──────────┘ └──────────┘                       │    │
│  │       ▲              ▲            ▲                            │    │
│  │       └──────────────┴────────────┘                            │    │
│  │              NCCL FAILS ✗                                      │    │
│  │        (same physical GPU)                                     │    │
│  └────────────────────────────────────────────────────────────────┘    │
│                                                                         │
│  ┌────────────────────┐  ┌────────────────────┐                        │
│  │  GPU 0             │  │  GPU 1             │                        │
│  │  ┌──────────┐      │  │  ┌──────────┐      │                        │
│  │  │ Worker-0 │      │  │  │ Worker-1 │      │                        │
│  │  │ 2g.20gb  │      │  │  │ 2g.20gb  │      │                        │
│  │  └──────────┘      │  │  └──────────┘      │                        │
│  └────────────────────┘  └────────────────────┘                        │
│           ▲                       ▲                                    │
│           └───────────────────────┘                                    │
│                  NCCL WORKS ✓                                          │
│            (different physical GPUs)                                   │
│                                                                         │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  CURRENT WORKAROUNDS:                                                   │
│                                                                         │
│  1. Use gloo backend instead of NCCL for PyTorch DDP                   │
│     - Works with MIG on same GPU                                       │
│     - Lower performance than NCCL                                      │
│                                                                         │
│  2. Use full GPUs (non-MIG) for NCCL workloads                         │
│     - Request nvidia.com/gpu instead of nvidia.com/mig-*               │
│                                                                         │
│  FUTURE: DAS scheduler could implement GPU-level anti-affinity         │
│          to place NCCL workers on different physical GPUs              │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

**Reference:** [NVIDIA NCCL Issue #431](https://github.com/NVIDIA/nccl/issues/431)

### 2. MIG Profile Constraints

- Not all MIG profiles are available on all GPUs
- A100 40GB vs A100 80GB have different profiles
- Some profile combinations are mutually exclusive

### 3. Workload Requirements

- Workloads must have `kueue.x-k8s.io/queue-name` label to be Kueue-managed
- Non-Kueue workloads follow the original DAS flow (resource renaming)

---

## Implementation Status

### Supported Integrations

DAS webhooks support the following Kueue-managed workload types:

- **Kubernetes native:** Pod, Job, Deployment, StatefulSet, LeaderWorkerSet
- **Kubeflow Training Operator:** PyTorchJob, MPIJob, TFJob, XGBoostJob, PaddleJob, JAXJob
- **KubeRay:** RayJob, RayCluster
- **Other:** JobSet, AppWrapper (CodeFlare), Kueue Workload

### Not Covered

- **NCCL Anti-Affinity:** Requires scheduler changes for GPU-level placement

---

## Setup and Configuration

### Prerequisites

1. **Kueue installed:** `kubectl get pods -n kueue-system`
2. **DAS operator installed:** All four components running
3. **MIG-capable GPUs:** A100, H100, or A30
4. **NVIDIA GPU Operator configured for DAS:** See [nvidia-gpu-openshift.md](nvidia-gpu-openshift.md)
5. **Namespaces labeled for Kueue integration:** DAS webhooks for Kueue-managed workloads (Job, PyTorchJob, etc.) only apply to namespaces with the label `kueue.openshift.io/managed=true`:
   ```bash
   kubectl label namespace <namespace> kueue.openshift.io/managed=true
   ```

**Key GPU Operator settings for DAS:**

```yaml
# In ClusterPolicy
devicePlugin:
  enabled: false           # DAS replaces NVIDIA's device plugin

mig:
  strategy: mixed          # Required for MIG

migManager:
  enabled: true
  env:
    - name: MIG_PARTED_MODE_CHANGE_ONLY
      value: 'true'        # GPU Operator only enables MIG mode, DAS creates partitions
```

This configuration ensures:
- MIG mode is enabled on GPUs (prerequisite for any MIG partitioning)
- GPU Operator does NOT manage MIG partitions (DAS does this dynamically)
- DAS device plugin handles resource allocation

### Kueue Configuration

Apply the sample setup:

```bash
kubectl apply -f samples/kueue/00-kueue-setup.yaml
```

This creates:

```yaml
# ResourceFlavor: represents GPU resources
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: das-gpu-flavor
spec:
  nodeLabels: {}

---
# ClusterQueue: defines quota
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: das-cluster-queue
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources:
    - gpu.das.openshift.io/mem  # DAS GPU memory resource
    - cpu
    - memory
    flavors:
    - name: das-gpu-flavor
      resources:
      - name: gpu.das.openshift.io/mem
        nominalQuota: "80"  # 80GB total GPU memory
      - name: cpu
        nominalQuota: "100"
      - name: memory
        nominalQuota: "200Gi"

---
# LocalQueue: namespace-scoped queue
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  name: das-local-queue
  namespace: default
spec:
  clusterQueue: das-cluster-queue
```

### Example Workload

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: mig-job
  labels:
    kueue.x-k8s.io/queue-name: das-local-queue  # Required for Kueue
spec:
  template:
    spec:
      containers:
      - name: cuda
        image: nvidia/cuda:12.0-base
        command: ["nvidia-smi", "-L"]
        resources:
          limits:
            nvidia.com/mig-1g.10gb: 1  # DAS transforms this
      restartPolicy: Never
```

---
