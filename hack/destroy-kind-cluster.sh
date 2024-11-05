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
	_kind delete cluster -n ${KIND_NAME}
fi
