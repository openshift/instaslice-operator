all: build
.PHONY: all
SHELL := /usr/bin/env bash

SOURCE_GIT_TAG ?=$(shell git describe --long --tags --abbrev=7 --match 'v[0-9]*' || echo 'v1.0.0-$(SOURCE_GIT_COMMIT)')
SOURCE_GIT_COMMIT ?=$(shell git rev-parse --short "HEAD^{commit}" 2>/dev/null)
IMAGE_TAG ?= latest
OPERATOR_VERSION ?= 0.1.0
DEPLOY_DIR ?= deploy

# OS_GIT_VERSION is populated by ART
# If building out of the ART pipeline, fallback to SOURCE_GIT_TAG
ifndef OS_GIT_VERSION
	OS_GIT_VERSION = $(SOURCE_GIT_TAG)
endif

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/build-machinery-go/make/, \
	golang.mk \
	targets/openshift/images.mk \
	targets/openshift/deps.mk \
	targets/openshift/crd-schema-gen.mk \
)

# Exclude e2e tests from unit testing
GO_TEST_PACKAGES :=./pkg/... ./cmd/...
GO_BUILD_FLAGS :=-tags strictfipsruntime

IMAGE_REGISTRY ?= quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant
EMULATED_MODE ?= disabled
PODMAN ?= podman
KUBECTL ?= oc
BUNDLE_IMAGE ?= mustchange
LOCALBIN ?= $(shell pwd)/bin
OPERATOR_SDK_VERSION ?= v1.40.0
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk

# This will call a macro called "build-image" which will generate image specific targets based on the parameters:
# $0 - macro name
# $1 - target name
# $2 - image ref
# $3 - Dockerfile path
# $4 - context directory for image build
ifdef OSS
$(call build-image,instaslice-operator,$(IMAGE_REGISTRY)/instaslice-operator:$(IMAGE_TAG), ./Dockerfile,.)
$(call build-image,das-daemonset,$(IMAGE_REGISTRY)/das-daemonset:$(IMAGE_TAG), ./Dockerfile.daemonset,.)
$(call build-image,das-webhook,$(IMAGE_REGISTRY)/das-webhook:$(IMAGE_TAG), ./Dockerfile.webhook,.)

$(call verify-golang-versions,Dockerfile)
$(call verify-golang-versions,Dockerfile.daemonset)
else
$(call build-image,instaslice-operator,$(IMAGE_REGISTRY)/instaslice-operator:$(IMAGE_TAG), ./Dockerfile.ocp,.)
$(call build-image,das-daemonset,$(IMAGE_REGISTRY)/das-daemonset:$(IMAGE_TAG), ./Dockerfile.daemonset.ocp,.)
$(call build-image,das-webhook,$(IMAGE_REGISTRY)/das-webhook:$(IMAGE_TAG), ./Dockerfile.webhook.ocp,.)

$(call verify-golang-versions,Dockerfile.ocp)
$(call verify-golang-versions,Dockerfile.daemonset.ocp)
endif

GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.2.1
golangci-lint:
	@[ -f $(GOLANGCI_LINT) ] || { \
	set -e ;\
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell dirname $(GOLANGCI_LINT)) $(GOLANGCI_LINT_VERSION) ;\
	}

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter & yamllint
	$(GOLANGCI_LINT) run --timeout 5m ./pkg/...

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

regen-crd:
	go build -o _output/tools/bin/controller-gen ./vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
	rm -f manifests/instaslice-operator.crd.yaml
	./_output/tools/bin/controller-gen crd paths=./pkg/apis/dasoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./manifests
	mv manifests/inference.redhat.com_dasoperators.yaml manifests/instaslice-operator.crd.yaml
	cp manifests/instaslice-operator.crd.yaml $(DEPLOY_DIR)/00_instaslice-operator.crd.yaml
	cp manifests/inference.redhat.com_nodeaccelerators.yaml $(DEPLOY_DIR)/00_nodeaccelerators.crd.yaml

.PHONY: regen-crd-k8s
regen-crd-k8s:
	@echo "Generating CRDs into deploy directory"
	go build -o _output/tools/bin/controller-gen ./vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
	rm -f $(DEPLOY_DIR)/00_instaslice-operator.crd.yaml
	rm -f $(DEPLOY_DIR)/00_nodeaccelerators.crd.yaml
	./_output/tools/bin/controller-gen crd paths=./pkg/apis/dasoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./$(DEPLOY_DIR)
	mv $(DEPLOY_DIR)/inference.redhat.com_dasoperators.yaml $(DEPLOY_DIR)/00_instaslice-operator.crd.yaml
	mv $(DEPLOY_DIR)/inference.redhat.com_nodeaccelerators.yaml $(DEPLOY_DIR)/00_nodeaccelerators.crd.yaml

