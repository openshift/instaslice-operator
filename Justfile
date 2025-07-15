export PODMAN := env('PODMAN', 'podman')
export KUBECTL := env('KUBECTL', 'oc')
export EMULATED_MODE := env('EMULATED_MODE', 'disabled')
export RELATED_IMAGES := env('RELATED_IMAGES', 'related_images.json')
export DEPLOY_DIR := env('DEPLOY_DIR', 'deploy')
export OPERATOR_SDK := env('OPERATOR_SDK', 'operator-sdk')
export OPERATOR_VERSION := env('OPERATOR_VERSION', '0.1.0')
export GOLANGCI_LINT:= env('GOLANGCI_LINT', 'golangci-lint')
export MARKDOWNLINT := env('MARKDOWNLINT', 'markdownlint')
export KUBECONFIG := env('KUBECONFIG', '')

export OPERATOR_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-operator-next") | .image' $1""", RELATED_IMAGES)
export WEBHOOK_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-webhook-next") | .image' $1""", RELATED_IMAGES)
export SCHEDULER_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-scheduler-next") | .image' $1""", RELATED_IMAGES)
export DAEMONSET_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-daemonset-next") | .image' $1""", RELATED_IMAGES)
export BUNDLE_IMAGE := shell("""jq -r '.[] | select(.name == "instaslice-operator-bundle-next") | .image' $1""", RELATED_IMAGES)

export OPERATOR_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-operator-next:latest"
export WEBHOOK_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-webhook-next:latest"
export SCHEDULER_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-scheduler-next:latest"
export DAEMONSET_IMAGE_ORIGINAL := "quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-daemonset-next:latest"

# Default target - shows available commands and environment info
default:
  @just --list
  @just info

# Display environment and image configuration information
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

  hack/deploy-das-ocp.sh ${TMP_DIR}

# Regenerate Custom Resource Definitions (CRDs) for Kubernetes
regen-crd-k8s:
  @echo "Generating CRDs into deploy directory"
  go build -o _output/tools/bin/controller-gen ./vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
  rm -f {{DEPLOY_DIR}}/00_instaslice-operator.crd.yaml
  rm -f {{DEPLOY_DIR}}/00_nodeaccelerators.crd.yaml
  ./_output/tools/bin/controller-gen crd paths=./pkg/apis/dasoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./{{DEPLOY_DIR}}
  mv {{DEPLOY_DIR}}/inference.redhat.com_dasoperators.yaml {{DEPLOY_DIR}}/00_instaslice-operator.crd.yaml
  mv {{DEPLOY_DIR}}/inference.redhat.com_nodeaccelerators.yaml {{DEPLOY_DIR}}/00_nodeaccelerators.crd.yaml

# Build and push all container images in parallel
build-push-parallel:
  #!/usr/bin/env -S parallel --shebang --ungroup --jobs {{ num_cpus() }}
  just build-push-scheduler
  just build-push-daemonset
  just build-push-operator
  just build-push-webhook

# Build and push scheduler image
build-push-scheduler:
  {{PODMAN}} build -f Dockerfile.scheduler.ocp -t {{SCHEDULER_IMAGE}} .
  {{PODMAN}} push {{SCHEDULER_IMAGE}}

# Build and push daemonset image
build-push-daemonset:
  {{PODMAN}} build -f Dockerfile.daemonset.ocp -t {{DAEMONSET_IMAGE}} .
  {{PODMAN}} push {{DAEMONSET_IMAGE}}

# Build and push operator image
build-push-operator:
  {{PODMAN}} build -f Dockerfile.ocp -t {{OPERATOR_IMAGE}} .
  {{PODMAN}} push {{OPERATOR_IMAGE}}

# Build and push webhook image
build-push-webhook:
  {{PODMAN}} build -f Dockerfile.webhook.ocp -t {{WEBHOOK_IMAGE}} .
  {{PODMAN}} push {{WEBHOOK_IMAGE}}

# Generate operator bundle using operator-sdk
bundle-generate:
	{{OPERATOR_SDK}} generate bundle --input-dir {{DEPLOY_DIR}}/ --version {{OPERATOR_VERSION}} --output-dir=bundle-ocp --package das-operator

