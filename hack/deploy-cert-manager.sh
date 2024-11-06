#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-kubectl}
CERT_MANAGER_URL=${CERT_MANAGER_URL:-"https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml"}
CERTMANAGER_TIMEOUT=${CERTMANAGER_TIMEOUT:-120}
WEBHOOK_TIMEOUT=${WEBHOOK_TIMEOUT:-2m}

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

echo "Applying cert manager URL"
_kubectl apply -f ${CERT_MANAGER_URL}
echo "Waiting for cert manager for ${CERTMANAGER_TIMEOUT}s"
_wait_for_pods_to_exist cert-manager cert-manager ${CERTMANAGER_TIMEOUT}
_kubectl wait --for=condition=ready pod -l app=webhook -n cert-manager --timeout=${WEBHOOK_TIMEOUT}
echo "Success: cert-manager deployed"
