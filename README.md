# Note - Kubecon EU 2024 code (DRA code) is now available in the legacy branch

# InstaSlice

InstaSlice works with GPU operator to create mig slices on demand.

# Why InstaSlice

Partiionable accelarators provided by vendors need partition to be created at node boot-time or to change partitions one would have to evict all the workloads at the node level to create new set of partitions. 

InstaSlice will help if

 - user does not know all the accelarator partitions needed apriori on every node on the cluster
 - user partition requirements change at the workload level rather than the node level
 - user does not want to learn or use new API to request accelarators slices
 - user prefers to use stable device plugins APIs for creating partitions

# Demo


[InstaSlice demo](samples/demo_script/demo_video/instaslice.mp4)

## Getting Started

### Prerequisites
- [Go](https://go.dev/doc/install) v1.22.0+
- [Docker](https://docs.docker.com/get-docker/) v17.03+
- [Docker buildx plugin](https://github.com/docker/buildx) for building cross-platform images.
- [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl) v1.11.3+.
- Access to a [KinD](https://kind.sigs.k8s.io/docs/user/quick-start/) cluster.

### Install KinD cluster with GPU operator

- Make sure the GPUs on the host have MIG enabled

```sh
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 550.54.14              Driver Version: 550.54.14      CUDA Version: 12.4     |
|-----------------------------------------+------------------------+----------------------+
| GPU  Name                 Persistence-M | Bus-Id          Disp.A | Volatile Uncorr. ECC |
| Fan  Temp   Perf          Pwr:Usage/Cap |           Memory-Usage | GPU-Util  Compute M. |
|                                         |                        |               MIG M. |
|=========================================+========================+======================|
|   0  NVIDIA A100-PCIE-40GB          Off |   00000000:0E:00.0 Off |                   On |
| N/A   36C    P0             33W /  250W |       0MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+
|   1  NVIDIA A100-PCIE-40GB          Off |   00000000:0F:00.0 Off |                   On |
| N/A   40C    P0             32W /  250W |       0MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+

+-----------------------------------------------------------------------------------------+
| MIG devices:                                                                            |
+------------------+----------------------------------+-----------+-----------------------+
| GPU  GI  CI  MIG |                     Memory-Usage |        Vol|      Shared           |
|      ID  ID  Dev |                       BAR1-Usage | SM     Unc| CE ENC DEC OFA JPG    |
|                  |                                  |        ECC|                       |
|==================+==================================+===========+=======================|
|  No MIG devices found                                                                   |
+-----------------------------------------------------------------------------------------+

+-----------------------------------------------------------------------------------------+
| Processes:                                                                              |
|  GPU   GI   CI        PID   Type   Process name                              GPU Memory |
|        ID   ID                                                               Usage      |
|=========================================================================================|
|  No running processes found                                                             |
```

- Run the below script
```sh
sh ./deploy/setup.sh
```
NOTE: Please check if all the pods in GPU operator are completed or Running before moving to the next step.

```sh
(base) openstack@netsres62:~/asmalvan/instaslice2$ kubectl get pods -n gpu-operator
NAME                                                              READY   STATUS      RESTARTS   AGE
gpu-feature-discovery-578q8                                       1/1     Running     0          102s
gpu-operator-1714053627-node-feature-discovery-gc-9b857c99phlnn   1/1     Running     0          7m21s
gpu-operator-1714053627-node-feature-discovery-master-6df78zgsz   1/1     Running     0          7m21s
gpu-operator-1714053627-node-feature-discovery-worker-47tpx       1/1     Running     0          7m19s
gpu-operator-54b8bfbfd8-rmzbd                                     1/1     Running     0          7m21s
nvidia-container-toolkit-daemonset-wkc5h                          1/1     Running     0          6m21s
nvidia-cuda-validator-cn8lg                                       0/1     Completed   0          88s
nvidia-dcgm-exporter-h75xg                                        1/1     Running     0          102s
nvidia-device-plugin-daemonset-452dk                              1/1     Running     0          101s
nvidia-mig-manager-htt7z                                          1/1     Running     0          2m21s
nvidia-operator-validator-kh6jf                                   1/1     Running     0          102s
```

- After all the pods are Running/Completed, run nvidia-smi on the host and check if MIG slices appear on the all the GPUs of the host.

```sh
(base) openstack@netsres62:~/asmalvan/instaslice2$ nvidia-smi
Thu Apr 25 10:08:24 2024
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 550.54.14              Driver Version: 550.54.14      CUDA Version: 12.4     |
|-----------------------------------------+------------------------+----------------------+
| GPU  Name                 Persistence-M | Bus-Id          Disp.A | Volatile Uncorr. ECC |
| Fan  Temp   Perf          Pwr:Usage/Cap |           Memory-Usage | GPU-Util  Compute M. |
|                                         |                        |               MIG M. |
|=========================================+========================+======================|
|   0  NVIDIA A100-PCIE-40GB          Off |   00000000:0E:00.0 Off |                   On |
| N/A   45C    P0             71W /  250W |      87MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+
|   1  NVIDIA A100-PCIE-40GB          Off |   00000000:0F:00.0 Off |                   On |
| N/A   49C    P0             69W /  250W |      87MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+

+-----------------------------------------------------------------------------------------+
| MIG devices:                                                                            |
+------------------+----------------------------------+-----------+-----------------------+
| GPU  GI  CI  MIG |                     Memory-Usage |        Vol|      Shared           |
|      ID  ID  Dev |                       BAR1-Usage | SM     Unc| CE ENC DEC OFA JPG    |
|                  |                                  |        ECC|                       |
|==================+==================================+===========+=======================|
|  0    2   0   0  |              37MiB / 19968MiB    | 42      0 |  3   0    2    0    0 |
|                  |                 0MiB / 32767MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  0    3   0   1  |              25MiB /  9856MiB    | 28      0 |  2   0    1    0    0 |
|                  |                 0MiB / 16383MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  0    9   0   2  |              12MiB /  4864MiB    | 14      0 |  1   0    0    0    0 |
|                  |                 0MiB /  8191MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  0   10   0   3  |              12MiB /  4864MiB    | 14      0 |  1   0    0    0    0 |
|                  |                 0MiB /  8191MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  1    2   0   0  |              37MiB / 19968MiB    | 42      0 |  3   0    2    0    0 |
|                  |                 0MiB / 32767MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  1    3   0   1  |              25MiB /  9856MiB    | 28      0 |  2   0    1    0    0 |
|                  |                 0MiB / 16383MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  1    9   0   2  |              12MiB /  4864MiB    | 14      0 |  1   0    0    0    0 |
|                  |                 0MiB /  8191MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  1   10   0   3  |              12MiB /  4864MiB    | 14      0 |  1   0    0    0    0 |
|                  |                 0MiB /  8191MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+

+-----------------------------------------------------------------------------------------+
| Processes:                                                                              |
|  GPU   GI   CI        PID   Type   Process name                              GPU Memory |
|        ID   ID                                                               Usage      |
|=========================================================================================|
|  No running processes found                                                             |
+-----------------------------------------------------------------------------------------+
(base) openstack@netsres62:~/asmalvan/instaslice2$
```


- Delete mig slices using the commmand

```sh
sudo nvidia-smi mig -dci && sudo nvidia-smi mig -dgi

uccessfully destroyed compute instance ID  0 from GPU  0 GPU instance ID  9
Successfully destroyed compute instance ID  0 from GPU  0 GPU instance ID 10
Successfully destroyed compute instance ID  0 from GPU  0 GPU instance ID  3
Successfully destroyed compute instance ID  0 from GPU  0 GPU instance ID  2
Successfully destroyed compute instance ID  0 from GPU  1 GPU instance ID  9
Successfully destroyed compute instance ID  0 from GPU  1 GPU instance ID 10
Successfully destroyed compute instance ID  0 from GPU  1 GPU instance ID  3
Successfully destroyed compute instance ID  0 from GPU  1 GPU instance ID  2
Successfully destroyed GPU instance ID  9 from GPU  0
Successfully destroyed GPU instance ID 10 from GPU  0
Successfully destroyed GPU instance ID  3 from GPU  0
Successfully destroyed GPU instance ID  2 from GPU  0
Successfully destroyed GPU instance ID  9 from GPU  1
Successfully destroyed GPU instance ID 10 from GPU  1
Successfully destroyed GPU instance ID  3 from GPU  1
Successfully destroyed GPU instance ID  2 from GPU  1
```

- Create placeholder slice to make k8s-device-plugin happy using the command

```sh
sudo nvidia-smi mig -cgi 3g.20gb -C
Successfully created GPU instance ID  2 on GPU  0 using profile MIG 3g.20gb (ID  9)
Successfully created compute instance ID  0 on GPU  0 GPU instance ID  2 using profile MIG 3g.20gb (ID  2)
Successfully created GPU instance ID  2 on GPU  1 using profile MIG 3g.20gb (ID  9)
Successfully created compute instance ID  0 on GPU  1 GPU instance ID  2 using profile MIG 3g.20gb (ID  2)
```

- Run the below command to patch device plugin with configmap created by the setup script. For OpenShift replace clusterpolicies.nvidia.com/cluster-policy to clusterpolicies.nvidia.com/gpu-cluster-policy and namespace to nvidia-gpu-operator

```sh
(base) openstack@netsres62:~/asmalvan/instaslice2$ kubectl patch clusterpolicies.nvidia.com/cluster-policy     -n gpu-operator --type merge     -p '{"spec": {"devicePlugin": {"config": {"name": "test"}}}}'
```

You are now all set to dynamically create slices on the cluster using InstaSlice.

### Running the controller

- Refer to section `To Deploy on the cluster`

### Submitting the workload

- Submit a sample workload using the command

```sh
kubectl apply -f ./samples/test-pod.yaml
pod/cuda-vectoradd-5 created
```

- check the status of the workload using commands

```sh
kubectl get pods
NAME               READY   STATUS    RESTARTS   AGE
cuda-vectoradd-5   1/1     Running   0          15s
kubectl logs cuda-vectoradd-5
GPU 0: NVIDIA A100-PCIE-40GB (UUID: GPU-31cfe05c-ed13-cd17-d7aa-c63db5108c24)
  MIG 1g.5gb      Device  0: (UUID: MIG-c5720b34-e550-5278-90e6-d99a979aafd1)
[Vector addition of 50000 elements]
Copy input data from the host memory to the CUDA device
CUDA kernel launch with 196 blocks of 256 threads
Copy output data from the CUDA device to the host memory
Test PASSED
Done

+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 550.54.14              Driver Version: 550.54.14      CUDA Version: 12.4     |
|-----------------------------------------+------------------------+----------------------+
| GPU  Name                 Persistence-M | Bus-Id          Disp.A | Volatile Uncorr. ECC |
| Fan  Temp   Perf          Pwr:Usage/Cap |           Memory-Usage | GPU-Util  Compute M. |
|                                         |                        |               MIG M. |
|=========================================+========================+======================|
|   0  NVIDIA A100-PCIE-40GB          Off |   00000000:0E:00.0 Off |                   On |
| N/A   52C    P0             75W /  250W |      50MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+
|   1  NVIDIA A100-PCIE-40GB          Off |   00000000:0F:00.0 Off |                   On |
| N/A   60C    P0             75W /  250W |      37MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+

+-----------------------------------------------------------------------------------------+
| MIG devices:                                                                            |
+------------------+----------------------------------+-----------+-----------------------+
| GPU  GI  CI  MIG |                     Memory-Usage |        Vol|      Shared           |
|      ID  ID  Dev |                       BAR1-Usage | SM     Unc| CE ENC DEC OFA JPG    |
|                  |                                  |        ECC|                       |
|==================+==================================+===========+=======================|
|  0    2   0   0  |              37MiB / 19968MiB    | 42      0 |  3   0    2    0    0 |
|                  |                 0MiB / 32767MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  0   10   0   1  |              12MiB /  4864MiB    | 14      0 |  1   0    0    0    0 |
|                  |                 0MiB /  8191MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  1    2   0   0  |              37MiB / 19968MiB    | 42      0 |  3   0    2    0    0 |
|                  |                 0MiB / 32767MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+

+-----------------------------------------------------------------------------------------+
| Processes:                                                                              |
|  GPU   GI   CI        PID   Type   Process name                              GPU Memory |
|        ID   ID                                                               Usage      |
|=========================================================================================|
|  No running processes found                                                             |
+-----------------------------------------------------------------------------------------+

```
### Deleting the workload

- Delete the pod and see the newly created MIG slice deleted

```sh
kubectl delete pod cuda-vectoradd-5

+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 550.54.14              Driver Version: 550.54.14      CUDA Version: 12.4     |
|-----------------------------------------+------------------------+----------------------+
| GPU  Name                 Persistence-M | Bus-Id          Disp.A | Volatile Uncorr. ECC |
| Fan  Temp   Perf          Pwr:Usage/Cap |           Memory-Usage | GPU-Util  Compute M. |
|                                         |                        |               MIG M. |
|=========================================+========================+======================|
|   0  NVIDIA A100-PCIE-40GB          Off |   00000000:0E:00.0 Off |                   On |
| N/A   53C    P0             75W /  250W |      37MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+
|   1  NVIDIA A100-PCIE-40GB          Off |   00000000:0F:00.0 Off |                   On |
| N/A   60C    P0             75W /  250W |      37MiB /  40960MiB |     N/A      Default |
|                                         |                        |              Enabled |
+-----------------------------------------+------------------------+----------------------+

+-----------------------------------------------------------------------------------------+
| MIG devices:                                                                            |
+------------------+----------------------------------+-----------+-----------------------+
| GPU  GI  CI  MIG |                     Memory-Usage |        Vol|      Shared           |
|      ID  ID  Dev |                       BAR1-Usage | SM     Unc| CE ENC DEC OFA JPG    |
|                  |                                  |        ECC|                       |
|==================+==================================+===========+=======================|
|  0    2   0   0  |              37MiB / 19968MiB    | 42      0 |  3   0    2    0    0 |
|                  |                 0MiB / 32767MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+
|  1    2   0   0  |              37MiB / 19968MiB    | 42      0 |  3   0    2    0    0 |
|                  |                 0MiB / 32767MiB  |           |                       |
+------------------+----------------------------------+-----------+-----------------------+

+-----------------------------------------------------------------------------------------+
| Processes:                                                                              |
|  GPU   GI   CI        PID   Type   Process name                              GPU Memory |
|        ID   ID                                                               Usage      |
|=========================================================================================|
|  No running processes found                                                             |
+-----------------------------------------------------------------------------------------+

```

### To Deploy on the cluster

**All in one command**

make docker-build && make docker-push && make deploy

Cross-platform or multi-arch images can be built and pushed using
`make docker-buildx`. When using Docker as your container tool, make
sure to create a builder instance. Refer to
[Multi-platform images](https://docs.docker.com/build/building/multi-platform/)
for documentation on building mutli-platform images with Docker.

You can change the destination platform(s) by
setting `PLATFORMS`, e.g.

```sh
PLATFORMS=linux/arm64,linux/amd64 make docker-buildx
```

**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/instaslice:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/instaslice:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/instaslice:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/instaslice/<tag or branch>/dist/install.yaml
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

