export PODMAN := env('PODMAN', 'podman')
export KUBECTL := env('KUBECTL', 'oc')
export EMULATED_MODE := env('EMULATED_MODE', 'disabled')
export RELATED_IMAGES := env('RELATED_IMAGES', 'related_images.json')
export DEPLOY_DIR := env('DEPLOY_DIR', 'deploy')
export OPERATOR_SDK := env('OPERATOR_SDK', 'operator-sdk')
export OPERATOR_VERSION := env('OPERATOR_VERSION', '0.1.0')
export GOLANGCI_LINT := env('GOLANGCI_LINT', 'golangci-lint')
export MARKDOWNLINT := env('MARKDOWNLINT', 'markdownlint')
export KUBECONFIG := env('KUBECONFIG', '')
export SHFMT := env('SHFMT', 'shfmt')
export OPERATOR_IMAGE := env('OPERATOR_IMAGE', shell("""jq -r '.[] | select(.name == "instaslice-operator-next") | .image' $1""", RELATED_IMAGES))
export WEBHOOK_IMAGE := env('WEBHOOK_IMAGE', shell("""jq -r '.[] | select(.name == "instaslice-webhook-next") | .image' $1""", RELATED_IMAGES))
export SCHEDULER_IMAGE := env('SCHEDULER_IMAGE', shell("""jq -r '.[] | select(.name == "instaslice-scheduler-next") | .image' $1""", RELATED_IMAGES))
export DAEMONSET_IMAGE := env('DAEMONSET_IMAGE', shell("""jq -r '.[] | select(.name == "instaslice-daemonset-next") | .image' $1""", RELATED_IMAGES))
export BUNDLE_IMAGE := env('BUNDLE_IMAGE', shell("""jq -r '.[] | select(.name == "instaslice-operator-bundle-next") | .image' $1""", RELATED_IMAGES))
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
    @echo "===== Environment Variables Configuration ====="
    @echo
    @echo "Container Runtime & CLI Tools:"
    @echo "  PODMAN: {{ PODMAN }}"
    @echo "  KUBECTL: {{ KUBECTL }}"
    @echo "  KUBECONFIG: {{ KUBECONFIG }}"
    @echo
    @echo "Operational Configuration:"
    @echo "  EMULATED_MODE: {{ EMULATED_MODE }}"
    @echo "  RELATED_IMAGES: {{ RELATED_IMAGES }}"
    @echo "  DEPLOY_DIR: {{ DEPLOY_DIR }}"
    @echo
    @echo "Development Tools:"
    @echo "  OPERATOR_SDK: {{ OPERATOR_SDK }}"
    @echo "  OPERATOR_VERSION: {{ OPERATOR_VERSION }}"
    @echo "  GOLANGCI_LINT: {{ GOLANGCI_LINT }}"
    @echo "  MARKDOWNLINT: {{ MARKDOWNLINT }}"
    @echo
    @echo "Container Images (Resolved from JSON, or environment):"
    @echo "  OPERATOR_IMAGE: {{ OPERATOR_IMAGE }}"
    @echo "  WEBHOOK_IMAGE: {{ WEBHOOK_IMAGE }}"
    @echo "  SCHEDULER_IMAGE: {{ SCHEDULER_IMAGE }}"
    @echo "  DAEMONSET_IMAGE: {{ DAEMONSET_IMAGE }}"
    @echo "  BUNDLE_IMAGE: {{ BUNDLE_IMAGE }}"
    @echo

# Deploy DAS on OpenShift Container Platform
[group('deploy')]
deploy-das-ocp: info regen-crd-k8s
    hack/deploy-das-ocp.sh

# Regenerate Custom Resource Definitions (CRDs) - generates into manifests directory
[group('generate')]
regen-crd:
    go build -o _output/tools/bin/controller-gen ./vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
    rm -f manifests/instaslice-operator.crd.yaml
    ./_output/tools/bin/controller-gen crd paths=./pkg/apis/dasoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./manifests
    mv manifests/inference.redhat.com_dasoperators.yaml manifests/instaslice-operator.crd.yaml
    cp manifests/instaslice-operator.crd.yaml {{ DEPLOY_DIR }}/00_instaslice-operator.crd.yaml
    cp manifests/inference.redhat.com_nodeaccelerators.yaml {{ DEPLOY_DIR }}/00_nodeaccelerators.crd.yaml

