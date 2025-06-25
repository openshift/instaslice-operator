all: build
.PHONY: all
SHELL := /bin/bash

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
GOLANGCI_LINT_VERSION ?= v2.1.6
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
	podman build -f Dockerfile.ocp -t ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG} .
	podman build -f Dockerfile.scheduler.ocp -t ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG} .
	podman build -f Dockerfile.daemonset.ocp -t ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG} .
	podman build -f Dockerfile.webhook.ocp -t ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG} .

build-push-images:
	podman push ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG}
	podman push ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG}
	podman push ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG}
	podman push ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG}

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
	# docker build -f Dockerfile.scheduler.ocp -t ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG} .
	# docker push ${IMAGE_REGISTRY}/das-scheduler:${IMAGE_TAG}

build-push-daemonset:
	# docker build -f Dockerfile.daemonset.ocp -t ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG} .
	# docker push ${IMAGE_REGISTRY}/das-daemonset:${IMAGE_TAG}

build-push-operator:
	# docker build -f Dockerfile.ocp -t ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG} .
	# docker push ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG}

build-push-webhook:
	# docker build -f Dockerfile.webhook.ocp -t ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG} .
	# docker push ${IMAGE_REGISTRY}/das-webhook:${IMAGE_TAG}

.PHONY: test-k8s
test-k8s:
	kubectl label node $$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}') \
		nvidia.com/mig.capable=true --overwrite

	@echo "=== Building and pushing images in parallel ==="
	$(MAKE) -j16 build-push-scheduler build-push-daemonset build-push-operator build-push-webhook

	@echo "=== All images built & pushed ==="

	@echo "=== Deploying Cert Manager ==="
	$(MAKE) deploy-cert-manager

	@echo "=== Generating CRDs for K8s ==="
	$(MAKE) regen-crd-k8s

	@echo "=== Applying K8s CRDs ==="
	       kubectl apply -f $(DEPLOY_DIR)/00_instaslice-operator.crd.yaml \
                      -f $(DEPLOY_DIR)/00_nodeaccelerators.crd.yaml

	@echo "=== Waiting for CRDs to be established ==="
	kubectl wait --for=condition=established --timeout=60s \
                     crd dasoperators.inference.redhat.com

	@echo "=== Applying K8s core manifests ==="
	@echo "=== Setting emulatedMode to $(EMULATED_MODE) in CR ==="
	TMP_DIR=$$(mktemp -d); \
	cp $(DEPLOY_DIR)/*.yaml $$TMP_DIR/; \
       sed -i 's/emulatedMode: .*/emulatedMode: "$(EMULATED_MODE)"/' $$TMP_DIR/03_instaslice_operator.cr.yaml; \
       env IMAGE_REGISTRY=$(IMAGE_REGISTRY) IMAGE_TAG=$(IMAGE_TAG) envsubst < $(DEPLOY_DIR)/04_deployment.yaml > $$TMP_DIR/04_deployment.yaml; \
       env IMAGE_REGISTRY=$(IMAGE_REGISTRY) IMAGE_TAG=$(IMAGE_TAG) envsubst < $(DEPLOY_DIR)/05_scheduler_deployment.yaml > $$TMP_DIR/05_scheduler_deployment.yaml; \
       kubectl apply -f $$TMP_DIR/; \
       kubectl apply -f $$TMP_DIR/05_scheduler_deployment.yaml

.PHONY: emulated-k8s
emulated-k8s: EMULATED_MODE=enabled
emulated-k8s: test-k8s

.PHONY: cleanup-k8s
cleanup-k8s:
	@echo "=== Deleting K8s resources ==="
	kubectl delete -f $(DEPLOY_DIR)/

.PHONY: test-ocp
test-ocp:
	kubectl label node $$(kubectl get nodes -l node-role.kubernetes.io/worker \
                                -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}') \
        nvidia.com/mig.capable=true --overwrite

	@echo "=== Building and pushing images in parallel ==="
	$(MAKE) -j16 build-push-scheduler build-push-daemonset build-push-operator build-push-webhook

	@echo "=== Allowing quay to refresh the images ==="
	sleep 15

	@echo "=== All images built & pushed ==="

	@echo "=== Generating CRDs for K8s ==="
	$(MAKE) regen-crd-k8s

	@echo "=== Applying K8s CRDs ==="
	       kubectl apply -f $(DEPLOY_DIR)/00_instaslice-operator.crd.yaml \
                      -f $(DEPLOY_DIR)/00_nodeaccelerators.crd.yaml

	@echo "=== Waiting for CRDs to be established ==="
	kubectl wait --for=condition=established --timeout=60s \
                     crd dasoperators.inference.redhat.com

	@echo "=== Applying K8s core manifests ==="
	@echo "=== Setting emulatedMode to $(EMULATED_MODE) in CR ==="
	TMP_DIR=$$(mktemp -d); \
	cp $(DEPLOY_DIR)/*.yaml $$TMP_DIR/; \
       sed -i 's/emulatedMode: .*/emulatedMode: "$(EMULATED_MODE)"/' $$TMP_DIR/03_instaslice_operator.cr.yaml; \
       env IMAGE_REGISTRY=$(IMAGE_REGISTRY) IMAGE_TAG=$(IMAGE_TAG) envsubst < $(DEPLOY_DIR)/04_deployment.yaml > $$TMP_DIR/04_deployment.yaml; \
       env IMAGE_REGISTRY=$(IMAGE_REGISTRY) IMAGE_TAG=$(IMAGE_TAG) envsubst < $(DEPLOY_DIR)/05_scheduler_deployment.yaml > $$TMP_DIR/05_scheduler_deployment.yaml; \
       kubectl apply -f $$TMP_DIR/; \
       kubectl apply -f $$TMP_DIR/05_scheduler_deployment.yaml

.PHONY: emulated-ocp
emulated-ocp: EMULATED_MODE=enabled
emulated-ocp: test-ocp

.PHONY: gpu-ocp
gpu-ocp: EMULATED_MODE=disabled
gpu-ocp: test-ocp

.PHONY: cleanup-ocp
cleanup-ocp:
	@echo "=== Deleting OCP resources ==="
	kubectl delete -f $(DEPLOY_DIR)/


.PHONY: deploy-cert-manager
deploy-cert-manager:
	export KUBECTL=$(KUBECTL) IMG=$(IMG) IMG_DMST=$(IMG_DMST) && \
		hack/deploy-cert-manager.sh


TEST_E2E_ARGS := -ginkgo.v
ifdef FOCUS
TEST_E2E_ARGS += -ginkgo.focus=$(FOCUS)
endif

.PHONY: test-e2e
test-e2e:
	@echo "=== Running e2e tests ==="
	GOFLAGS=-mod=vendor go test ./test/e2e -v -count=1 -args $(TEST_E2E_ARGS)
