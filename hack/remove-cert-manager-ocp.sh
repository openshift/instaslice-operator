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

KUBECTL=${KUBECTL:-oc}
NAMESPACE="cert-manager-operator"

echo "Deleting custom resources managed by cert-manager..."
$KUBECTL delete certificates --all -n $NAMESPACE || true
$KUBECTL delete certificaterequests --all -n $NAMESPACE || true
$KUBECTL delete issuers --all -n $NAMESPACE || true
$KUBECTL delete clusterissuers --all || true

echo "Deleting Subscription for cert-manager-operator..."
$KUBECTL delete subscription openshift-cert-manager-operator -n $NAMESPACE || true

echo "Deleting ClusterServiceVersion (CSV)..."
$KUBECTL get csv -n $NAMESPACE -o name | xargs -r $KUBECTL delete -n $NAMESPACE

echo "Deleting OperatorGroup..."
$KUBECTL delete operatorgroup openshift-cert-manager-operator -n $NAMESPACE || true

echo "Deleting services..."
$KUBECTL delete service cert-manager cert-manager-webhook -n cert-manager || true

echo "Deleting Custom Resource Definitions (CRDs)..."
$KUBECTL delete crd certificates.cert-manager.io \
  certificaterequests.cert-manager.io \
  issuers.cert-manager.io \
  clusterissuers.cert-manager.io \
  challenges.acme.cert-manager.io \
  orders.acme.cert-manager.io || true

echo "Deleting the namespace..."
$KUBECTL delete namespace $NAMESPACE || true

echo "Deleting deployments in the cert-manager namespace..."
$KUBECTL delete deployment -n cert-manager --all

echo "Uninstall of cert manager operator completed successfully."
