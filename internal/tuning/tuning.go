// Package tuning parses environment-only, per-node tuning knobs once at process
// start. Consumers read typed fields from the resulting Config.
package tuning

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
)

// EnvTCPCongestion selects the congestion control for client-facing listeners.
const EnvTCPCongestion = "SILO_NET_TCP_CONGESTION"

// EnvTCPSendBuffer selects a TCP send-buffer force size in bytes, or off.
const EnvTCPSendBuffer = "SILO_NET_TCP_SEND_BUFFER"

// DefaultTCPSendBuffer is the default send-buffer force request in bytes.
const DefaultTCPSendBuffer = 33554432

// TCPCongestion is a listener congestion-control policy.
type TCPCongestion string

const (
	// TCPCongestionBBR requests kernel BBR on client-facing listeners.
	TCPCongestionBBR TCPCongestion = "bbr"
	// TCPCongestionOff leaves the kernel's congestion-control choice unchanged.
	TCPCongestionOff TCPCongestion = "off"
)

// Config holds the effective tuning values for this process.
type Config struct {
	TCPCongestion TCPCongestion
	// TCPSendBuffer is the force request in bytes; zero keeps kernel autotuning.
	TCPSendBuffer int
}

// Adjustment describes a requested value refused in favor of off.
type Adjustment struct {
	Env       string
	Requested string
	Effective string
	Reason    string
}

const reasonInvalid = "invalid"

// Parse reads both knobs through getenv. Empty values use the defaults. A value
// a knob does not recognize turns that knob off and returns an adjustment, as
// internal/envutil does for flags that default on: an operator who mistypes the
// off switch was reaching for off. No adjustments means a nil slice. The caller
// parses once at startup and shares the resulting Config.
func Parse(getenv func(string) string) (Config, []Adjustment) {
	cfg := Config{TCPCongestion: TCPCongestionBBR, TCPSendBuffer: DefaultTCPSendBuffer}
	var adjustments []Adjustment
	refuse := func(env, requested string) {
		adjustments = append(adjustments, Adjustment{
			Env:       env,
			Requested: requested,
			Effective: string(TCPCongestionOff),
			Reason:    reasonInvalid,
		})
	}
	requested := getenv(EnvTCPCongestion)
	switch TCPCongestion(strings.ToLower(strings.TrimSpace(requested))) {
	case "", TCPCongestionBBR:
	case TCPCongestionOff:
		cfg.TCPCongestion = TCPCongestionOff
	default:
		cfg.TCPCongestion = TCPCongestionOff
		refuse(EnvTCPCongestion, requested)
	}
	requested = getenv(EnvTCPSendBuffer)
	switch normalized := strings.ToLower(strings.TrimSpace(requested)); normalized {
	case "":
	case string(TCPCongestionOff):
		cfg.TCPSendBuffer = 0
	default:
		// Linux doubles SO_SNDBUF and clamps requests to INT_MAX/2.
		if bytes, err := strconv.ParseUint(normalized, 10, 30); err == nil && bytes > 0 {
			cfg.TCPSendBuffer = int(bytes)
		} else {
			cfg.TCPSendBuffer = 0
			refuse(EnvTCPSendBuffer, requested)
		}
	}
	return cfg, adjustments
}

// LogAdjustments logs one warning per adjustment using ctx.
func LogAdjustments(ctx context.Context, adjustments []Adjustment) {
	for _, adjustment := range adjustments {
		slog.WarnContext(ctx, "tuning knob: value adjusted",
			slog.String("component", "tuning"),
			slog.String("env", adjustment.Env),
			slog.String("requested", adjustment.Requested),
			slog.String("effective", adjustment.Effective),
			slog.String("reason", adjustment.Reason),
		)
	}
}
