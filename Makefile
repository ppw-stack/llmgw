.PHONY: build web test run fmt vet clean

BIN := bin/llmgw

# Rebuild the embedded console. Required after changing anything under web/.
web:
	cd web && npm run build

build: web
	@mkdir -p bin
	go build -trimpath -o $(BIN) ./cmd/llmgw

test:
	go test -race ./...

run:
	go run ./cmd/llmgw -config config.yaml

fmt:
	gofmt -l -w .

vet:
	go vet ./...

clean:
	rm -rf bin
