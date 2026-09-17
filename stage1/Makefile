.PHONY: build test lint run
build:
	go build -o bin/logstat .
test:
	go test -race ./...
lint:
	golangci-lint run