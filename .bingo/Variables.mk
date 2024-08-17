# Auto generated binary variables helper managed by https://github.com/bwplotka/bingo v0.9. DO NOT EDIT.
# All tools are designed to be build inside $GOBIN.
BINGO_DIR := $(dir $(lastword $(MAKEFILE_LIST)))
GOPATH ?= $(shell go env GOPATH)
GOBIN  ?= $(firstword $(subst :, ,${GOPATH}))/bin
GO     ?= $(shell which go)

# Below generated variables ensure that every time a tool under each variable is invoked, the correct version
# will be used; reinstalling only if needed.
# For example for bingo variable:
#
# In your main Makefile (for non array binaries):
#
#include .bingo/Variables.mk # Assuming -dir was set to .bingo .
#
#command: $(BINGO)
#	@echo "Running bingo"
#	@$(BINGO) <flags/args..>
#
BINGO := $(GOBIN)/bingo-v0.9.0
$(BINGO): $(BINGO_DIR)/bingo.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/bingo-v0.9.0"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=bingo.mod -o=$(GOBIN)/bingo-v0.9.0 "github.com/bwplotka/bingo"

CONTROLLER_GEN := $(GOBIN)/controller-gen-v0.14.0
$(CONTROLLER_GEN): $(BINGO_DIR)/controller-gen.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/controller-gen-v0.14.0"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=controller-gen.mod -o=$(GOBIN)/controller-gen-v0.14.0 "sigs.k8s.io/controller-tools/cmd/controller-gen"

GOLANGCI_LINT := $(GOBIN)/golangci-lint-v1.54.2
$(GOLANGCI_LINT): $(BINGO_DIR)/golangci-lint.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/golangci-lint-v1.54.2"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=golangci-lint.mod -o=$(GOBIN)/golangci-lint-v1.54.2 "github.com/golangci/golangci-lint/cmd/golangci-lint"

KUSTOMIZE := $(GOBIN)/kustomize-v5.3.0
$(KUSTOMIZE): $(BINGO_DIR)/kustomize.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/kustomize-v5.3.0"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=kustomize.mod -o=$(GOBIN)/kustomize-v5.3.0 "sigs.k8s.io/kustomize/kustomize/v5"

OPERATOR_SDK := $(GOBIN)/operator-sdk-v1.34.1
$(OPERATOR_SDK): $(BINGO_DIR)/operator-sdk.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/operator-sdk-v1.34.1"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=operator-sdk.mod -o=$(GOBIN)/operator-sdk-v1.34.1 "github.com/operator-framework/operator-sdk/cmd/operator-sdk"

OPM := $(GOBIN)/opm-v1.23.0
$(OPM): $(BINGO_DIR)/opm.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/opm-v1.23.0"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=opm.mod -o=$(GOBIN)/opm-v1.23.0 "github.com/operator-framework/operator-registry/cmd/opm"

SETUP_ENVTEST := $(GOBIN)/setup-envtest-v0.0.0-20240903085516-38546806f2fa
$(SETUP_ENVTEST): $(BINGO_DIR)/setup-envtest.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/setup-envtest-v0.0.0-20240903085516-38546806f2fa"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=setup-envtest.mod -o=$(GOBIN)/setup-envtest-v0.0.0-20240903085516-38546806f2fa "sigs.k8s.io/controller-runtime/tools/setup-envtest"