# Regenerate Custom Resource Definitions (CRDs) for Kubernetes
[group('generate')]
regen-crd-k8s:
    @echo "Generating CRDs into deploy directory"
    go build -o _output/tools/bin/controller-gen ./vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
    rm -f {{ DEPLOY_DIR }}/00_instaslice-operator.crd.yaml
    rm -f {{ DEPLOY_DIR }}/00_nodeaccelerators.crd.yaml
    ./_output/tools/bin/controller-gen crd paths=./pkg/apis/dasoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./{{ DEPLOY_DIR }}
    mv {{ DEPLOY_DIR }}/inference.redhat.com_dasoperators.yaml {{ DEPLOY_DIR }}/00_instaslice-operator.crd.yaml
    mv {{ DEPLOY_DIR }}/inference.redhat.com_nodeaccelerators.yaml {{ DEPLOY_DIR }}/00_nodeaccelerators.crd.yaml

# Generate clients using codegen
[group('generate')]
generate-clients:
    GO=GO111MODULE=on GOFLAGS=-mod=readonly hack/update-codegen.sh

# Verify generated client code is up to date
[group('generate')]
verify-codegen:
    hack/verify-codegen.sh

# Generate all artifacts - CRDs and clients
[group('generate')]
generate: regen-crd regen-crd-k8s generate-clients

# Build and push all container images using parallel
[group('build')]
build-push-parallel:
    #!/usr/bin/env -S parallel --shebang --ungroup --jobs {{ num_cpus() }}
    just build-push-scheduler
    just build-push-daemonset
    just build-push-operator
    just build-push-webhook

# Build and push all container images in parallel using just's [parallel]
# [group: 'build']
# [parallel]
# build-push: build-push-scheduler build-push-daemonset build-push-operator build-push-webhook

# Build and push scheduler image
[group('build')]
build-push-scheduler:
    {{ PODMAN }} build -f Dockerfile.scheduler.ocp -t {{ SCHEDULER_IMAGE }} .
    {{ PODMAN }} push {{ SCHEDULER_IMAGE }}

# Build and push daemonset image
[group('build')]
build-push-daemonset:
    {{ PODMAN }} build -f Dockerfile.daemonset.ocp -t {{ DAEMONSET_IMAGE }} .
    {{ PODMAN }} push {{ DAEMONSET_IMAGE }}

# Build and push operator image
[group('build')]
build-push-operator:
    {{ PODMAN }} build -f Dockerfile.ocp -t {{ OPERATOR_IMAGE }} .
    {{ PODMAN }} push {{ OPERATOR_IMAGE }}

# Build and push webhook image
[group('build')]
build-push-webhook:
    {{ PODMAN }} build -f Dockerfile.webhook.ocp -t {{ WEBHOOK_IMAGE }} .
    {{ PODMAN }} push {{ WEBHOOK_IMAGE }}

# Generate operator bundle using operator-sdk
[group('bundle')]
bundle-generate:
    {{ OPERATOR_SDK }} generate bundle --input-dir {{ DEPLOY_DIR }}/ --version {{ OPERATOR_VERSION }} --output-dir=bundle-ocp --package das-operator

# Build and push developer bundle image
[group('build')]
build-push-developer-bundle: (_build-push-bundle "bundle.developer.Dockerfile")

# Build and push the OCP bundle
[group('build')]
build-push-bundle: (_build-push-bundle "bundle-ocp.Dockerfile")

# Private function to build and push a bundle image
[group('build')]
_build-push-bundle bundleDockerfile:
    {{ PODMAN }} build -f {{ bundleDockerfile }} -t {{ BUNDLE_IMAGE }} .
    {{ PODMAN }} push {{ BUNDLE_IMAGE }}

# Deploy CRDs and run operator locally for development
[group('developer')]
run-local:
    hack/run-local.sh

