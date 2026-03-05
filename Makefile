IMAGE_REGISTRY ?= quay.io/jcallen
IMAGE_NAME ?= ocp-vcf-dashboard
IMAGE_TAG ?= latest
IMAGE ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)
BINARY ?= bin/ocp-vcf-dashboard

GO ?= go
GOFLAGS ?=

.PHONY: all build clean image push deploy undeploy fmt vet test

all: build

build: fmt vet
	$(GO) build $(GOFLAGS) -o $(BINARY) ./cmd/dashboard

clean:
	rm -rf bin/

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./... -coverprofile cover.out

image:
	podman build -t $(IMAGE) -f Containerfile .

push: image
	podman push $(IMAGE)

deploy:
	oc apply -k config/default

undeploy:
	oc delete -k config/default

run: build
	$(BINARY) --secret-namespace=vsphere-infra-helpers --config-name=vsphere-cleanup-config
