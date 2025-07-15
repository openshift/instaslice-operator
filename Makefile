all: build
.PHONY: all
SHELL := /usr/bin/env bash
DEPLOY_DIR ?= deploy

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

