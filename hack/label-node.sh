#!/usr/bin/env bash

set -eou pipefail

KUBECTL=${KUBECTL:-oc}
NAMESPACE=${NAMESPACE:-"das-operator"}

_kubectl() {
        ${KUBECTL} $@
}

NODE_NAME=$(${KUBECTL} get pods -l control-plane=controller-manager -n ${NAMESPACE} -o jsonpath='{.items..spec.nodeName}')
echo "Patching node ${NODE_NAME}"
_kubectl patch node ${NODE_NAME} -p '{"metadata":{"labels":{"nvidia.com/mig.capable":"true"}}}'
