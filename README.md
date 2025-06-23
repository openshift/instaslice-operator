# Dynamic Accelerator Slicer (DAS) Operator

Dynamic Accelerator Slicer (DAS) is an operator that dynamically partitions GPU accelerators in Kubernetes and OpenShift. It currently ships with a reference implementation for NVIDIA Multi-Instance GPU (MIG) and is designed to support additional technologies such as NVIDIA MPS or GPUs from other vendors.

## Table of Contents
- [Features](#features)
- [Getting Started](#getting-started)
  - [Emulated Mode](#emulated-mode)
  - [Running on OpenShift](#running-on-openshift)
- [Architecture](#architecture)
  - [MIG scheduler plugin](#mig-scheduler-plugin)
  - [AllocationClaim resource](#allocationclaim-resource)
- [Debugging](#debugging)
- [Running E2E tests](#running-e2e-tests)
- [Uninstalling](#uninstalling)
- [Contributing](#contributing)
- [License](#license)

## Features

- On-demand partitioning of GPUs via a custom Kubernetes operator.
- Scheduler integration that allocates NVIDIA MIG slices through a plugin located at [`pkg/scheduler/plugins/mig/mig.go`](pkg/scheduler/plugins/mig/mig.go).
- `AllocationClaim` custom resource to track slice reservations (`pkg/apis/dasoperator/v1alpha1/allocation_types.go`).
- Emulated mode to exercise the workflow without real hardware.

## Getting Started

### Kubernetes

The operator can be deployed on any Kubernetes cluster. To build and push the images run:

```bash
IMAGE_REGISTRY=<image_registry> IMAGE_TAG=<tag> make emulated-k8s   # no GPUs
```
e.g.
```
IMAGE_REGISTRY=quay.io/harpatil IMAGE_TAG=dev  make emulated-k8s
```

Apply one of the sample pods to verify the installation. For example, in emulated mode:

```bash
kubectl apply -f test/test-pod-emulated.yaml
```

### Running on OpenShift

Prerequisites are documented in [docs/nvidia-gpu-openshift.md](docs/nvidia-gpu-openshift.md). Once the prerequisites are met, you can deploy the operator with:

```bash
IMAGE_REGISTRY=<image_registry> IMAGE_TAG=<tag> make emulated-ocp   # no GPUs
IMAGE_REGISTRY=<image_registry> IMAGE_TAG=<tag> make gpu-ocp        # with GPUs
```
Please note that when deployed using `emulated-ocp`, the cert manager operator needs to be installed in the Openshift cluster using the OperatorHub before running the command.
e.g.
```
IMAGE_REGISTRY=quay.io/harpatil IMAGE_TAG=dev make gpu-ocp
```

If GPUs are present, test with:

```bash
kubectl apply -f test/test-pod.yaml
```

## Architecture

The diagram below summarizes how the operator components interact. Pods requesting GPU slices are mutated by a webhook to use the `mig.das.com` extended resource. The scheduler plugin tracks slice availability and creates `AllocationClaim` objects processed by the device plugin on each node.

![DAS Architecture](docs/images/arch.png)

### MIG scheduler plugin

The plugin integrates with the Kubernetes scheduler and runs through three framework phases:

* **Filter** – ensures the node is MIG capable and stages `AllocationClaim`s for suitable GPUs.
* **Score** – prefers nodes with the most free MIG slice slots after considering existing and staged claims.
* **PreBind** – promotes staged claims on the selected node to `created` and removes the rest.

Once promoted, the device plugin provisions the slices.

### AllocationClaim resource

`AllocationClaim` is a namespaced CRD that records which MIG slice will be prepared for a pod. Claims start in the `staged` state and transition to `created` once all requests are satisfied. Each claim stores the GPU UUID, slice position and pod reference.

Example:

```console
$ kubectl get allocationclaims -n das-operator
NAME                                          AGE
8835132e-8a7a-4766-a78f-0cb853d165a2-busy-0   61s
```

```console
$ kubectl get allocationclaims -n das-operator -o yaml
apiVersion: inference.redhat.com/v1alpha1
kind: AllocationClaim
...
```

## Emulated mode

When `emulatedMode` is enabled in the `DASOperator` custom resource, the operator publishes synthetic GPU capacity and skips NVML calls. This is handy for development and CI environments with no hardware.

## Debugging

All components run in the `das-operator` namespace:

```console
kubectl get pods -n das-operator
```

Inspect the active claims:

```console
kubectl get allocationclaims -n das-operator
```

On the node, verify that the CDI devices were created:

```console
ls -l /var/run/cdi/
```

Increase verbosity by editing the `DASOperator` resource and setting `operatorLogLevel` to `Debug` or `Trace`.

## Running E2E tests

A running cluster with a valid `KUBECONFIG` is required:

```console
make test-e2e
```

You can focus on specific tests:

```console
make test-e2e FOCUS="GPU slices"
```

## Uninstalling

Remove the deployed resources with:

```bash
make cleanup-k8s
```

## Contributing

Contributions are welcome! Please open issues or pull requests.

## License

This project is licensed under the Apache 2.0 License.
