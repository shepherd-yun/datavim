.PHONY: build test test-integration lint run snapshot release-notes demo

build:
	go build -o bin/datavim ./cmd/datavim

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./internal/db/... ./internal/rdb/... ./internal/kafka/... ./internal/es/...

lint:
	golangci-lint run

run: build
	./bin/datavim

snapshot:
	HOMEBREW_TAP_GITHUB_TOKEN=dummy goreleaser release --snapshot --clean

release-notes:
	@scripts/release-notes.sh $(VERSION)

demo: build
	docker compose -f docs/demo/docker-compose.yml up -d --wait
	vhs docs/demo/demo.tape
