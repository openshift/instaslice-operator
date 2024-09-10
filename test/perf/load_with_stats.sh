#!/bin/bash

# Script takes sample yaml and fires it on the controller with different pod names.
# It reports the elapsed time to completed all the fired jobs on the cluster.
# Wakes up very 30 seconds to check submitted pod status, if all are completed then script exists.

# Define the original pod name
ORIGINAL_NAME="cuda-vectoradd-1"

# Specify how many new names to generate
NUM_NEW_NAMES=24

# Read the original YAML content into a variable
YAML_CONTENT=$(cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $ORIGINAL_NAME
  finalizers:
  - org.instaslice/accelarator
spec:
  restartPolicy: OnFailure
  schedulingGates:
  - name: org.instaslice/accelarator
  containers:
  - name: $ORIGINAL_NAME
    image: "quay.io/tardieu/vectoradd:0.1.0"
    resources:
      limits:
        nvidia.com/mig-1g.5gb: 1
    command:
      - sh
      - -c
      -  "sleep infinity"
EOF
)

# Start the timer
start_time=$(date +%s)
echo $start_time

# Loop to generate new names and apply YAML
for i in $(seq 1 $NUM_NEW_NAMES); do
  NEW_NAME="cuda-vectoradd-$((i+1))"
  
  # Replace occurrences of the original name with the new name
  MODIFIED_YAML=$(echo "$YAML_CONTENT" | sed "s/$ORIGINAL_NAME/$NEW_NAME/g")
  
  # Apply the modified YAML
  echo "$MODIFIED_YAML" | kubectl apply -f -
  #sleep 3
done

# Periodically check the status of the pods
while true; do
  sleep 30

  # Check the status of all the pods
  all_completed=true
  for i in $(seq 1 $NUM_NEW_NAMES); do
    NEW_NAME="cuda-vectoradd-$((i+1))"
    status=$(kubectl get pod $NEW_NAME -o jsonpath='{.status.phase}')
    if [ "$status" != "Succeeded" ]; then
      all_completed=false
      break
    fi
  done

  # If all pods are completed, break the loop
  if $all_completed; then
    break
  fi
done

# End the timer
end_time=$(date +%s)

# Calculate the elapsed time
elapsed_time=$((end_time - start_time))

# Report the elapsed time
echo "Time required to complete $NUM_NEW_NAMES jobs: $elapsed_time seconds"

# Write the stats to a file
echo "Time required to complete $NUM_NEW_NAMES jobs: $elapsed_time seconds" > job_stats_$NUM_NEW_NAMES.txt
