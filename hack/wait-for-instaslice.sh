#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-kubectl}
TIMEOUT=${TIMEOUT:-900}

_kubectl() {
  ${KUBECTL} $@
}

_wait_for_controller_to_exist() {
  local max_wait_secs=$1
  local interval_secs=2
  local start_time
  start_time=$(date +%s)
  while true; do
    current_time=$(date +%s)
    if (((current_time - start_time) > max_wait_secs)); then
      echo "Timed out for controller"
      return 1
    fi
    if _kubectl wait --for=condition=Available deployment/instaslice-operator-controller-manager -n das-operator --timeout=120s; then
      break
    else
      sleep $interval_secs
    fi
  done
}

_wait_for_daemonset_to_exist() {
  local max_wait_secs=$1
  local interval_secs=2
  local start_time
  start_time=$(date +%s)
  while true; do
    current_time=$(date +%s)
    if (((current_time - start_time) > max_wait_secs)); then
      echo "Timed out for daemonset"
      return 1
    fi
    if _kubectl rollout status daemonset/instaslice-operator-controller-daemonset -n das-operator --timeout=60s --request-timeout=20s; then
      break
    else
      echo "Instaslice Pods"
      _kubectl get pods -n das-operator --request-timeout=20s || true
      echo "Daemonsets"
      _kubectl get daemonsets -n das-operator --request-timeout=20s -o yaml || true
      echo "Deployment logs"
      _kubectl logs -n das-operator deployment/instaslice-operator-controller-manager --all-containers --request-timeout=20s || true
      sleep $interval_secs
    fi
  done
}

echo "Waiting for instaslice controller"
_wait_for_controller_to_exist ${TIMEOUT}
echo "Waiting for instaslice daemonset"
_wait_for_daemonset_to_exist ${TIMEOUT}