build-images:
	${PODMAN} build -f Dockerfile.ocp -t ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG} .
	${PODMAN} build -f Dockerfile.scheduler.ocp -t ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG} .
	${PODMAN} build -f Dockerfile.daemonset.ocp -t ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG} .
	${PODMAN} build -f Dockerfile.webhook.ocp -t ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG} .

build-push-images:
	${PODMAN} push ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG}
	${PODMAN} push ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG}
	${PODMAN} push ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG}
	${PODMAN} push ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG}

generate: regen-crd regen-crd-k8s generate-clients
.PHONY: generate

generate-clients:
	GO=GO111MODULE=on GOFLAGS=-mod=readonly hack/update-codegen.sh
.PHONY: generate-clients

verify-codegen:
	hack/verify-codegen.sh
.PHONY: verify-codegen

clean:
	$(RM) -r ./_tmp
.PHONY: clean


.PHONY: build-push-scheduler build-push-daemonset build-push-operator build-push-webhook

build-push-scheduler:
	${PODMAN} build -f Dockerfile.scheduler.ocp -t ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG} .
	${PODMAN} push ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG}

build-push-daemonset:
	${PODMAN} build -f Dockerfile.daemonset.ocp -t ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG} .
	${PODMAN} push ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG}

build-push-operator:
	${PODMAN} build -f Dockerfile.ocp -t ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG} .
	${PODMAN} push ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG}

build-push-webhook:
	${PODMAN} build -f Dockerfile.webhook.ocp -t ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG} .
	${PODMAN} push ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG}

.PHONY: deploy-das-k8s
deploy-das-k8s:
	${KUBECTL} label node $$(${KUBECTL} get nodes -o jsonpath='{.items[*].metadata.name}') \
		nvidia.com/mig.capable=true --overwrite

	@echo "=== Deploying Cert Manager ==="
	$(MAKE) deploy-cert-manager

	@echo "=== Generating CRDs for K8s ==="
	$(MAKE) regen-crd-k8s

	@echo "=== Applying K8s CRDs ==="
	       ${KUBECTL} apply -f $(DEPLOY_DIR)/00_instaslice-operator.crd.yaml \
                      -f $(DEPLOY_DIR)/00_nodeaccelerators.crd.yaml

	@echo "=== Waiting for CRDs to be established ==="
	${KUBECTL} wait --for=condition=established --timeout=60s \
                     crd dasoperators.inference.redhat.com

	@echo "=== Applying K8s core manifests ==="
	@echo "=== Setting emulatedMode to $(EMULATED_MODE) in CR ==="
	TMP_DIR=$$(mktemp -d); \
	cp $(DEPLOY_DIR)/*.yaml $$TMP_DIR/; \
       sed -i 's/emulatedMode: .*/emulatedMode: "$(EMULATED_MODE)"/' $$TMP_DIR/03_instaslice_operator.cr.yaml; \
       env IMAGE_REGISTRY=$(IMAGE_REGISTRY) IMAGE_TAG=$(IMAGE_TAG) envsubst < $(DEPLOY_DIR)/04_deployment.yaml > $$TMP_DIR/04_deployment.yaml; \
       ${KUBECTL} apply -f $$TMP_DIR/

.PHONY: emulated-k8s
emulated-k8s: EMULATED_MODE=enabled
emulated-k8s: build-push-images-parallel deploy-das-k8s

.PHONY: cleanup-k8s
cleanup-k8s:
	@echo "=== Deleting K8s resources ==="
	${KUBECTL} delete --ignore-not-found --wait=true -f $(DEPLOY_DIR)/
	# Wait for the operator namespace to be fully removed
	${KUBECTL} wait --for=delete namespace/das-operator --timeout=120s || true

.PHONY: deploy-das-ocp
deploy-das-ocp:
	${KUBECTL} label node $$(${KUBECTL} get nodes -l node-role.kubernetes.io/worker \
                                -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}') \
        nvidia.com/mig.capable=true --overwrite
	@echo "=== Generating CRDs for K8s ==="
	$(MAKE) regen-crd-k8s

	@echo "=== Applying K8s CRDs ==="
	       ${KUBECTL} apply -f $(DEPLOY_DIR)/00_instaslice-operator.crd.yaml \
                      -f $(DEPLOY_DIR)/00_nodeaccelerators.crd.yaml

	@echo "=== Waiting for CRDs to be established ==="
	${KUBECTL} wait --for=condition=established --timeout=60s \
                     crd dasoperators.inference.redhat.com

	@echo "=== Applying K8s core manifests ==="
	@echo "=== Setting emulatedMode to $(EMULATED_MODE) in CR ==="
	TMP_DIR=$$(mktemp -d); \
	cp $(DEPLOY_DIR)/*.yaml $$TMP_DIR/; \
       sed -i 's/emulatedMode: .*/emulatedMode: "$(EMULATED_MODE)"/' $$TMP_DIR/03_instaslice_operator.cr.yaml; \
       env IMAGE_REGISTRY=$(IMAGE_REGISTRY) IMAGE_TAG=$(IMAGE_TAG) envsubst < $(DEPLOY_DIR)/04_deployment.yaml > $$TMP_DIR/04_deployment.yaml; \
       ${KUBECTL} apply -f $$TMP_DIR/

