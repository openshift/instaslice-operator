#!/bin/bash

# Script takes sample yaml and fires it on the controller with different pod names.
# It reports the elapsed time to spawn all the long running jobs on the cluster.
# Wakes up every 30 seconds to check submitted pod status, if 14 pods are running and 14 MIG slices exists then script exits.

ORIGINAL_NAME="cuda-vectoradd-1"

TOTAL_PODS=15
PODS_RUNNING_TARGET=14

YAML_CONTENT=$(cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $ORIGINAL_NAME
spec:
  restartPolicy: OnFailure
  containers:
  - name: $ORIGINAL_NAME
    image: "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0-ubi8"
    resources:
      limits:
        nvidia.com/mig-1g.5gb: 1
    command:
      - sh
      - -c
      -  "nvidia-smi -L; vectorAdd && sleep infinity"
EOF
)

# Start the timer
start_time=$(date +%s)
echo "Start time: $start_time"

for i in $(seq 1 $TOTAL_PODS); do
  NEW_NAME="cuda-vectoradd-$((i+1))"

  MODIFIED_YAML=$(echo "$YAML_CONTENT" | sed "s/$ORIGINAL_NAME/$NEW_NAME/g")

  echo "$MODIFIED_YAML" | kubectl apply -f -
done

count_running_pods() {
  running_count=0

  for i in $(seq 1 $TOTAL_PODS); do
    NEW_NAME="cuda-vectoradd-$((i+1))"
    status=$(kubectl get pod $NEW_NAME -o jsonpath='{.status.phase}')

    if [ "$status" == "Running" ]; then
      ((running_count++))
    fi
  done

  echo $running_count
}

while true; do
  sleep 30 

  running_pods=$(count_running_pods)
  echo "Currently $running_pods pods are in 'Running' state."

  if [ "$running_pods" -eq "$PODS_RUNNING_TARGET" ]; then
    echo "Exactly 14 pods are in the 'Running' state."
    break
  else
    echo "Waiting for 14 pods to be in the 'Running' state..."
  fi
done

# End the timer
end_time=$(date +%s)

elapsed_time=$((end_time - start_time))

echo "Time required for 14 pods to be in the 'Running' state: $elapsed_time seconds"

check_nvidia_smi() {
  lgi_count=$(sudo nvidia-smi mig -lgi | grep -E '^\| +[0-9]+ +MIG' | wc -l)
  lci_count=$(sudo sudo nvidia-smi mig -lci | grep -E '^\| +[0-9]+ +[0-9]+ +MIG' | wc -l)

  if [ "$lgi_count" -ne 14 ] || [ "$lci_count" -ne 14 ]; then
    return 1 
  else
    return 0 
  fi
}

if check_nvidia_smi; then
      echo "All pods are running, one pod is scheduling gated, and MIG counts are 14."
else
      echo "MIG count check failed."
fi
