#!/usr/bin/env bash

set -eou pipefail

KIND=${KIND:-kind}
KIND_NAME=${KIND_NAME:-"kind-e2e"}
KIND_IMAGE=${KIND_IMAGE:-kindest/node:v1.31.1}

_kind() {
  ${KIND} $@
}

# check to see if the cluster is running
if _kind get kubeconfig --name ${KIND_NAME} &>/dev/null; then
  echo "kind cluster ${KIND_NAME} is running"
  exit 0
fi

echo "Creating kind cluster named ${KIND_NAME}"
_kind create cluster -n ${KIND_NAME} --image ${KIND_IMAGE}
echo "Success: ${KIND_NAME} kind cluster created"
