#!/usr/bin/env bash

set -e
set -o pipefail

echo "> Creating Kind cluster"
kind create cluster --config - <<EOF
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
nodes:
- role: control-plane
  image: kindest/node:v1.27.3@sha256:3966ac761ae0136263ffdb6cfd4db23ef8a83cba8a463690e98317add2c9ba72
  # required for GPU workaround
  extraMounts:
    - hostPath: /dev/null
      containerPath: /var/run/nvidia-container-devices/all
EOF

echo "> Creating symlink in the control-plane container"
docker exec -ti kind-control-plane ln -s /sbin/ldconfig /sbin/ldconfig.real

echo "> Unmounting the nvidia devices in the control-plane container"
docker exec -ti kind-control-plane umount -R /proc/driver/nvidia

# According to https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html
echo "> Adding/updateding the NVIDIA Helm repository"
helm repo add nvidia https://helm.ngc.nvidia.com/nvidia && helm repo update

echo "> Installing the GPU Operator Helm chart"
helm upgrade --install --wait gpu-operator -n gpu-operator --create-namespace nvidia/gpu-operator \
    --set mig.strategy=mixed \
    --set cdi.enabled=true \
    --set migManager.enabled=false \
    --set migManager.config.default=""

echo "> Waiting for container toolkit daemonset pod(s) to become ready"
kubectl wait pod -n gpu-operator -l app=nvidia-container-toolkit-daemonset --for condition=Ready=true --timeout=360s

echo "> Waiting for device plugin daemonset pod(s) to become ready"
kubectl wait pod -n gpu-operator -l app=nvidia-device-plugin-daemonset --for condition=Ready=true --timeout=360s

echo "> Labeling nodes to use custom device plugin configuration"
kubectl label node --all nvidia.com/device-plugin.config=update-capacity

echo "> Adding custom device plugin configuration"
kubectl apply -f ./deploy/custom-configmapwithprofiles.yaml

echo "> Triggering GPU capacity update"
kubectl patch clusterpolicies.nvidia.com/cluster-policy -n gpu-operator \
    --type merge -p '{"spec": {"devicePlugin": {"config": {"name": "capacity-update-trigger"}}}}'
