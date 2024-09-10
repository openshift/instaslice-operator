#!/bin/bash

# Set the number of worker nodes here
num_workers=10
namespace_instaslice="instaslice-operator-system"

# Create the kind configuration file
cat <<EOF > kind-config.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
EOF

# Add the worker nodes dynamically based on num_workers
for i in $(seq 1 $num_workers); do
  cat <<EOF >> kind-config.yaml
  - role: worker
EOF
done

echo "kind-config.yaml file generated with $num_workers worker nodes."

# Create the Kind cluster
kind create cluster --config kind-config.yaml

wait_for_nodes_ready() {
  echo "Waiting for all nodes to be in 'Ready' state..."
  while true; do
    not_ready_nodes=$(kubectl get nodes --no-headers | grep -v ' Ready' | wc -l)
    if [ "$not_ready_nodes" -eq 0 ]; then
      echo "All nodes are ready!"
      break
    else
      echo "$not_ready_nodes nodes not ready yet. Checking again in 5 seconds..."
      sleep 10
    fi
  done
}

# Wait for nodes to be ready before applying fake capacity
wait_for_nodes_ready

echo "deploying cert manager"

kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml

namespace="cert-manager"
label_selector="app=webhook"

check__cert_manager_pods_running() {
  pod_statuses=$(kubectl get pods -l "$label_selector" -n "$namespace" \
    -o go-template='{{ range .items }}{{ if not .metadata.deletionTimestamp }}{{ .status.phase }}{{ "\n" }}{{ end }}{{ end }}')

  not_running_pods=$(echo "$pod_statuses" | grep -v "Running")

  if [ -z "$not_running_pods" ]; then
    return 0
  else
    return 1
  fi
}

echo "Waiting for all pods in namespace '$namespace' with label '$label_selector' to be in 'Running' state..."
while true; do
  if check__cert_manager_pods_running; then
    echo "All pods are now running!"
    break
  else
    echo "Some pods are not yet running. Checking again in 5 seconds..."
    sleep 5
  fi
done

sleep 30

echo "deploying InstaSlice"

make deploy-emulated

wait_for_pods_running() {
  echo "Waiting for all pods in namespace '$namespace' to be in 'Running' state..."
  while true; do
    not_running_pods=$(kubectl get pods -n $namespace --no-headers | grep -v ' Running' | wc -l)
    if [ "$not_running_pods" -eq 0 ]; then
      echo "All pods in namespace '$namespace' are running!"
      break
    else
      echo "$not_running_pods pods not running yet. Checking again in 5 seconds..."
      sleep 5
    fi
  done
}

# Wait for all instaslice controller pods to be running before applying fake capacity
namespace=$namespace_instaslice
wait_for_pods_running

generate_fake_capacity() {
  local worker_name=$1

  # Generate the dynamic GPU UUIDs for the worker
  local gpu_uuid1="GPU-${worker_name}-8d042338-e67f-9c48-92b4-5b55c7e5133c"
  local gpu_uuid2="GPU-${worker_name}-31cfe05c-ed13-cd17-d7aa-c63db5108c24"

  cat <<EOF > fake-capacity.yaml
apiVersion: v1
items:
- apiVersion: inference.codeflare.dev/v1alpha1
  kind: Instaslice
  metadata:
    name: $worker_name
    namespace: default
  spec:
    cpuonnodeatboot: 72
    memoryonnodeatboot: 1000000000
    MigGPUUUID:
      $gpu_uuid1: NVIDIA A100-PCIE-40GB
      $gpu_uuid2: NVIDIA A100-PCIE-40GB
    migplacement:
    - ciProfileid: 0
      ciengprofileid: 0
      giprofileid: 0
      placements:
      - size: 1
        start: 0
      - size: 1
        start: 1
      - size: 1
        start: 2
      - size: 1
        start: 3
      - size: 1
        start: 4
      - size: 1
        start: 5
      - size: 1
        start: 6
      profile: 1g.5gb
    - ciProfileid: 1
      ciengprofileid: 0
      giprofileid: 1
      placements:
      - size: 2
        start: 0
      - size: 2
        start: 2
      - size: 2
        start: 4
      profile: 2g.10gb
    - ciProfileid: 2
      ciengprofileid: 0
      giprofileid: 2
      placements:
      - size: 4
        start: 0
      - size: 4
        start: 4
      profile: 3g.20gb
    - ciProfileid: 3
      ciengprofileid: 0
      giprofileid: 3
      placements:
      - size: 4
        start: 0
      profile: 4g.20gb
    - ciProfileid: 4
      ciengprofileid: 0
      giprofileid: 4
      placements:
      - size: 8
        start: 0
      profile: 7g.40gb
    - ciProfileid: 7
      ciengprofileid: 0
      giprofileid: 7
      placements:
      - size: 1
        start: 0
      - size: 1
        start: 1
      - size: 1
        start: 2
      - size: 1
        start: 3
      - size: 1
        start: 4
      - size: 1
        start: 5
      - size: 1
        start: 6
      profile: 1g.5gb+me
    - ciProfileid: 9
      ciengprofileid: 0
      giprofileid: 9
      placements:
      - size: 2
        start: 0
      - size: 2
        start: 2
      - size: 2
        start: 4
      - size: 2
        start: 6
      profile: 1g.10gb
    prepared:
      MIG-0f1cecc2-27a4-5452-85f2-ad9c3a15f1de:
        ciinfo: 0
        giinfo: 2
        parent: $gpu_uuid2
        podUUID: ""
        profile: 3g.20gb
        size: 4
        start: 4
      MIG-3dc2c68a-45e6-5acb-b043-caef296e6038:
        ciinfo: 0
        giinfo: 2
        parent: $gpu_uuid1
        podUUID: ""
        profile: 3g.20gb
        size: 4
        start: 4
  status:
    processed: "true"
kind: List
EOF
}

for i in $(seq 1 $num_workers); do
  if [ "$i" -eq 1 ]; then
    worker_name="kind-worker" 
  else
    worker_name="kind-worker$i"
  fi
  generate_fake_capacity $worker_name
  kubectl apply -f fake-capacity.yaml
  kubectl patch node $worker_name --subresource=status --type=json -p='[{"op":"add","path":"/status/capacity/nvidia.com~1accelerator-memory","value":"80Gi"}]'
done

check_daemonset_logs() {
  echo "Waiting for 'daemonset simulator mode' log line in DaemonSet pods with 'InstaSlice' in their names..."

  # Loop until all pods have the log line "daemonset simulator mode"
  while true; do
    all_pods_ready=true

    daemonset_pods=$(kubectl get pods -n $namespace -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep 'InstaSlice')

    for pod in $daemonset_pods; do
      echo "Checking logs for pod: $pod"
      if ! kubectl logs -n $namespace $pod | grep -q "daemonset simulator mode"; then
        echo "'daemonset simulator mode' not found in logs of $pod. Waiting..."
        all_pods_ready=false
        break
      else
        echo "Found 'daemonset simulator mode' in logs of $pod"
      fi
    done

    if [ "$all_pods_ready" = true ]; then
      echo "All DaemonSet pods with 'InstaSlice' in their names have the 'daemonset simulator mode' log line."
      break
    else
      sleep 5
    fi
  done
}

check_daemonset_logs
