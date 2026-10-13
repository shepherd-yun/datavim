.PHONY: build test test-integration lint run

build:
	go build -o bin/sqlvim ./cmd/sqlvim

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./internal/db/...

lint:
	golangci-lint run

run: build
	./bin/sqlvim
