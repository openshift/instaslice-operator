#!/usr/bin/env bash

set -eou pipefail

# Source environment variables if available
if [ -f ".env" ]; then
  source .env
fi

# Create temporary directory
TMP_DIR=$(mktemp -d)
trap "rm -rf ${TMP_DIR}" EXIT
cp ${DEPLOY_DIR}/*.yaml ${TMP_DIR}/

echo "Rewriting Operator Image"
sed -i "s|${OPERATOR_IMAGE_ORIGINAL}|${OPERATOR_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml
echo "Rewriting Webhook Image"
sed -i "s|${WEBHOOK_IMAGE_ORIGINAL}|${WEBHOOK_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml
echo "Rewriting Scheduler Image"
sed -i "s|${SCHEDULER_IMAGE_ORIGINAL}|${SCHEDULER_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml
echo "Rewriting Daemonset Image"
sed -i "s|${DAEMONSET_IMAGE_ORIGINAL}|${DAEMONSET_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml

echo "Applying CRDs..."
CRD_FILES=$(find ${TMP_DIR} -name "*.yaml" \( -name "*.crd.yaml" -o -exec grep -l "kind: CustomResourceDefinition" {} \; \) | sort -u)
if [ -n "$CRD_FILES" ]; then
  for crd_file in $CRD_FILES; do
    echo "Applying CRD: $(basename $crd_file)"
    ${KUBECTL} apply -f "$crd_file"
  done

  echo "Waiting for CRDs to be established..."
  APPLIED_CRDS=$(for crd_file in $CRD_FILES; do
    awk '/^metadata:/{p=1} p && /^  name:/{print $2; p=0}' "$crd_file"
  done | sort -u)
  for crd_name in $APPLIED_CRDS; do
    if [ -n "$crd_name" ]; then
      echo "Waiting for CRD: $crd_name"
      ${KUBECTL} wait --for condition=established --timeout=60s crd/$crd_name || echo "Warning: Failed to wait for CRD $crd_name"
    fi
  done
else
  echo "No CRDs found to apply."
fi

# Now apply the remaining resources in numerical order, auto-discovering all prefixes.
echo "Applying remaining resources..."
ALL_PREFIXES=$(find ${TMP_DIR} -name "[0-9][0-9]_*.yaml" | sed 's/.*\/\([0-9][0-9]\)_.*/\1/' | sort -u)
for prefix in $ALL_PREFIXES; do
  PREFIX_FILES=$(find ${TMP_DIR} -name "${prefix}_*.yaml" | sort)
  for file in $PREFIX_FILES; do
    echo "Applying: $(basename $file)"
    ${KUBECTL} apply -f "$file"
  done
done

echo "DAS operator deployed successfully!"

# Clean up generated files after successful deployment.
echo "Cleaning up generated files..."
rm -rf config/ 2>/dev/null || true
rm -f deploy/inference.redhat.com_allocationclaims.yaml 2>/dev/null || true
rm -rf ${TMP_DIR}
echo "Cleanup completed!"
