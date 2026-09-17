FROM golang:1.25 AS builder
WORKDIR /app
COPY go.* ./
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
RUN go mod download
COPY ./cmd/alert-receiver ./cmd/alert-receiver
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /app/alert-receiver ./cmd/alert-receiver

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/alert-receiver /usr/local/bin/alert-receiver
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/alert-receiver"]