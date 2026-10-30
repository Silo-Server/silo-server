// Package tuning parses environment-only, per-node tuning knobs once at process
// start. Consumers read typed fields from the resulting Config.
package tuning

import (
	"context"
	"log/slog"
	"slices"
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

// Adjustment describes a requested value refused in favor of its default.
type Adjustment struct {
	Env       string
	Requested string
	Effective string
	Reason    string
}

const reasonInvalid = "invalid"

var knobs = [...]struct {
	env          string
	defaultValue TCPCongestion
	allowed      []TCPCongestion
}{
	{
		env:          EnvTCPCongestion,
		defaultValue: TCPCongestionBBR,
		allowed:      []TCPCongestion{TCPCongestionBBR, TCPCongestionOff},
	},
}

// Parse reads each knob through getenv. Empty values use the default; invalid
// values also use the default and return an adjustment. No adjustments means a
// nil slice. The caller parses once at startup and shares the resulting Config.
func Parse(getenv func(string) string) (Config, []Adjustment) {
	knob := knobs[0]
	requested := getenv(knob.env)
	effective := TCPCongestion(strings.ToLower(strings.TrimSpace(requested)))
	var adjustments []Adjustment
	if effective == "" {
		effective = knob.defaultValue
	} else if !slices.Contains(knob.allowed, effective) {
		effective = knob.defaultValue
		adjustments = append(adjustments, Adjustment{
			Env:       knob.env,
			Requested: requested,
			Effective: string(effective),
			Reason:    reasonInvalid,
		})
	}
	sendBuffer := DefaultTCPSendBuffer
	requested = getenv(EnvTCPSendBuffer)
	normalized := strings.ToLower(strings.TrimSpace(requested))
	switch normalized {
	case "":
	case "off":
		sendBuffer = 0
	default:
		// Linux doubles SO_SNDBUF and clamps requests to INT_MAX/2.
		bytes, err := strconv.ParseUint(normalized, 10, 30)
		if err == nil && bytes > 0 {
			sendBuffer = int(bytes)
		} else {
			adjustments = append(adjustments, Adjustment{
				Env:       EnvTCPSendBuffer,
				Requested: requested,
				Effective: strconv.Itoa(DefaultTCPSendBuffer),
				Reason:    reasonInvalid,
			})
		}
	}
	return Config{TCPCongestion: effective, TCPSendBuffer: sendBuffer}, adjustments
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
