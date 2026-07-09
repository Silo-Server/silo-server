// Package telemetry provides the OpenTelemetry SDK bootstrap for Silo: shared
// resource, tracer provider, logger provider, and W3C propagation. It
// deliberately does NOT build or register a MeterProvider — metrics stay on the
// existing Prometheus rail. Leaving the global MeterProvider as the built-in
// no-op is what prevents the trace instrumentation libraries from double-emitting
// metrics. See docs/superpowers/plans/2026-07-02-opentelemetry-observability.md.
package telemetry

import (
	"math"
	"os"
	"strconv"
	"strings"
)

// Protocol identifies the OTLP exporter wire protocol.
type Protocol string

const (
	// ProtocolGRPC is the OTLP/gRPC protocol (default).
	ProtocolGRPC Protocol = "grpc"
	// ProtocolHTTP is the OTLP/HTTP+protobuf protocol.
	ProtocolHTTP Protocol = "http/protobuf"
)

// Sampler identifies the head-sampling strategy (OTEL_TRACES_SAMPLER).
type Sampler string

const (
	// SamplerAlwaysOn samples every trace.
	SamplerAlwaysOn Sampler = "always_on"
	// SamplerAlwaysOff samples nothing.
	SamplerAlwaysOff Sampler = "always_off"
	// SamplerTraceIDRatio samples by trace-id ratio regardless of the parent.
	SamplerTraceIDRatio Sampler = "traceidratio"
	// SamplerParentBasedAlwaysOn honors the parent decision, sampling roots.
	SamplerParentBasedAlwaysOn Sampler = "parentbased_always_on"
	// SamplerParentBasedAlwaysOff honors the parent decision, dropping roots.
	SamplerParentBasedAlwaysOff Sampler = "parentbased_always_off"
	// SamplerParentBasedTraceIDRatio honors the parent decision, sampling roots
	// by trace-id ratio. This is the default.
	SamplerParentBasedTraceIDRatio Sampler = "parentbased_traceidratio"
)

// defaultServiceName is used when OTEL_SERVICE_NAME is unset.
const defaultServiceName = "silo-server"

// defaultSamplerRatio is the parent-based trace-id ratio applied when
// OTEL_TRACES_SAMPLER_ARG is unset or unparseable.
const defaultSamplerRatio = 1.0

// Config is the fully-defaulted telemetry configuration parsed from the
// environment. It is cheap to construct and safe to build even when telemetry
// is disabled.
type Config struct {
	// Enabled gates the entire feature. True when SILO_OTEL_ENABLED is truthy
	// OR OTEL_EXPORTER_OTLP_ENDPOINT is set.
	Enabled bool

	// Endpoint is the OTLP collector endpoint (OTEL_EXPORTER_OTLP_ENDPOINT). It
	// is used ONLY to decide Enabled; the exporters themselves read the endpoint
	// (and all other OTEL_EXPORTER_OTLP_* knobs) directly from the environment,
	// which remains the single source of truth for exporter wiring.
	Endpoint string
	// Protocol is the generic OTLP wire protocol (OTEL_EXPORTER_OTLP_PROTOCOL).
	// It is the fallback for any signal without a signal-specific override.
	Protocol Protocol
	// TracesProtocol selects the trace exporter's wire protocol, honoring
	// OTEL_EXPORTER_OTLP_TRACES_PROTOCOL and falling back to Protocol.
	TracesProtocol Protocol
	// LogsProtocol selects the log exporter's wire protocol, honoring
	// OTEL_EXPORTER_OTLP_LOGS_PROTOCOL and falling back to Protocol.
	LogsProtocol Protocol

	// ServiceName populates the service.name resource attribute.
	ServiceName string
	// ServiceVersion populates the service.version resource attribute.
	ServiceVersion string
	// NodeID populates the service.instance.id resource attribute.
	NodeID string

	// Sampler is the head-sampling strategy (OTEL_TRACES_SAMPLER). Unrecognized
	// or unsupported values (e.g. jaeger_remote) fall back to
	// parentbased_traceidratio.
	Sampler Sampler
	// SamplerRatio is the trace-id-ratio sampling probability used by the
	// ratio-based samplers (OTEL_TRACES_SAMPLER_ARG).
	SamplerRatio float64
}

// LoadConfig parses the telemetry configuration from the environment. nodeID is
// the resolved node identity used for the service.instance.id resource
// attribute.
func LoadConfig(nodeID string) Config {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	enabled := truthy(os.Getenv("SILO_OTEL_ENABLED")) || endpoint != ""

	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = defaultServiceName
	}

	// The generic protocol is the fallback; per-signal env vars override it for
	// their own exporter so mixed collector setups (e.g. HTTP logs, gRPC traces)
	// work as the OTLP spec prescribes.
	protocol := parseProtocol(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"), ProtocolGRPC)
	tracesProtocol := parseProtocol(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"), protocol)
	logsProtocol := parseProtocol(os.Getenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"), protocol)

	ratio := defaultSamplerRatio
	if raw := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG")); raw != "" {
		// Accept only finite, non-negative values; clamp above 1 to 1.0 so a
		// typo'd or +Inf arg means "sample everything" rather than silently
		// falling through. NaN and -Inf fail the v >= 0 / IsInf checks.
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 && !math.IsInf(v, 1) {
			ratio = math.Min(v, 1.0)
		}
	}

	return Config{
		Enabled:        enabled,
		Endpoint:       endpoint,
		Protocol:       protocol,
		TracesProtocol: tracesProtocol,
		LogsProtocol:   logsProtocol,
		ServiceName:    serviceName,
		ServiceVersion: strings.TrimSpace(os.Getenv("OTEL_SERVICE_VERSION")),
		NodeID:         nodeID,
		Sampler:        parseSampler(os.Getenv("OTEL_TRACES_SAMPLER")),
		SamplerRatio:   ratio,
	}
}

// parseSampler maps an OTEL_TRACES_SAMPLER value to a Sampler, falling back to
// parentbased_traceidratio when the value is empty, unrecognized, or names a
// sampler this bootstrap does not support (e.g. jaeger_remote).
func parseSampler(raw string) Sampler {
	switch s := Sampler(strings.ToLower(strings.TrimSpace(raw))); s {
	case SamplerAlwaysOn, SamplerAlwaysOff, SamplerTraceIDRatio,
		SamplerParentBasedAlwaysOn, SamplerParentBasedAlwaysOff:
		return s
	default:
		return SamplerParentBasedTraceIDRatio
	}
}

// parseProtocol maps an OTEL_EXPORTER_OTLP*_PROTOCOL value to a Protocol,
// returning fallback when the value is empty or unrecognized.
func parseProtocol(raw string, fallback Protocol) Protocol {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "http/protobuf", "http":
		return ProtocolHTTP
	case "grpc":
		return ProtocolGRPC
	default:
		return fallback
	}
}

// truthy reports whether an env value should be treated as a boolean true.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
