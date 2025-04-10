#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-kubectl}
NFD_TIMEOUT=${NFD_TIMEOUT:-600}
TIMEOUT=${TIMEOUT:-10m}

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
		if kubectl -n "$ns" describe pod "$pod_name_prefix" --request-timeout "5s" &>/dev/null; then
			echo "Pods in namespace \"$ns\" with name prefix \"$pod_name_prefix\" exist."
			break
		else
			sleep $interval_secs
		fi
	done
}

echo "Applying NFD minifest"
_kubectl apply -f hack/manifests/nfd.yaml
echo "Waiting for NFD for ${NFD_TIMEOUT}s"
_wait_for_pods_to_exist openshift-nfd nfd-controller-manager ${NFD_TIMEOUT}
_kubectl wait --for=condition=ready pod -l control-plane=controller-manager -n openshift-nfd --timeout=${TIMEOUT}
_kubectl apply -f hack/manifests/nfd-instance.yaml
echo "Success: nfd deployed"
