all: build
.PHONY: all

SOURCE_GIT_TAG ?=$(shell git describe --long --tags --abbrev=7 --match 'v[0-9]*' || echo 'v1.0.0-$(SOURCE_GIT_COMMIT)')
SOURCE_GIT_COMMIT ?=$(shell git rev-parse --short "HEAD^{commit}" 2>/dev/null)
IMAGE_TAG ?= latest
OPERATOR_VERSION ?= 0.1.0

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

# This will call a macro called "build-image" which will generate image specific targets based on the parameters:
# $0 - macro name
# $1 - target name
# $2 - image ref
# $3 - Dockerfile path
# $4 - context directory for image build
ifdef OSS
$(call build-image,instaslice-operator,$(IMAGE_REGISTRY)/instaslice-operator:$(IMAGE_TAG), ./Dockerfile,.)
$(call build-image,instaslice-daemonset,$(IMAGE_REGISTRY)/instaslice-daemonset:$(IMAGE_TAG), ./Dockerfile.daemonset,.)
$(call build-image,instaslice-webhook,$(IMAGE_REGISTRY)/instaslice-webhook:$(IMAGE_TAG), ./Dockerfile.webhook,.)

$(call verify-golang-versions,Dockerfile)
$(call verify-golang-versions,Dockerfile.daemonset)
else
$(call build-image,instaslice-operator,$(IMAGE_REGISTRY)/instaslice-operator:$(IMAGE_TAG), ./Dockerfile.ocp,.)
$(call build-image,instaslice-daemonset,$(IMAGE_REGISTRY)/instaslice-daemonset:$(IMAGE_TAG), ./Dockerfile.daemonset.ocp,.)
$(call build-image,instaslice-webhook,$(IMAGE_REGISTRY)/instaslice-webhook:$(IMAGE_TAG), ./Dockerfile.webhook.ocp,.)

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
	./_output/tools/bin/controller-gen crd paths=./pkg/apis/instasliceoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./manifests
	mv manifests/inference.redhat.com_instasliceoperators.yaml manifests/instaslice-operator.crd.yaml
	cp manifests/instaslice-operator.crd.yaml deploy/00_instaslice-operator.crd.yaml
	cp manifests/inference.redhat.com_instaslices.yaml deploy/00_instaslices.crd.yaml

.PHONY: regen-crd-kind
regen-crd-kind:
	@echo "Generating CRDs into deploy-kind directory"
	go build -o _output/tools/bin/controller-gen ./vendor/sigs.k8s.io/controller-tools/cmd/controller-gen
	rm -f deploy-kind/00_instaslice-operator.crd.yaml
	rm -f deploy-kind/00_instaslices.crd.yaml
	./_output/tools/bin/controller-gen crd paths=./pkg/apis/instasliceoperator/v1alpha1/... schemapatch:manifests=./manifests output:crd:dir=./deploy-kind
	mv deploy-kind/inference.redhat.com_instasliceoperators.yaml deploy-kind/00_instaslice-operator.crd.yaml
	mv deploy-kind/inference.redhat.com_instaslices.yaml deploy-kind/00_instaslices.crd.yaml

build-images:
	podman build -f Dockerfile.ocp -t ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG} .
	podman build -f Dockerfile.scheduler.ocp -t ${IMAGE_REGISTRY}/instaslice-scheduler:${IMAGE_TAG} .
	podman build -f Dockerfile.daemonset.ocp -t ${IMAGE_REGISTRY}/instaslice-daemonset:${IMAGE_TAG} .
	podman build -f Dockerfile.webhook.ocp -t ${IMAGE_REGISTRY}/instaslice-webhook:${IMAGE_TAG} .

build-push-images:
	podman push ${IMAGE_REGISTRY}/instaslice-operator:${IMAGE_TAG}
	podman push ${IMAGE_REGISTRY}/instaslice-scheduler:${IMAGE_TAG}
	podman push ${IMAGE_REGISTRY}/instaslice-daemonset:${IMAGE_TAG}
	podman push ${IMAGE_REGISTRY}/instaslice-webhook:${IMAGE_TAG}

generate: regen-crd generate-clients
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

## test-kind: quick smoke-test on a Kind cluster
.PHONY: test-kind
test-kind:
	@echo "=== Creating Kind cluster 'instaslice-test' ==="
	kind create cluster --name instaslice-test

	kubectl label node $$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}') nvidia.com/mig.capable=true --overwrite

	# @echo "=== Building container images ==="
	# docker build -f Dockerfile.scheduler.ocp -t instaslice-scheduler:dev .
	# docker build -f Dockerfile.daemonset.ocp -t instaslice-daemonset:dev .
	# docker build -f Dockerfile.ocp -t instaslice-operator:dev .
	# docker build -f Dockerfile.webhook.ocp -t instaslice-webhook:dev .

	@echo "=== Loading images into Kind ==="
	kind load docker-image instaslice-scheduler:dev --name instaslice-test
	kind load docker-image instaslice-daemonset:dev --name instaslice-test
	kind load docker-image instaslice-operator:dev --name instaslice-test
	kind load docker-image instaslice-webhook:dev --name instaslice-test

	@echo "=== Deploying Cert Manager ==="
	$(MAKE) deploy-cert-manager
	@echo "=== Generating CRDs for Kind ==="
	$(MAKE) regen-crd-kind


	@echo "=== Applying Kind CRDs ==="
	kubectl apply \
 	-f deploy-kind/00_instaslice-operator.crd.yaml \
 	-f deploy-kind/00_instaslices.crd.yaml

	@echo "=== Waiting for CRDs to be established ==="
	kubectl wait --for=condition=established --timeout=60s crd instasliceoperators.inference.redhat.com

	@echo "=== Applying Kind core manifests ==="
	kubectl apply \
	  -f deploy-kind/01_namespace.yaml \
	  -f deploy-kind/02_00_operand_clusterrole.yaml \
	  -f deploy-kind/02_01_operand_clusterrolebinding.yaml \
	  -f deploy-kind/02_02_operand_role.yaml \
	  -f deploy-kind/02_03_operand_rolebinding.yaml \
	  -f deploy-kind/02_04_daemonset_rolebinding.yaml \
	  -f deploy-kind/04_01_clusterrole.yaml \
	  -f deploy-kind/04_02_clusterrolebinding.yaml \
	  -f deploy-kind/04_serviceaccount.yaml \
	  -f deploy-kind/05_deployment.yaml \
	  -f deploy-kind/05_default_sa_rbac.yaml \
	  -f deploy-kind/09_instaslice_operator.cr.yaml \
	  -f deploy-kind/05_06_scheduler_core_rbac.yaml


	@echo "=== Deploying instaslice-scheduler ==="
	kubectl apply -f deploy-kind/06_scheduler_deployment.yaml

	@echo "=== Deploying test pod ==="
	kubectl apply -f deploy-kind/07_test_pod.yaml

.PHONY: cleanup-kind
cleanup-kind:
	@echo "=== Deleting Kind cluster 'instaslice-test' ==="
	kind delete cluster --name instaslice-test

.PHONY: deploy-cert-manager
deploy-cert-manager:
	export KUBECTL=$(KUBECTL) IMG=$(IMG) IMG_DMST=$(IMG_DMST) && \
                hack/deploy-cert-manager.sh