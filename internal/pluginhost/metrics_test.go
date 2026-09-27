package pluginhost

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestPluginRPCBoundsMethodsAndRedactsErrors(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("baggage", "private=secret", "required-header", "fixture"))
	want := errors.New("private plugin error and password")
	err := observePluginRPC(ctx, "/silo.plugin.v1.MetadataProvider/Search", nil, nil, nil, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md.Get("baggage")) != 0 || len(md.Get("traceparent")) != 1 || md.Get("required-header")[0] != "fixture" {
			t.Error("RPC context propagation incorrect")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatal("interceptor changed error identity")
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "plugin.MetadataProvider.Search" || spans[0].Status.Description != "error" || len(spans[0].Events) != 0 {
		t.Fatalf("unexpected span: %v", spans)
	}
	for _, method := range []string{"/silo.plugin.v1.MetadataProvider/SecretQuery", "/malicious/secret", "private"} {
		if pluginOperation(method) != "other" {
			t.Fatal("unbounded plugin method")
		}
	}
}
