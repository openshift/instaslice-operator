#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-kubectl}
TIMEOUT=${TIMEOUT:-600}
NODE_TIMEOUT=${NODE_TIMEOUT:-900}

_kubectl() {
	${KUBECTL} $@
}

_wait_for_pods_to_exist() {
	local ns=$1
	local pod_name_prefix=$2
	local max_wait_secs=$3
	local interval_secs=2
	local start_time
	start_time=$(date +%s)
	while true; do
		current_time=$(date +%s)
		if (((current_time - start_time) > max_wait_secs)); then
			echo "Waited for pods in namespace \"$ns\" with name prefix \"$pod_name_prefix\" to exist for $max_wait_secs seconds without luck. Returning with error."
			return 1
		fi
		if _kubectl -n "$ns" describe pod "$pod_name_prefix" --request-timeout "5s" &>/dev/null; then
			echo "Pods in namespace \"$ns\" with name prefix \"$pod_name_prefix\" exist."
			break
		else
			sleep $interval_secs
		fi
	done
}

_wait_for_node() {
	local max_wait_secs=$1
	local interval_secs=2
	local start_time
	start_time=$(date +%s)
	while true; do
		current_time=$(date +%s)
		if (((current_time - start_time) > max_wait_secs)); then
			echo "Waiting for Node"
			return 1
		fi
		if _kubectl wait node -l nvidia.com/mig.capable=true --timeout=900s --for=condition=ready --request-timeout=2s; then
			echo "Node Label Found"
			break
		else
			sleep $interval_secs
		fi
	done
}

_wait_for_apiserver() {
	local max_wait_secs=$1
	local interval_secs=2
	local start_time
	start_time=$(date +%s)
	while true; do
		current_time=$(date +%s)
		if (((current_time - start_time) > max_wait_secs)); then
			echo "Waiting for API server"
			return 1
		fi
		if _kubectl get nodes --request-timeout 10s; then
			echo "API server is back"
			break
		else
			sleep $interval_secs
		fi
	done
}

echo "Applying Nvidia"
_kubectl apply -f hack/manifests/nvidia-cpu-operator.yaml
echo "Waiting for Nvidia for ${TIMEOUT}"
_wait_for_pods_to_exist nvidia-gpu-operator gpu-operator ${TIMEOUT}
_kubectl wait --for condition=established --timeout=300s crd/clusterpolicies.nvidia.com
_kubectl apply -f hack/manifests/gpu-cluster-policy.yaml
_kubectl label $(${KUBECTL} get node -o name) nvidia.com/mig.config=all-enabled --overwrite
sleep 1m
echo "Waiting for Node to have nvidia.com/mig.capable=true label"
_wait_for_node ${NODE_TIMEOUT}
echo "Success: Nvidia deployed"
#echo "Waiting for API server"
#_wait_for_apiserver ${NODE_TIMEOUT}
# TODO optimize this
sleep 15m
