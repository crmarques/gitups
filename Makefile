SHELL := /usr/bin/env bash

.DEFAULT_GOAL := help
.DELETE_ON_ERROR:

APP_NAME := gitups
CMD_DIR := ./cmd/$(APP_NAME)
BIN_DIR := $(CURDIR)/bin
BIN := $(BIN_DIR)/$(APP_NAME)
COVERAGE_DIR := $(CURDIR)/coverage
COVERAGE_FILE := $(COVERAGE_DIR)/coverage.out

GO ?= go
GOFLAGS ?=
GOOS ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)
CGO_ENABLED ?= 0

.PHONY: help all build install run test test-race coverage fmt vet tidy deps check e2e clean

help: ## Show available targets.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: check build ## Run verification and build ./bin/gitups.

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

build: $(BIN_DIR) ## Build the gitups CLI into ./bin/gitups.
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build $(GOFLAGS) -trimpath -o $(BIN) $(CMD_DIR)

install: build ## Alias for build; the local install path is ./bin/gitups.
	@printf 'gitups built at %s\n' "$(BIN)"

run: build ## Build and run ./bin/gitups. Pass CLI args with ARGS='...'.
	$(BIN) $(ARGS)

test: ## Run unit tests.
	$(GO) test $(GOFLAGS) ./...

test-race: ## Run unit tests with the race detector.
	$(GO) test $(GOFLAGS) -race ./...

coverage: $(COVERAGE_DIR) ## Run tests with coverage and print function coverage.
	$(GO) test $(GOFLAGS) -coverprofile=$(COVERAGE_FILE) ./...
	$(GO) tool cover -func=$(COVERAGE_FILE)

$(COVERAGE_DIR):
	mkdir -p $(COVERAGE_DIR)

fmt: ## Format Go source.
	$(GO) fmt ./...

vet: ## Run go vet.
	$(GO) vet $(GOFLAGS) ./...

tidy: ## Normalize go.mod and go.sum.
	$(GO) mod tidy

deps: ## Download module dependencies.
	$(GO) mod download

check: fmt vet test ## Format, vet, and test.

e2e: build ## Run the dsv end-to-end flow. Override KUBE_CONTEXT=kind-dsv if needed.
	tests/e2e/run.sh dsv "$${KUBE_CONTEXT:-kind-dsv}"

clean: ## Remove generated local build and coverage outputs.
	rm -f $(BIN) $(BIN).exe
	rm -rf $(COVERAGE_DIR)