# Build and push developer bundle image
build-push-developer-bundle: (_build-push-bundle "bundle.developer.Dockerfile")

# Build and push the OCP bundle
build-push-bundle: (_build-push-bundle "bundle-ocp.Dockerfile")

# Private function to build and push a bundle image
_build-push-bundle bundleDockerfile:
	{{PODMAN}} build -f {{bundleDockerfile}} -t {{BUNDLE_IMAGE}} .
	{{PODMAN}} push {{BUNDLE_IMAGE}}

# Deploy CRDs and run operator locally for development
run-local:
  {{KUBECTL}} apply -f deploy/00_instaslice-operator.crd.yaml
  {{KUBECTL}} apply -f deploy/00_node_allocationclaims.crd.yaml
  {{KUBECTL}} apply -f deploy/00_nodeaccelerators.crd.yaml
  {{KUBECTL}} apply -f deploy/01_namespace.yaml
  {{KUBECTL}} apply -f deploy/01_operator_sa.yaml
  {{KUBECTL}} apply -f deploy/02_operator_rbac.yaml
  {{KUBECTL}} apply -f deploy/03_instaslice_operator.cr.yaml
  RELATED_IMAGE_DAEMONSET_IMAGE={{DAEMONSET_IMAGE}} RELATED_IMAGE_WEBHOOK_IMAGE={{WEBHOOK_IMAGE}} RELATED_IMAGE_SCHEDULER_IMAGE={{SCHEDULER_IMAGE}} go run cmd/das-operator/main.go operator --namespace=das-operator --kubeconfig="{{KUBECONFIG}}"

# Run end-to-end tests with optional focus filter
test-e2e e2e-args="-ginkgo.v" focus="":
  #!/usr/bin/env bash

  set -eoux pipefail

  args=("{{e2e-args}}")
  if [[ -n "{{focus}}" ]]; then
    args+=("-ginkgo.focus={{focus}}")
  fi

  echo "=== Running e2e tests ==="
  GOFLAGS=-mod=vendor go test ./test/e2e -v -count=1 -args ${args[@]}

# Remove NVIDIA GPU operator from OpenShift
undeploy-nvidia-ocp:
  {{KUBECTL}} delete -f hack/manifests/gpu-cluster-policy.yaml
  {{KUBECTL}} delete -f hack/manifests/nvidia-cpu-operator.yaml

# Deploy NVIDIA GPU operator to OpenShift
deploy-nvidia-ocp:
  hack/deploy-nvidia.sh

# Clean up all deployed Kubernetes resources
undeploy:
  @echo "=== Deleting K8s resources ==="
  {{KUBECTL}} delete --ignore-not-found --wait=true -f {{DEPLOY_DIR}}/
  # Wait for the operator namespace to be fully removed
  {{KUBECTL}} wait --for=delete namespace/das-operator --timeout=120s || true

# Deploy cert-manager for Kubernetes
deploy-cert-manager:
  export KUBECTL=$(KUBECTL) IMG=$(IMG) IMG_DMST=$(IMG_DMST) && \
    hack/deploy-cert-manager.sh

# Deploy cert-manager for OpenShift
deploy-cert-manager-ocp:
  {{KUBECTL}} apply -f hack/manifests/cert-manager-rh.yaml

# Remove cert-manager from OpenShift
undeploy-cert-manager-ocp:
  {{KUBECTL}} delete -f hack/manifests/cert-manager-rh.yaml

# Deploy Node Feature Discovery (NFD) operator for OpenShift
deploy-nfd-ocp:
  hack/deploy-nfd.sh

# Run golangci-lint on the codebase
lint-go:
  {{GOLANGCI_LINT}} run --timeout 5m ./pkg/...

# Run golangci-lint and automatically fix issues
lint-go-fix: lint-go
  {{GOLANGCI_LINT}} run --fix

# Run all linting (markdown and Go)
lint: lint-md lint-go

# Run markdownlint on markdown files
lint-md:
  {{MARKDOWNLINT}} -c .markdownlint.json *.md

# Run markdownlint and automatically fix issues
lint-md-fix:
  {{MARKDOWNLINT}} -c .markdownlint.json --fix *.md

# Run all linting fixes (markdown and Go)
lint-fix: lint-md-fix lint-go-fix
