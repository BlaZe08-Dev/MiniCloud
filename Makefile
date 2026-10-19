.PHONY: all build test test-race fmt vet run-coordinator run-worker demo clean

BIN_DIR := bin

all: fmt vet build test-race

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/coordinator ./cmd/coordinator
	go build -o $(BIN_DIR)/worker ./cmd/worker
	go build -o $(BIN_DIR)/mc ./cmd/mc
	@echo "Build complete. Executables located in $(BIN_DIR)/"

test:
	go test -v ./...

test-race:
	go test -v -race ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

run-coordinator: build
	./$(BIN_DIR)/coordinator

run-worker: build
	./$(BIN_DIR)/worker

demo: build
	@echo "=== Starting MiniCloud Local Core Demo ==="
	@bash scripts/demo.sh

clean:
	rm -rf $(BIN_DIR)
