package main

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// initTracer 初始化全局 TracerProvider, 返回tp 供 main 里Shutdown用。
func initTracer(ctx context.Context, serviceName string) (*sdktrace.TracerProvider, error) {
	// 1) 创建exporter
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	// 2) resource: service.name, 打传递来源为服务名
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName)),
	)

	// 3) provider: 组装
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),  // processor
		sdktrace.WithResource(res), // resource
		// sampler先不配(默认 ParentBased+AlwaysSample)
	)

	// 4) 设全局 + 传播器
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp, nil

}