.PHONY: emulated-ocp
emulated-ocp: EMULATED_MODE=enabled
emulated-ocp: deploy-das-ocp

.PHONY: build-push-images-parallel
build-push-images-parallel:
	@echo "=== Building and pushing images in parallel ==="
	$(MAKE) -j16 build-push-scheduler build-push-daemonset build-push-operator build-push-webhook
	@echo "=== Allowing quay to refresh the images ==="
	sleep 15
	@echo "=== All images built & pushed ==="

.PHONY: gpu-ocp
gpu-ocp: EMULATED_MODE=disabled
gpu-ocp: build-push-images-parallel deploy-das-ocp

.PHONY: cleanup-ocp
cleanup-ocp:
	@echo "=== Deleting OCP resources ==="
	${KUBECTL} delete --ignore-not-found --wait=true -f $(DEPLOY_DIR)/
	# Wait for the operator namespace to be fully removed
	${KUBECTL} wait --for=delete namespace/das-operator --timeout=120s || true

.PHONY: deploy-cert-manager
deploy-cert-manager:
	export KUBECTL=$(KUBECTL) IMG=$(IMG) IMG_DMST=$(IMG_DMST) && \
		hack/deploy-cert-manager.sh

.PHONY: deploy-cert-manager-ocp
deploy-cert-manager-ocp:
	${KUBECTL} apply -f hack/manifests/cert-manager-rh.yaml

.PHONY: undeploy-cert-manager-ocp
undeploy-cert-manager-ocp:
	${KUBECTL} delete -f hack/manifests/cert-manager-rh.yaml

.PHONY: deploy-nfd-ocp
deploy-nfd-ocp:
	hack/deploy-nfd.sh

.PHONY: undeploy-nfd-ocp
undeploy-nfd-ocp:
	${KUBECTL} delete -f hack/manifests/nfd-instance.yaml
	${KUBECTL} delete -f hack/manifests/nfd.yaml

.PHONY: deploy-nvidia-ocp
deploy-nvidia-ocp:
	hack/deploy-nvidia.sh

.PHONY: undeploy-nvidia-ocp
undeploy-nvidia-ocp:
	${KUBECTL} delete -f hack/manifests/gpu-cluster-policy.yaml
	${KUBECTL} delete -f hack/manifests/nvidia-cpu-operator.yaml

TEST_E2E_ARGS := -ginkgo.v
ifdef FOCUS
TEST_E2E_ARGS += -ginkgo.focus=$(FOCUS)
endif

.PHONY: test-e2e
test-e2e:
	@echo "=== Running e2e tests ==="
	GOFLAGS=-mod=vendor go test ./test/e2e -v -count=1 -args $(TEST_E2E_ARGS)

.PHONY: operator-sdk
operator-sdk:
	@[ -f $(OPERATOR_SDK) ] || { \
	set -e ;\
	mkdir -p $(dir $(OPERATOR_SDK)) ;\
	OS=$(shell go env GOOS) && ARCH=$(shell go env GOARCH) && \
	curl -sSLo $(OPERATOR_SDK) https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$${OS}_$${ARCH};\
	chmod +x $(OPERATOR_SDK) ;\
	}

.PHONY: bundle-generate
bundle-generate: operator-sdk
	$(OPERATOR_SDK) generate bundle --input-dir $(DEPLOY_DIR)/ --version $(OPERATOR_VERSION) --output-dir=bundle-ocp --package das-operator

.PHONY: bundle-build
bundle-build: bundle-generate
	$(PODMAN) build -f bundle-ocp.Dockerfile -t $(BUNDLE_IMAGE) .

.PHONY: bundle-push
bundle-push:
	$(PODMAN) push $(BUNDLE_IMAGE)


run-local: export DAEMONSET_IMAGE="${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG}"
run-local: export WEBHOOK_IMAGE="${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG}"
run-local: export SCHEDULER_IMAGE="${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG}"
run-local:
	${KUBECTL} apply -f deploy/00_instaslice-operator.crd.yaml
	${KUBECTL} apply -f deploy/00_instaslice-operator.crd.yaml
	${KUBECTL} apply -f deploy/01_namespace.yaml
	${KUBECTL} apply -f deploy/01_operator_sa.yaml
	${KUBECTL} apply -f deploy/02_privileged_scc_binding.yaml
	${KUBECTL} apply -f deploy/03_instaslice_operator.cr.yaml
	RELATED_IMAGE_DAEMONSET_IMAGE=${DAEMONSET_IMAGE} RELATED_IMAGE_WEBHOOK_IMAGE=${WEBHOOK_IMAGE} RELATED_IMAGE_SCHEDULER_IMAGE=${SCHEDULER_IMAGE} go run cmd/das-operator/main.go operator --kubeconfig=${KUBECONFIG} --namespace=das-operator
