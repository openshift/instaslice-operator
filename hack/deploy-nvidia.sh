#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-kubectl}
TIMEOUT=${TIMEOUT:-600}

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

echo "Applying Nvidia"
_kubectl apply -f hack/manifests/nvidia-cpu-operator.yaml
echo "Waiting for Nvidia for ${TIMEOUT}"
_wait_for_pods_to_exist nvidia-gpu-operator gpu-operator ${TIMEOUT}
_kubectl wait --for condition=established --timeout=300s crd/clusterpolicies.nvidia.com
_kubectl apply -f hack/manifests/gpu-cluster-policy.yaml
_kubectl label $(${KUBECTL} get node -o name) nvidia.com/mig.config=all-enabled --overwrite
echo "Success: Nvidia deployed"
