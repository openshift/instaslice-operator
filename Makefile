SHELL := /usr/bin/env bash

all: build
.PHONY: all

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

# Clean all binaries
clean:
	$(RM) -r ./_tmp
.PHONY: clean

