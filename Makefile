.PHONY: build build-all web test run fmt vet clean docker release

BIN := bin/llmgw
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Rebuild the embedded console. Required after changing anything under web/.
web:
	cd web && npm run build

build:
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/llmgw

# Rebuild the console and the binary together.
build-all: web build

test:
	go test -race ./...

run:
	go run ./cmd/llmgw -config config.yaml

fmt:
	gofmt -l -w .

vet:
	go vet ./...

docker:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t llmgw:$(VERSION) .

release:
	VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE) ./scripts/release.sh

clean:
	rm -rf bin dist build
