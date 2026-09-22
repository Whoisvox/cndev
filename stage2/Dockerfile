# ----- Builder ------
FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
RUN go mod download
COPY ./cmd/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /app/logsvc ./cmd/server

# ----- Runtime -----
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/logsvc /usr/local/bin/logsvc
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/logsvc"]