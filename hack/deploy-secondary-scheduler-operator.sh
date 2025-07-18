#!/usr/bin/env bash

set -eou pipefail
set -x

oc apply -f - <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: openshift-secondary-scheduler-operator
EOF

oc apply -f - <<'EOF'
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: secondary-scheduler-operator-group
  namespace: openshift-secondary-scheduler-operator
spec:
  targetNamespaces:
  - openshift-secondary-scheduler-operator
EOF

oc apply -f - <<'EOF'
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openshift-secondary-scheduler-operator
  namespace: openshift-secondary-scheduler-operator
spec:
  channel: stable
  installPlanApproval: Automatic
  name: openshift-secondary-scheduler-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

CSVName=""
for ((i = 1; i <= 60; i++)); do
    if oc get sub openshift-secondary-scheduler-operator -n openshift-secondary-scheduler-operator -o jsonpath='{.status.currentCSV}' >/dev/null 2>&1; then
        CSVName=$(oc get sub openshift-secondary-scheduler-operator -n openshift-secondary-scheduler-operator -o jsonpath='{.status.currentCSV}')
        if [[ -n "$CSVName" ]]; then
            break
        fi
    fi
    sleep 2
done

_apiReady=0
echo "* Using CSV: ${CSVName}"
for ((i = 1; i <= 20; i++)); do
    sleep 30
    if oc get csv -n openshift-secondary-scheduler-operator "$CSVName" -o jsonpath='{.status.phase}' >/dev/null 2>&1; then
        phase=$(oc get csv -n openshift-secondary-scheduler-operator "$CSVName" -o jsonpath='{.status.phase}')
        if [[ "$phase" == "Succeeded" ]]; then
            _apiReady=1
            break
        fi
        echo "Waiting for CSV to be ready"
    fi
done

if [ $_apiReady -eq 0 ]; then
    echo "secondary-scheduler-operator subscription could not install in the allotted time."
    exit 1
fi

echo "secondary-scheduler-operator installed successfully"
