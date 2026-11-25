package tuning

import (
	"slices"
	"testing"
)

var tuningParseTests = []struct {
	name        string
	env         map[string]string
	congestion  TCPCongestion
	sendBuffer  int
	direct      *Config
	adjustments []Adjustment
}{
	{
		name:       "direct port 80",
		env:        map[string]string{"SILO_NET_DIRECT_UDP_PORT": "80"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          80,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
	},
	{
		name:       "direct port zero",
		env:        map[string]string{"SILO_NET_DIRECT_UDP_PORT": "0"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          0,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_UDP_PORT", Requested: "0", Effective: "0", Reason: "invalid"}},
	},
	{
		name:       "direct port too large",
		env:        map[string]string{"SILO_NET_DIRECT_UDP_PORT": "65536"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          0,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_UDP_PORT", Requested: "65536", Effective: "0", Reason: "invalid"}},
	},
	{
		name:       "direct port malformed",
		env:        map[string]string{"SILO_NET_DIRECT_UDP_PORT": "abc"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          0,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_UDP_PORT", Requested: "abc", Effective: "0", Reason: "invalid"}},
	},
	{
		name:       "direct receive window below minimum",
		env:        map[string]string{"SILO_NET_DIRECT_RECEIVE_WINDOW": "2"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 4,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_RECEIVE_WINDOW", Requested: "2", Effective: "4", Reason: "clamped"}},
	},
	{
		name:       "direct receive window above maximum",
		env:        map[string]string{"SILO_NET_DIRECT_RECEIVE_WINDOW": "500"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_RECEIVE_WINDOW", Requested: "500", Effective: "128", Reason: "clamped"}},
	},
	{
		name:       "direct receive window malformed",
		env:        map[string]string{"SILO_NET_DIRECT_RECEIVE_WINDOW": "x"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_RECEIVE_WINDOW", Requested: "x", Effective: "128", Reason: "invalid_default"}},
	},
	{
		name:       "direct GSO invalid",
		env:        map[string]string{"SILO_NET_DIRECT_GSO": "maybe"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              false,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_GSO", Requested: "maybe", Effective: "off", Reason: "invalid"}},
	},
	{
		name:       "direct ACK decimation invalid",
		env:        map[string]string{"SILO_NET_DIRECT_ACK_DECIMATION": "maybe"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    false,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_ACK_DECIMATION", Requested: "maybe", Effective: "off", Reason: "invalid"}},
	},
	{
		name:       "direct BBR loss invalid",
		env:        map[string]string{"SILO_NET_DIRECT_BBR_LOSS": "none"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "off",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
		adjustments: []Adjustment{{Env: "SILO_NET_DIRECT_BBR_LOSS", Requested: "none", Effective: "off", Reason: "invalid"}},
	},
	{
		name:       "direct GSO off",
		env:        map[string]string{"SILO_NET_DIRECT_GSO": " Off "},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              false,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
	},
	{
		name:       "direct ACK decimation off",
		env:        map[string]string{"SILO_NET_DIRECT_ACK_DECIMATION": "off"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    false,
		},
	},
	{
		name:       "direct BBR loss no lower",
		env:        map[string]string{"SILO_NET_DIRECT_BBR_LOSS": "no_lower"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "no_lower",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
	},
	{
		name:       "direct BBR loss threshold",
		env:        map[string]string{"SILO_NET_DIRECT_BBR_LOSS": "loss_thresh"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "loss_thresh",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
	},
	{
		name:       "direct BBR loss off",
		env:        map[string]string{"SILO_NET_DIRECT_BBR_LOSS": "off"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "off",
			DirectReceiveWindowMiB: 128,
			DirectACKDecimation:    true,
		},
	},
	{
		name:       "direct receive window in range",
		env:        map[string]string{"SILO_NET_DIRECT_RECEIVE_WINDOW": "64"},
		congestion: TCPCongestionBBR,
		sendBuffer: 33554432,
		direct: &Config{
			DirectUDPPort:          8094,
			DirectGSO:              true,
			DirectBBRLoss:          "both",
			DirectReceiveWindowMiB: 64,
			DirectACKDecimation:    true,
		},
	},
}

func TestTuningParse(t *testing.T) {
	for _, tt := range tuningParseTests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, adjustments := Parse(func(key string) string { return tt.env[key] })
			if cfg.TCPCongestion != tt.congestion {
				t.Errorf("TCPCongestion = %q, want %q", cfg.TCPCongestion, tt.congestion)
			}
			if cfg.TCPSendBuffer != tt.sendBuffer {
				t.Errorf("TCPSendBuffer = %d, want %d", cfg.TCPSendBuffer, tt.sendBuffer)
			}
			if tt.direct != nil {
				want := *tt.direct
				want.TCPCongestion = tt.congestion
				want.TCPSendBuffer = tt.sendBuffer
				if cfg != want {
					t.Errorf("Config = %+v, want %+v", cfg, want)
				}
			}
			if tt.adjustments == nil && adjustments != nil {
				t.Errorf("adjustments = %#v, want nil", adjustments)
			} else if !slices.Equal(adjustments, tt.adjustments) {
				t.Errorf("adjustments = %#v, want %#v", adjustments, tt.adjustments)
			}
		})
	}
}
