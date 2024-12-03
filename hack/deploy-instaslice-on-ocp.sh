#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-oc}
NAMESPACE=${NAMESPACE:-"instaslice-system"}
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
IMG=${IMG} IMG_DMST=${IMG_DMST} make ocp-deploy
_kubectl wait --for=condition=ready pod -l control-plane=controller-manager -n ${NAMESPACE} --timeout=${WEBHOOK_TIMEOUT}