IMAGE ?= alert-receiver:dev
.PHONY: build run test lint vet docker-build docker-run
build:
	go build -o bin/alert-receiver ./cmd/alert-receiver
run:
	go run ./cmd/alert-receiver
test:
	go test -race ./...
lint:
	golangci-lint run
vet:
	go vet ./...
docker-build:
	docker build -t $(IMAGE) .
docker-run:
	docker run --rm -p 8080:8080 $(IMAGE)