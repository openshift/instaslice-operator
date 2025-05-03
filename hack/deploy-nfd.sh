#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-kubectl}
NFD_TIMEOUT=${NFD_TIMEOUT:-900}
TIMEOUT=${TIMEOUT:-10m}

_kubectl() {
	${KUBECTL} $@
}

_wait_for_nfd() {
	local max_wait_secs=$1
	local interval_secs=2
	local api_ready=0
	local start_time
	local csv_name=""

	for ((i = 1; i <= 60; i++)); do
		output=$(oc get sub nfd -n openshift-nfd -o jsonpath='{.status.currentCSV}' >>/dev/null && echo "exists" || echo "not found")
		if [ "$output" != "exists" ]; then
			sleep 2
			continue
		fi
		csv_name=$(oc get sub nfd -n openshift-nfd -o jsonpath='{.status.currentCSV}')
		if [ "$csv_name" != "" ]; then
			break
		fi
		sleep 10
	done

	echo "* Using CSV: ${csv_name}"
	start_time=$(date +%s)
	for ((i = 1; i <= 20; i++)); do
		sleep 30
		current_time=$(date +%s)
		if (((current_time - start_time) > max_wait_secs)); then
			echo "Waiting for NFD timeout"
			return 1
		fi
		output=$(_kubectl get csv -n openshift-nfd $csv_name -o jsonpath='{.status.phase}' >>/dev/null && echo "exists" || echo "not found")
		if [ "$output" != "exists" ]; then
			continue
		fi
		phase=$(_kubectl get csv -n openshift-nfd $csv_name -o jsonpath='{.status.phase}')
		if [ "$phase" == "Succeeded" ]; then
			api_ready=1
			break
		fi

		if [ $api_ready -eq 0 ]; then
			echo "nfd-operator subscription could not install in the allotted time."
			exit 1
		fi

		echo "NFD Ready"
	done
}

echo "Applying NFD minifest"
_kubectl apply -f hack/manifests/nfd.yaml
local channel=$(_kubectl get packagemanifest nfd -n openshift-marketplace -o jsonpath='{.status.defaultChannel}')
_kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: nfd
  namespace: openshift-nfd
spec:
  channel: ${channel}
  installPlanApproval: Automatic
  name: nfd
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF
echo "Waiting for NFD for ${NFD_TIMEOUT}s"
_wait_for_nfd ${NFD_TIMEOUT}
_kubectl apply -f hack/manifests/nfd-instance.yaml
_kubectl wait nodefeaturediscovery -n openshift-nfd --for=condition=Available --timeout=15m --all
echo "Success: nfd deployed"
