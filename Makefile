# Go isn't required on the host: everything runs in a pinned golang container,
# with module/build caches kept in named volumes so repeat runs are fast.
GO_IMAGE ?= golang:1.27
GO = docker run --rm \
	-v "$(CURDIR)":/src -w /src \
	-v pamphleteer-gomod:/go/pkg/mod \
	-v pamphleteer-gobuild:/root/.cache/go-build \
	$(GO_IMAGE) go

.PHONY: tidy vet test build image

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

build:
	$(GO) build -o /dev/null ./...

image:
	docker build -t pamphleteer:local .
