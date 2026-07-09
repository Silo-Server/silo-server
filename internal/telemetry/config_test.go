package telemetry

import "testing"

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantEnabled bool
		wantService string
		wantProto   Protocol
		wantRatio   float64
	}{
		{
			name:        "disabled by default",
			env:         map[string]string{},
			wantEnabled: false,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
		{
			name:        "enabled via endpoint",
			env:         map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4317"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
		{
			name:        "enabled via SILO_OTEL_ENABLED",
			env:         map[string]string{"SILO_OTEL_ENABLED": "true"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
		{
			name:        "service name override",
			env:         map[string]string{"SILO_OTEL_ENABLED": "1", "OTEL_SERVICE_NAME": "custom"},
			wantEnabled: true,
			wantService: "custom",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
		{
			name:        "http protocol",
			env:         map[string]string{"SILO_OTEL_ENABLED": "yes", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolHTTP,
			wantRatio:   1.0,
		},
		{
			name:        "sampler ratio override",
			env:         map[string]string{"SILO_OTEL_ENABLED": "on", "OTEL_TRACES_SAMPLER_ARG": "0.25"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   0.25,
		},
		{
			name:        "invalid sampler arg falls back",
			env:         map[string]string{"SILO_OTEL_ENABLED": "true", "OTEL_TRACES_SAMPLER_ARG": "not-a-number"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
		{
			name:        "sampler arg above 1 clamps to 1.0",
			env:         map[string]string{"SILO_OTEL_ENABLED": "true", "OTEL_TRACES_SAMPLER_ARG": "5"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
		{
			name:        "sampler arg +Inf falls back",
			env:         map[string]string{"SILO_OTEL_ENABLED": "true", "OTEL_TRACES_SAMPLER_ARG": "+Inf"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
		{
			name:        "sampler arg negative falls back",
			env:         map[string]string{"SILO_OTEL_ENABLED": "true", "OTEL_TRACES_SAMPLER_ARG": "-0.5"},
			wantEnabled: true,
			wantService: "silo-server",
			wantProto:   ProtocolGRPC,
			wantRatio:   1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all relevant env, then set the case's values.
			for _, k := range []string{
				"SILO_OTEL_ENABLED", "OTEL_EXPORTER_OTLP_ENDPOINT",
				"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_SERVICE_NAME",
				"OTEL_SERVICE_VERSION", "OTEL_TRACES_SAMPLER",
				"OTEL_TRACES_SAMPLER_ARG",
			} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg := LoadConfig("node-1")
			if cfg.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", cfg.Enabled, tt.wantEnabled)
			}
			if cfg.ServiceName != tt.wantService {
				t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, tt.wantService)
			}
			if cfg.Protocol != tt.wantProto {
				t.Errorf("Protocol = %q, want %q", cfg.Protocol, tt.wantProto)
			}
			if cfg.SamplerRatio != tt.wantRatio {
				t.Errorf("SamplerRatio = %v, want %v", cfg.SamplerRatio, tt.wantRatio)
			}
			if cfg.NodeID != "node-1" {
				t.Errorf("NodeID = %q, want %q", cfg.NodeID, "node-1")
			}
			if cfg.Sampler != SamplerParentBasedTraceIDRatio {
				t.Errorf("Sampler = %q, want default %q", cfg.Sampler, SamplerParentBasedTraceIDRatio)
			}
		})
	}
}

func TestParseSampler(t *testing.T) {
	tests := []struct {
		raw  string
		want Sampler
	}{
		{"", SamplerParentBasedTraceIDRatio},
		{"always_on", SamplerAlwaysOn},
		{"ALWAYS_OFF", SamplerAlwaysOff},
		{" traceidratio ", SamplerTraceIDRatio},
		{"parentbased_always_on", SamplerParentBasedAlwaysOn},
		{"parentbased_always_off", SamplerParentBasedAlwaysOff},
		{"parentbased_traceidratio", SamplerParentBasedTraceIDRatio},
		// Unsupported spec value and garbage both fall back to the default.
		{"jaeger_remote", SamplerParentBasedTraceIDRatio},
		{"bogus", SamplerParentBasedTraceIDRatio},
	}
	for _, tt := range tests {
		if got := parseSampler(tt.raw); got != tt.want {
			t.Errorf("parseSampler(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}
