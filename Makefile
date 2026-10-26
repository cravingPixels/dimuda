.PHONY: test lint build image run

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

build:
	go build -o dimuda ./cmd/dimuda

image:
	docker build -t dimuda:dev .

# Scans a local checkout of the stacks repo; open http://127.0.0.1:8080
run: build
	COMPOSE_DIRS=$${COMPOSE_DIRS:?set COMPOSE_DIRS, e.g. stacks=../docker-compose-stacks/stacks} \
	DATA_DIR=./data OVERRIDES_FILE=./overrides.yaml LISTEN_ADDR=127.0.0.1:8080 ./dimuda
