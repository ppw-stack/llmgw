.PHONY: build test run fmt vet clean

BIN := bin/llmgw

build:
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
