#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-oc}
NAMESPACE=${NAMESPACE:-"das-operator"}
WEBHOOK_TIMEOUT=${WEBHOOK_TIMEOUT:-2m}

_kubectl() {
        ${KUBECTL} $@
}

echo "Creating namespace ${NAMESPACE}"
_kubectl create ns ${NAMESPACE}
echo "Installing instaslice CRD"
make install
sleep 10
echo "Deploying Instaslice controller-manager"
IMG=${IMG} IMG_DMST=${IMG_DMST} make ocp-deploy-emulated
_kubectl wait --for=condition=ready pod -l control-plane=controller-manager -n ${NAMESPACE} --timeout=${WEBHOOK_TIMEOUT}
NODE_NAME=$(${KUBECTL} get pods -l control-plane=controller-manager -n ${NAMESPACE} -o jsonpath='{.items..spec.nodeName}')
echo "Patching node ${NODE_NAME}"
_kubectl patch node ${NODE_NAME} -p '{"metadata":{"labels":{"nvidia.com/mig.capable":"true"}}}'
