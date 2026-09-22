IMAGE ?= logsvc:dev
.PHONY: build run test lint vet docker-build docker-run
build:
	go build -o bin/logsvc ./cmd/server
run:
	go run ./cmd/server
test:
	go test -race ./...
lint:
	golangci-lint run
vet:
	go vet ./...
docker-build:
	docker build --no-cache -t $(IMAGE) .
docker-run:
	docker run --rm -p 8080:8080 $(IMAGE)
docker-push:
	docker tag $(IMAGE) localhost/godev/$(IMAGE)
	docker push localhost/godev/$(IMAGE)