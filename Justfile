export PODMAN := env('PODMAN', 'podman')
export KUBECTL := env('KUBECTL', 'oc')
export EMULATED_MODE := env('EMULATED_MODE', 'disabled')
export RELATED_IMAGES := env('RELATED_IMAGES', 'related_images.json')
export DEPLOY_DIR := env('DEPLOY_DIR', 'deploy')


export OPERATOR_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-operator-next") | .image' $1""", RELATED_IMAGES)
export WEBHOOK_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-webhook-next") | .image' $1""", RELATED_IMAGES)
export SCHEDULER_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-scheduler-next") | .image' $1""", RELATED_IMAGES)
export DAEMONSET_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-daemonset-next") | .image' $1""", RELATED_IMAGES)

export OPERATOR_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-operator-next:latest"
export WEBHOOK_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-webhook-next:latest"
export SCHEDULER_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-scheduler-next:latest"
export DAEMONSET_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-daemonset-next:latest"

default:
  @just --list
  @just info

info:
  @echo
  @echo "Related Images: ${RELATED_IMAGES}"
  @echo
  @echo "    Operator Image: {{OPERATOR_IMAGE}}"
  @echo "     Webhook Image: {{WEBHOOK_IMAGE}}"
  @echo "   Scheduler Image: {{SCHEDULER_IMAGE}}"
  @echo "   Daemonset Image: {{DAEMONSET_IMAGE}}"
  @echo "     Emulated Mode: {{EMULATED_MODE}}"
  @echo

# Deploy DAS on OpenShift Container Platform
deploy-das-ocp: info regen-crd-k8s
  #!/usr/bin/env bash
  
  set -eou pipefail

  TMP_DIR=$(mktemp -d)
  cp ${DEPLOY_DIR}/*.yaml ${TMP_DIR}/

  sed -i "s/emulatedMode: .*/emulatedMode: \"${EMULATED_MODE}\"/" ${TMP_DIR}/03_instaslice_operator.cr.yaml

  echo "Rewriting Operator Image"
  sed -i "s|${OPERATOR_IMAGE_ORIGINAL}|${OPERATOR_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml
  echo "Rewriting Webhook Image"
  sed -i "s|${WEBHOOK_IMAGE_ORIGINAL}|${WEBHOOK_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml
  echo "Rewriting Scheduler Image"
  sed -i "s|${SCHEDULER_IMAGE_ORIGINAL}|${SCHEDULER_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml
  echo "Rewriting Daemonset Image"
  sed -i "s|${DAEMONSET_IMAGE_ORIGINAL}|${DAEMONSET_IMAGE}|g" ${TMP_DIR}/04_deployment.yaml
  echo "Rewriting Emulated Mode"
  sed -i "s/emulatedMode: .*/emulatedMode: \"${EMULATED_MODE}\"/" ${TMP_DIR}/03_instaslice_operator.cr.yaml

  ${KUBECTL} apply -f ${TMP_DIR}/

regen-crd-k8s:
  @echo "Generating CRDs into deploy directory"
  go build -o _output/tools/bin/controller-gen ./vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
  rm -f {{DEPLOY_DIR}}/00_instaslice-operator.crd.yaml
  rm -f {{DEPLOY_DIR}}/00_nodeaccelerators.crd.yaml
  ./_output/tools/bin/controller-gen crd paths=./pkg/apis/dasoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./{{DEPLOY_DIR}}
  mv {{DEPLOY_DIR}}/inference.redhat.com_dasoperators.yaml {{DEPLOY_DIR}}/00_instaslice-operator.crd.yaml
  mv {{DEPLOY_DIR}}/inference.redhat.com_nodeaccelerators.yaml {{DEPLOY_DIR}}/00_nodeaccelerators.crd.yaml

build-push-parallel:
  #!/usr/bin/env -S parallel --shebang --ungroup --jobs {{ num_cpus() }}
  just build-push-scheduler
  just build-push-daemonset
  just build-push-operator
  just build-push-webhook

# Build and push scheduler image
build-push-scheduler:
  ${PODMAN} build -f Dockerfile.scheduler.ocp -t {{SCHEDULER_IMAGE}} .
  ${PODMAN} push {{SCHEDULER_IMAGE}}

# Build and push daemonset image
build-push-daemonset:
  ${PODMAN} build -f Dockerfile.daemonset.ocp -t {{DAEMONSET_IMAGE}} .
  ${PODMAN} push {{DAEMONSET_IMAGE}}

# Build and push operator image
build-push-operator:
  ${PODMAN} build -f Dockerfile.ocp -t {{OPERATOR_IMAGE}} .
  ${PODMAN} push {{OPERATOR_IMAGE}}

# Build and push webhook image
build-push-webhook:
  ${PODMAN} build -f Dockerfile.webhook.ocp -t {{WEBHOOK_IMAGE}} .
  ${PODMAN} push {{WEBHOOK_IMAGE}}
