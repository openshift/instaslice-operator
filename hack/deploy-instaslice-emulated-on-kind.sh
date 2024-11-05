#!/usr/bin/env bash

set -eou pipefail

KIND=${KIND:-kind}
KUBECTL=${KUBECTL:-kubectl}
KIND_NAME=${KIND_NAME:-"kind-e2e"}
KIND_CONTEXT=kind-${KIND_NAME}
NAMESPACE=${NAMESPACE:-"instaslice-system"}
KIND_NODE_NAME=${KIND_NODE_NAME:-"kind-e2e-control-plane"}
WEBHOOK_TIMEOUT=${WEBHOOK_TIMEOUT:-2m}

_kubectl() {
        ${KUBECTL} --context ${KIND_CONTEXT} $@
}

_kind() {
	${KIND} $@
}

_kind load docker-image ${IMG} --name ${KIND_NAME}
_kind load docker-image ${IMG_DMST} --name ${KIND_NAME}
echo "Creating namespace ${NAMESPACE}"
_kubectl create ns ${NAMESPACE}
echo "Installing instaslice CRD"
make install
sleep 10
${KUBECTL} config set-context ${KIND_CONTEXT}
_kubectl patch node ${KIND_NODE_NAME} -p '{"metadata":{"labels":{"nvidia.com/mig.capable":"true"}}}'
echo "Deploying Instaslice controller-manager and daemonset"
make deploy-emulated
_kubectl wait --for=condition=ready pod -l control-plane=controller-manager -n ${NAMESPACE} --timeout=${WEBHOOK_TIMEOUT}
