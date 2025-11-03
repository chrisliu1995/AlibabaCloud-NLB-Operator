# Build variables
VERSION ?= latest
REGISTRY ?= registry.cn-hangzhou.aliyuncs.com
IMAGE_NAME ?= alibabacloud-nlb-operator
IMAGE_TAG ?= $(VERSION)
IMAGE = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# Go build variables
GO ?= go
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0

# Binary name
BINARY_NAME = alibabacloud-nlb-operator-manager

.PHONY: all
all: build

.PHONY: build
build: fmt vet
	@echo "Building $(BINARY_NAME)..."
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build \
		-o bin/$(BINARY_NAME) \
		./cmd/manager/main.go

.PHONY: run
run: fmt vet
	@echo "Running $(BINARY_NAME)..."
	$(GO) run ./cmd/manager/main.go

.PHONY: fmt
fmt:
	@echo "Running go fmt..."
	$(GO) fmt ./...

.PHONY: vet
vet:
	@echo "Running go vet..."
	$(GO) vet ./...

.PHONY: test
test: fmt vet
	@echo "Running tests..."
	$(GO) test ./... -coverprofile cover.out

.PHONY: docker-build
docker-build:
	@echo "Building docker image $(IMAGE)..."
	docker build -t $(IMAGE) .

.PHONY: docker-push
docker-push:
	@echo "Pushing docker image $(IMAGE)..."
	docker push $(IMAGE)

.PHONY: deploy
deploy:
	@echo "Deploying AlibabaCloud NLB Operator..."
	kubectl apply -f deploy/crd.yaml
	kubectl apply -f deploy/rbac.yaml
	kubectl apply -f deploy/deployment.yaml

.PHONY: undeploy
undeploy:
	@echo "Undeploying AlibabaCloud NLB Operator..."
	kubectl delete -f deploy/deployment.yaml --ignore-not-found=true
	kubectl delete -f deploy/rbac.yaml --ignore-not-found=true
	kubectl delete -f deploy/crd.yaml --ignore-not-found=true

.PHONY: clean
clean:
	@echo "Cleaning up..."
	rm -rf bin/

.PHONY: help
help:
	@echo "AlibabaCloud NLB Operator Makefile Commands:"
	@echo "  make build         - Build the binary"
	@echo "  make run           - Run the operator locally"
	@echo "  make fmt           - Run go fmt"
	@echo "  make vet           - Run go vet"
	@echo "  make test          - Run tests"
	@echo "  make docker-build  - Build docker image"
	@echo "  make docker-push   - Push docker image"
	@echo "  make deploy        - Deploy to Kubernetes"
	@echo "  make undeploy      - Remove from Kubernetes"
	@echo "  make clean         - Clean build artifacts"
