package tuning

import (
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		congestion  TCPCongestion
		sendBuffer  int
		adjustments []Adjustment
	}{
		{
			name:       "unset",
			env:        map[string]string{},
			congestion: TCPCongestionBBR,
			sendBuffer: 33554432,
		},
		{
			name:       "normalized off",
			env:        map[string]string{EnvTCPCongestion: " Off ", "SILO_NET_TCP_SEND_BUFFER": " Off "},
			congestion: TCPCongestionOff,
		},
		{
			name:       "mistyped off switches",
			env:        map[string]string{EnvTCPCongestion: "false", "SILO_NET_TCP_SEND_BUFFER": "0"},
			congestion: TCPCongestionOff,
			adjustments: []Adjustment{
				{Env: "SILO_NET_TCP_CONGESTION", Requested: "false", Effective: "off", Reason: "invalid"},
				{Env: "SILO_NET_TCP_SEND_BUFFER", Requested: "0", Effective: "off", Reason: "invalid"},
			},
		},
		{
			name:       "invalid enum",
			env:        map[string]string{EnvTCPCongestion: "cubic", "SILO_NET_TCP_SEND_BUFFER": " 16777216 "},
			congestion: TCPCongestionOff,
			sendBuffer: 16777216,
			adjustments: []Adjustment{{
				Env:       "SILO_NET_TCP_CONGESTION",
				Requested: "cubic",
				Effective: "off",
				Reason:    "invalid",
			}},
		},
		{
			name:       "oversized send buffer",
			env:        map[string]string{"SILO_NET_TCP_SEND_BUFFER": "1073741824"},
			congestion: TCPCongestionBBR,
			adjustments: []Adjustment{{
				Env:       "SILO_NET_TCP_SEND_BUFFER",
				Requested: "1073741824",
				Effective: "off",
				Reason:    "invalid",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, adjustments := Parse(func(key string) string { return tt.env[key] })
			if cfg.TCPCongestion != tt.congestion {
				t.Errorf("TCPCongestion = %q, want %q", cfg.TCPCongestion, tt.congestion)
			}
			if cfg.TCPSendBuffer != tt.sendBuffer {
				t.Errorf("TCPSendBuffer = %d, want %d", cfg.TCPSendBuffer, tt.sendBuffer)
			}
			if tt.adjustments == nil && adjustments != nil {
				t.Errorf("adjustments = %#v, want nil", adjustments)
			} else if !slices.Equal(adjustments, tt.adjustments) {
				t.Errorf("adjustments = %#v, want %#v", adjustments, tt.adjustments)
			}
		})
	}
}
