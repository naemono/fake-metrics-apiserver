REGISTRY?=docker.io/mmontg1
IMAGE?=custom-metrics-apiserver
TEMP_DIR:=$(shell mktemp -d)
ARCH?=amd64
OS?=linux
OUT_DIR?=./_output
GOPATH:=$(shell go env GOPATH)

VERSION ?= $(shell git rev-parse --short=8 --verify HEAD)

GOLANGCI_VERSION:=1.52.2

.PHONY: all
all: build-fake-adapter

APISERVER_IMAGE ?= $(REGISTRY)/$(IMAGE):$(VERSION)-$(ARCH)

# Generate
# --------

generated_openapis := core custommetrics externalmetrics
generated_files := $(generated_openapis:%=pkg/generated/openapi/%/zz_generated.openapi.go)

pkg/generated/openapi/core/zz_generated.openapi.go: INPUTS := k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/api/resource,k8s.io/apimachinery/pkg/version,k8s.io/api/core/v1
pkg/generated/openapi/custommetrics/zz_generated.openapi.go: INPUTS := k8s.io/metrics/pkg/apis/custom_metrics,k8s.io/metrics/pkg/apis/custom_metrics/v1beta1,k8s.io/metrics/pkg/apis/custom_metrics/v1beta2
pkg/generated/openapi/externalmetrics/zz_generated.openapi.go: INPUTS := k8s.io/metrics/pkg/apis/external_metrics,k8s.io/metrics/pkg/apis/external_metrics/v1beta1

pkg/generated/openapi/%/zz_generated.openapi.go: go.mod go.sum
	go install -mod=readonly k8s.io/kube-openapi/cmd/openapi-gen
	$(GOPATH)/bin/openapi-gen --logtostderr \
	    -i $(INPUTS) \
	    -h ./hack/boilerplate.go.txt \
	    -p ./$(@D) \
	    -O zz_generated.openapi \
	    -o ./ \
	    -r /dev/null

.PHONY: update-generated
update-generated: $(generated_files)


# Build
# -----

.PHONY: build-fake-adapter
build-fake-adapter: update-generated
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) go build -o $(OUT_DIR)/$(ARCH)/fake-adapter github.com/naemono/fake-metrics-apiserver

docker-build:
	docker buildx build . \
		--progress=plain \
		--build-arg VERSION='$(VERSION)' \
		-t $(APISERVER_IMAGE) \
		--load

docker-build-push:
	docker buildx build . \
		--progress=plain \
		--build-arg VERSION='$(VERSION)' \
		-t $(APISERVER_IMAGE) \
		--push

# Format and lint
# ---------------

HAS_GOLANGCI_VERSION:=$(shell $(GOPATH)/bin/golangci-lint version --format=short)
.PHONY: golangci
golangci:
ifneq ($(HAS_GOLANGCI_VERSION), $(GOLANGCI_VERSION))
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOPATH)/bin v$(GOLANGCI_VERSION)
endif

.PHONY: verify-lint
verify-lint: golangci
	$(GOPATH)/bin/golangci-lint run --modules-download-mode=readonly || (echo 'Run "make update-lint"' && exit 1)

.PHONY: update-lint
update-lint: golangci
	$(GOPATH)/bin/golangci-lint run --fix --modules-download-mode=readonly


# Verify
# ------

.PHONY: verify
verify: verify-deps verify-lint verify-licenses verify-generated

.PHONY: verify-deps
verify-deps:
	go mod verify
	go mod tidy
	@git diff --exit-code -- go.sum go.mod

.PHONY: verify-generated
verify-generated: update-generated
	@git diff --exit-code -- $(generated_files)


# Test
# ----

.PHONY: test
test:
	CGO_ENABLED=0 go test ./pkg/...

.PHONY: fake-adapter-container
fake-adapter-container: build-fake-adapter
	cp Dockerfile $(TEMP_DIR)
	cp $(OUT_DIR)/$(ARCH)/fake-adapter $(TEMP_DIR)/adapter
	cd $(TEMP_DIR) && sed -i.bak "s|BASEIMAGE|scratch|g" Dockerfile
	docker build -t $(REGISTRY)/$(IMAGE)-$(ARCH):$(VERSION) $(TEMP_DIR)

.PHONY: test-kind
test-kind:
	kind load docker-image $(REGISTRY)/$(IMAGE)-$(ARCH):$(VERSION)
	sed 's|REGISTRY|'${REGISTRY}'|g' fake-adapter-deploy/testing-adapter.yaml | kubectl apply -f -
	kubectl rollout restart -n custom-metrics deployment/custom-metrics-apiserver
