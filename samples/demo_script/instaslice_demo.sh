#!/bin/bash

# Run demo script as - bash ./samples/demo_script/instaslice_demo.sh
# make sure that you are in demo_script dir to run demo
# this script works easily on VM or baremetal with sudo access
# to run vLLM server YOUR huggingface token is required.
function delay_command() {
  echo "Press any key to run: $@"
  read -n 1 -s
  sleep 2
  "$@"
}

# Create the resource
echo
echo "Welcome to demo of running vLLM and Jupyter notebook workloads with InstaSlice"
echo

echo "We will now create a Jupyter notebook workload that will consume MIG slice that is created dynamically"
echo
echo "Lets see initial state of the GPUs on the node"
echo
delay_command sudo nvidia-smi
echo
delay_command kubectl apply -f ../tf-notebook.yaml
echo

pod_status=""
while [ "$pod_status" != "Running" ]; do
  pod_status=$(kubectl get pods -l app=tf-notebook -o jsonpath='{.items[0].status.phase}' 2>/dev/null)
  
  if [ -z "$pod_status" ]; then
    echo "No pods found with label 'app=tf-notebook'. Retrying..."
    sleep 5
    continue
  fi

  if [ "$pod_status" != "Running" ]; then
    sleep 5
  fi
done

echo
# Wait for server to come up
echo "Waiting for Jupyter notebook server to come up"
echo

sleep 30

# Check logs for errors
kubectl logs -l app=tf-notebook || { echo "Failed to retrieve logs"; exit 1; }

echo

# Port-forward the service
delay_command kubectl port-forward svc/tf-notebook 8888:8888 -n default &
port_forward_pid=$!

# Wait for the port-forward to be ready
sleep 10

echo
delay_command sudo nvidia-smi
echo
delay_command echo Done setting up Jupyter notebook server
echo
echo We see that a new partition is available for Jupyter notebook
echo
echo Let us use the notebook and train model
echo

echo "Now we submit vLLM server, provision a new MIG slice and perform inference on it"
echo
delay_command kubectl apply -f ../vllm_cache.yaml
echo
delay_command kubectl apply -f ../vllm_pod.yaml

pod_status=""
while [ "$pod_status" != "Running" ]; do
  pod_status=$(kubectl get pods -l app=vllm -o jsonpath='{.items[0].status.phase}' 2>/dev/null)
  
  if [ -z "$pod_status" ]; then
    echo "No pods found with label 'app=vllm'. Retrying..."
    sleep 5
    continue
  fi

  if [ "$pod_status" != "Running" ]; then
    sleep 5
  fi
done

echo
# Wait for server to come up
echo "Waiting for vLLM server to come up"
echo

sleep 10

# Check logs for errors
kubectl logs -l app=vllm || { echo "Failed to retrieve logs"; exit 1; }

echo

# Port-forward the service
delay_command kubectl port-forward svc/vllm 8000:8000 -n default &
port_forward_pid=$!

# Wait for the port-forward to be ready
sleep 10

echo
delay_command sudo nvidia-smi
echo

echo
# Print the curl request
echo "Sending curl request:"
echo
delay_command echo "curl http://localhost:8000/v1/completions \
  -H \"Content-Type: application/json\" \
  -d '{
    \"model\": \"facebook/opt-125m\",
    \"prompt\": \"San Francisco is a\",
    \"max_tokens\": 7,
    \"temperature\": 0
  }'"

echo
# Send the curl request
curl http://localhost:8000/v1/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "facebook/opt-125m",
    "prompt": "San Francisco is a",
    "max_tokens": 7,
    "temperature": 0
  }' || { echo "Curl request failed"; exit 1; }

echo

echo "Lets delete the workloads"
echo
delay_command kubectl delete -f ../tf-notebook.yaml
echo
#delay_command kubectl delete -f ../vllm_cache.yaml
echo
delay_command kubectl delete -f ../vllm_pod.yaml
echo
sleep 30
echo 
delay_command sudo nvidia-smi
echo
echo "GPUs are back to the intial state and ready to provision new slices"
# Clean up the port-forward
#kill $port_forward_pid