# Run end-to-end tests with optional focus filter
[group('test')]
test-e2e e2e-args="-ginkgo.v" focus="":
    #!/usr/bin/env bash

    set -eoux pipefail

    args=("{{ e2e-args }}")
    if [[ -n "{{ focus }}" ]]; then
      args+=("-ginkgo.focus={{ focus }}")
    fi

    echo "=== Running e2e tests ==="
    GOFLAGS=-mod=vendor go test ./test/e2e -v -count=1 -args ${args[@]}

# Deploy all the pre-req operators, das-operator and execute end-to-end tests on CI
[group('test')]
test-e2e-ci: create-related-images deploy-cert-manager-ocp deploy-nfd-ocp deploy-nvidia-ocp deploy-das-ocp test-e2e

# Create a related_images.json file with the provided env variables
[group('generate')]
create-related-images filename="related_images.dev.json":
    #!/usr/bin/env bash

    echo "creating {{ filename }}"
    cat <<EOF > {{ filename }}
    [
      {"name": "instaslice-operator-next", "image": "{{ OPERATOR_IMAGE }}"},
      {"name": "instaslice-webhook-next", "image": "{{ WEBHOOK_IMAGE }}"},
      {"name": "instaslice-scheduler-next", "image": "{{ SCHEDULER_IMAGE }}"},
      {"name": "instaslice-daemonset-next", "image": "{{ DAEMONSET_IMAGE }}"}
    ]
    EOF

    cat {{ filename }}

# Remove NVIDIA GPU operator from OpenShift
[group('deploy')]
undeploy-nvidia-ocp:
    {{ KUBECTL }} delete -f hack/manifests/gpu-cluster-policy.yaml
    {{ KUBECTL }} delete -f hack/manifests/nvidia-cpu-operator.yaml

# Deploy NVIDIA GPU operator to OpenShift
[group('deploy')]
deploy-nvidia-ocp:
    hack/deploy-nvidia.sh

# Clean up all deployed Kubernetes resources
[group('deploy')]
undeploy:
    @echo "=== Deleting K8s resources ==="
    {{ KUBECTL }} delete --ignore-not-found --wait=true -f {{ DEPLOY_DIR }}/
    # Wait for the operator namespace to be fully removed
    {{ KUBECTL }} wait --for=delete namespace/das-operator --timeout=120s || true

# Deploy cert-manager for Kubernetes
[group('deploy')]
deploy-cert-manager:
    hack/deploy-cert-manager.sh

# Deploy cert-manager for OpenShift
[group('deploy')]
deploy-cert-manager-ocp:
    {{ KUBECTL }} apply -f hack/manifests/cert-manager-rh.yaml

# Remove cert-manager from OpenShift
[group('deploy')]
undeploy-cert-manager-ocp:
    {{ KUBECTL }} delete -f hack/manifests/cert-manager-rh.yaml

# Deploy Node Feature Discovery (NFD) operator for OpenShift
[group('deploy')]
deploy-nfd-ocp:
    hack/deploy-nfd.sh

# Run golangci-lint on the codebase
[group('lint')]
lint-go:
    {{ GOLANGCI_LINT }} run --timeout 5m ./pkg/...

# Run golangci-lint and automatically fix issues
[group('lint')]
lint-go-fix: lint-go
    {{ GOLANGCI_LINT }} run --fix

# Run markdownlint on markdown files
[group('lint')]
lint-md:
    {{ MARKDOWNLINT }} -c .markdownlint.json *.md

# Run markdownlint and automatically fix issues
[group('lint')]
lint-md-fix:
    {{ MARKDOWNLINT }} -c .markdownlint.json --fix *.md

# Run all linting fixes (markdown and Go)
[group('lint')]
lint-fix: lint-md-fix lint-go-fix lint-sh-fix lint-just-fix

# Run shfmt and automatically fix issues
[group('lint')]
lint-sh:
    {{ SHFMT }} -d hack/

# Run shfmt and automatically fix issues
[group('lint')]
lint-sh-fix:
    {{ SHFMT }} -w hack/

# Run just format
[group('lint')]
lint-just:
    @just --fmt --check --unstable

# Run just format
[group('lint')]
lint-just-fix:
    @just --fmt --unstable

# Run all linting (markdown and Go)
[group('lint')]
lint: lint-md lint-go lint-sh lint-just
