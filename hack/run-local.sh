#!/usr/bin/env bash

set -eou pipefail

# Source environment variables if available
if [ -f ".env" ]; then
  source .env
fi

# Create temporary directory and copy deployment files
TMP_DIR=$(mktemp -d)
trap "rm -rf ${TMP_DIR}" EXIT

cp ${DEPLOY_DIR}/*.yaml ${TMP_DIR}/

# Apply CRDs and resources
${KUBECTL} apply -f ${TMP_DIR}/00_instaslice-operator.crd.yaml
${KUBECTL} apply -f ${TMP_DIR}/00_node_allocationclaims.crd.yaml
${KUBECTL} apply -f ${TMP_DIR}/00_nodeaccelerators.crd.yaml
${KUBECTL} apply -f ${TMP_DIR}/01_namespace.yaml
${KUBECTL} apply -f ${TMP_DIR}/01_operator_sa.yaml
${KUBECTL} apply -f ${TMP_DIR}/02_operator_rbac.yaml
${KUBECTL} apply -f ${TMP_DIR}/03_instaslice_operator.cr.yaml

# Run the operator locally
RELATED_IMAGE_DAEMONSET_IMAGE=${DAEMONSET_IMAGE} \
  RELATED_IMAGE_WEBHOOK_IMAGE=${WEBHOOK_IMAGE} \
  RELATED_IMAGE_SCHEDULER_IMAGE=${SCHEDULER_IMAGE} \
  go run cmd/das-operator/main.go operator --namespace=das-operator --kubeconfig="${KUBECONFIG}"
