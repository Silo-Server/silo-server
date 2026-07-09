package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	lognoop "go.opentelemetry.io/otel/log/noop"
)

func TestSetupDisabled(t *testing.T) {
	// Guard: with telemetry disabled, Setup must install nothing — in
	// particular the global MeterProvider must be untouched (metrics stay on
	// Prometheus; the built-in no-op prevents the trace instrumentation libs
	// from double-emitting).
	beforeMP := otel.GetMeterProvider()

	providers, shutdown, err := Setup(context.Background(), Config{Enabled: false})
	if err != nil {
		t.Fatalf("Setup(disabled) error = %v", err)
	}
	if providers == nil || providers.LoggerProvider == nil {
		t.Fatal("Setup(disabled) returned nil providers/logger provider")
	}
	if _, ok := providers.LoggerProvider.(lognoop.LoggerProvider); !ok {
		t.Errorf("disabled LoggerProvider = %T, want no-op", providers.LoggerProvider)
	}
	if got := otel.GetMeterProvider(); got != beforeMP {
		t.Errorf("MeterProvider changed after disabled Setup: %T", got)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("disabled shutdown error = %v", err)
	}
}

func TestSetupEnabled(t *testing.T) {
	// Use gRPC exporters with the default (unset) endpoint; the OTLP exporters
	// connect lazily so Setup succeeds without a live collector.
	beforeMP := otel.GetMeterProvider()

	cfg := Config{
		Enabled:      true,
		Protocol:     ProtocolGRPC,
		ServiceName:  "silo-server",
		NodeID:       "node-test",
		SamplerRatio: 1.0,
	}
	providers, shutdown, err := Setup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Setup(enabled) error = %v", err)
	}
	if providers == nil || providers.LoggerProvider == nil {
		t.Fatal("Setup(enabled) returned nil providers")
	}
	if _, ok := providers.LoggerProvider.(lognoop.LoggerProvider); ok {
		t.Error("enabled LoggerProvider is no-op, want real provider")
	}

	// Metrics guard: still no MeterProvider registered.
	if got := otel.GetMeterProvider(); got != beforeMP {
		t.Errorf("MeterProvider changed after enabled Setup: %T", got)
	}

	// Shutdown returns nil and is idempotent.
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown error = %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("second shutdown error = %v", err)
	}
}
