#!/usr/bin/env bash

# /*
# Copyright 2025.

# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at

#     http://www.apache.org/licenses/LICENSE-2.0

# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# */

set -euo pipefail

# Check for required arguments
if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <channel> <version>"
  echo "Example: $0 stable-v1.14 v1.14.1"
  exit 1
fi

CHANNEL="$1"
VERSION="$2"
STARTING_CSV="cert-manager-operator.${VERSION}"

KUBECTL=${KUBECTL:-oc}
NAMESPACE="cert-manager-operator"
DEPLOYMENT_NAME="cert-manager-operator-controller-manager"
WEBHOOK_NAMESPACE="cert-manager"
WEBHOOK_LABEL="app=webhook"
WEBHOOK_TIMEOUT="120s"
POLL_INTERVAL=5
MAX_RETRIES=24  # Total timeout = MAX_RETRIES * POLL_INTERVAL = 120s

# Check if cert-manager-operator is already installed
if $KUBECTL get namespace $NAMESPACE > /dev/null 2>&1; then
  echo "Namespace $NAMESPACE already exists. Checking deployment..."
  if $KUBECTL get deployment $DEPLOYMENT_NAME -n $NAMESPACE > /dev/null 2>&1; then
    echo "cert-manager-operator is already installed and running."
    exit 0
  fi
  echo "Namespace exists, but deployment not found. Proceeding with installation..."
fi

echo "Creating namespace for cert-manager-operator..."
$KUBECTL create namespace $NAMESPACE --dry-run=client -o yaml | $KUBECTL apply -f -

echo "Applying OperatorGroup configuration..."
cat <<EOF | $KUBECTL apply -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-cert-manager-operator
  namespace: $NAMESPACE
spec:
  targetNamespaces:
  - $NAMESPACE
EOF

echo "Applying Subscription configuration..."
cat <<EOF | $KUBECTL apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openshift-cert-manager-operator
  namespace: $NAMESPACE
spec:
  channel: $CHANNEL
  name: openshift-cert-manager-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
  startingCSV: $STARTING_CSV
EOF

echo "Waiting for cert-manager-operator deployment to be created..."
until $KUBECTL get deployment $DEPLOYMENT_NAME -n $NAMESPACE > /dev/null 2>&1; do
  echo "Waiting for $DEPLOYMENT_NAME to appear..."
  sleep $POLL_INTERVAL
done

echo "Waiting for cert-manager-operator deployment to be available..."
$KUBECTL wait --for=condition=Available deployment/$DEPLOYMENT_NAME \
  -n $NAMESPACE --timeout=$WEBHOOK_TIMEOUT

echo "Waiting for webhook pod to be created..."
retries=0
until $KUBECTL get pod -l $WEBHOOK_LABEL -n $WEBHOOK_NAMESPACE > /dev/null 2>&1; do
  if [ $retries -ge $MAX_RETRIES ]; then
    echo "Error: Webhook pod did not appear within the timeout period."
    exit 1
  fi
  echo "Waiting for webhook pod to appear... (Attempt $((retries + 1))/$MAX_RETRIES)"
  sleep $POLL_INTERVAL
  retries=$((retries + 1))
done

echo "Waiting for webhook pod to be ready..."
$KUBECTL wait --for=condition=ready pod -l $WEBHOOK_LABEL -n $WEBHOOK_NAMESPACE --timeout=$WEBHOOK_TIMEOUT

echo "cert-manager-operator setup completed successfully."
