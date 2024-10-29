#!/usr/bin/env bash

set -eou pipefail

NODE_NODE_NAME=${KIND_NODE_NAME:-"kind-control-plane"}

${KIND} load docker-image ${IMG} --name ${KIND_NAME}
${KIND} load docker-image ${IMG_DMST} --name ${KIND_NAME}
make install
sleep 10
${KUBECTL} config set-context ${KIND_CONTEXT}
${KUBECTL} patch node ${KIND_NODE_NAME} -p '{"metadata":{"labels":{"nvidia.com/mig.capable":"true"}}}'
