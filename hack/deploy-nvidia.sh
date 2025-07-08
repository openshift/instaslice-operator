#!/usr/bin/env bash

set -eou pipefail
set -x

KUBECTL=${KUBECTL:-oc}
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
		echo "Waiting for Node"
		if (((current_time - start_time) > max_wait_secs)); then
			echo "Waiting for Node timed out"
			return 1
		fi
		if _kubectl wait node -l nvidia.com/mig.capable=true --timeout=20s --for=condition=ready --request-timeout=2s; then
			echo "Node Label Found"
			break
		else
			_kubectl get pods -n nvidia-gpu-operator
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
sleep 10m
_kubectl label $(${KUBECTL} get node -o name) nvidia.com/mig.config=all-enabled --overwrite
# gpu operator is going to reboot the node at this point.
# API access will be down, so sleep for a period of time.
echo "Waiting for reboot"
sleep 10m
_wait_for_node ${NODE_TIMEOUT}
# wait for the gpu cluster policy to be ready
until [[ "$(_kubectl get clusterpolicy gpu-cluster-policy -n nvidia-gpu-operator -o jsonpath='{.status.state}')" == "ready" ]]; do
	echo "Waiting for ClusterPolicy to become ready..."
	_kubectl get pods -n nvidia-gpu-operator
	sleep 20
done
echo "ClusterPolicy is ready."
_kubectl get pods -n nvidia-gpu-operator
echo "Success: Nvidia deployed"
