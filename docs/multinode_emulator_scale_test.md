# Scale testing with emulated GPUs

It is important to test allocation performance in environment where multiple nodes exists with multiple GPUs on them. InstaSlice provides ability to run scale test on a linux machine that has good amount of RAM and CPUs. Large RAM and CPU requirements makes it easy to launch KinD cluster with upto 20 nodes and submit pods to such a cluster that ask for MIG slices. When you run the tests using below commands it may take upto 10+ minutes to launch and setup KinD cluster based on the resources that you have on your host.

# Steps to run scale tests

- Clone the repo with latest code

- Run the below script form your repo folder

```console
    bash instaslice-operator/test/perf/kind-cluster/mutinode_emulator.sh
```

- After the cluster in up, submit workload to the cluster using command

```console
 bash instaslice-operator/test/perf/load_with_stats.sh 
```
- Please check if all pods are in state Running, for 10 node cluster all pods get in Running state in approx 55 seconds.

- Undeploy the cluster

```console
    make undeploy-emulated
    kind delete clusters --all
```