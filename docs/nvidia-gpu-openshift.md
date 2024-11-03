# Setting up the NVIDIA GPU Operator for InstaSlice

The InstaSlice operator requires NVIDIA GPU drivers to be installed on OpenShift nodes with NVIDIA GPUs.
It also requires Multi-Instance GPU (MIG) to be enabled on a node's GPUs *without* any MIG partitions defined.
The recommended way to accomplish both on OpenShift is via the NVIDIA GPU Operator.
The operator will install the drivers, and its MIG manager will gracefully take care of everything that is needed to set the correct MIG mode.

1. Install the [NVIDIA GPU Operator for OpenShift](https://docs.nvidia.com/datacenter/cloud-native/openshift/latest/index.html) (not for Kubernetes).

2. Create a cluster policy with the following changes:

```yaml
  devicePlugin:
    enabled: false
```

```yaml
  migManager:
    config:
      default: ""
      name: default-mig-parted-config
    enabled: true
    env:
      - name: WITH_REBOOT
        value: 'true'
```

```yaml
  mig:
    strategy: mixed
```

3. Wait for the NVIDIA GPU Operator pods to run successfully.

:warning: **Warning:** Validator pods may crash (`Init:Error` or `Init:CrashLoopBackOff`) or remain uninitialized (`Init:2/3`).
This is an expected behavior when MIG is enabled, but no partitions are defined.

```console
# oc get pod -n nvidia-gpu-operator
NAME                                                  READY   STATUS      RESTARTS   AGE
gpu-feature-discovery-7pz2r                           1/1     Running     0          6m47s
gpu-operator-9588668b5-l5vbr                          1/1     Running     0          9m50s
nvidia-container-toolkit-daemonset-tzdkb              1/1     Running     0          6m48s
nvidia-cuda-validator-wvx6x                           0/1     Completed   0          3m10s
nvidia-dcgm-8mzps                                     1/1     Running     0          6m48s
nvidia-dcgm-exporter-z5lj9                            1/1     Running     0          6m48s
nvidia-driver-daemonset-417.94.202409121747-0-xvdpr   2/2     Running     0          7m32s
nvidia-mig-manager-ww2cf                              1/1     Running     0          2m22s
nvidia-node-status-exporter-w28lj                     1/1     Running     0          7m25s
nvidia-operator-validator-bv4zc                       1/1     Running     0          6m48s
```

4. Apply *all-enabled* profile to enable MIG on the GPU nodes:

```
oc label node $node nvidia.com/mig.config=all-enabled --overwrite
```

5. Verify that MIG has been enabled on the labeled nodes. You can use the following command to query MIG mode of a node:

```console
oc exec -ti $(oc get pod -n nvidia-gpu-operator -l app.kubernetes.io/component=nvidia-driver --field-selector spec.nodeName=$node -o name) -n nvidia-gpu-operator -- nvidia-smi --query-gpu mig.mode.current,mig.mode.pending --format=csv,noheader
```

The expected output is:

```console
Enabled, Enabled
```

:warning: **Warning:** If running in a VM, on some platforms the hypervisor may prevent MIG configuration changes.
The MIG manager will try to reboot the VM to overcome that, but if it does not succeed, you may need to reboot manually.