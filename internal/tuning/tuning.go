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

// EnvDirectUDPPort selects the direct engine's fixed UDP port.
const EnvDirectUDPPort = "SILO_NET_DIRECT_UDP_PORT"

// EnvDirectGSO controls UDP segmentation offload for the direct engine.
const EnvDirectGSO = "SILO_NET_DIRECT_GSO"

// EnvDirectBBRLoss selects the direct transport's BBR loss policy.
const EnvDirectBBRLoss = "SILO_NET_DIRECT_BBR_LOSS"

// EnvDirectReceiveWindow selects the client's maximum stream receive window in MiB.
// It has no effect on downloads or playback speed.
const EnvDirectReceiveWindow = "SILO_NET_DIRECT_RECEIVE_WINDOW"

// EnvDirectACKDecimation controls ACK decimation in the client policy.
const EnvDirectACKDecimation = "SILO_NET_DIRECT_ACK_DECIMATION"

const (
	defaultDirectUDPPort          = 8094
	minDirectUDPPort              = 1
	maxDirectUDPPort              = 65535
	defaultDirectReceiveWindowMiB = 128
	minDirectReceiveWindowMiB     = 4
	maxDirectReceiveWindowMiB     = 128
	knobOn                        = "on"
	knobOff                       = "off"
	directBBRLossBoth             = "both"
	directBBRLossNoLower          = "no_lower"
	directBBRLossThreshold        = "loss_thresh"
)

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
	// DirectUDPPort is the engine's UDP port; zero marks an invalid request.
	DirectUDPPort int
	DirectGSO     bool
	DirectBBRLoss string
	// DirectReceiveWindowMiB is the client's maximum stream receive window in MiB.
	// It has no effect on downloads or playback speed.
	DirectReceiveWindowMiB int
	// DirectACKDecimation controls the client policy; the server keeps it off.
	DirectACKDecimation bool
}

// Adjustment describes a requested value that is refused or clamped.
type Adjustment struct {
	Env       string
	Requested string
	Effective string
	Reason    string
}

const (
	reasonInvalid = "invalid"
	// reasonInvalidDefault marks a malformed request that keeps the knob's default
	// instead of turning the knob off.
	reasonInvalidDefault = "invalid_default"
	reasonClamped        = "clamped"
)

// Parse reads startup knobs through getenv. Empty values use the defaults.
// An unrecognized TCP or direct on/off knob turns that knob off, as
// internal/envutil does; a malformed receive window keeps its default. Invalid
// ports become zero. Numeric receive windows clamp
// to their bounds. Adjustments report each changed request in parse order;
// no adjustments means a nil slice. No value prevents process startup.
func Parse(getenv func(string) string) (Config, []Adjustment) {
	cfg := Config{
		TCPCongestion: TCPCongestionBBR, TCPSendBuffer: DefaultTCPSendBuffer,
		DirectUDPPort: defaultDirectUDPPort, DirectReceiveWindowMiB: defaultDirectReceiveWindowMiB,
	}
	var adjustments []Adjustment
	adjust := func(env, requested, effective, reason string) {
		adjustments = append(adjustments, Adjustment{
			Env:       env,
			Requested: requested,
			Effective: effective,
			Reason:    reason,
		})
	}
	requested := getenv(EnvTCPCongestion)
	switch TCPCongestion(strings.ToLower(strings.TrimSpace(requested))) {
	case "", TCPCongestionBBR:
	case TCPCongestionOff:
		cfg.TCPCongestion = TCPCongestionOff
	default:
		cfg.TCPCongestion = TCPCongestionOff
		adjust(EnvTCPCongestion, requested, knobOff, reasonInvalid)
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
			adjust(EnvTCPSendBuffer, requested, knobOff, reasonInvalid)
		}
	}
	requested = getenv(EnvDirectUDPPort)
	if normalized := strings.TrimSpace(requested); normalized != "" {
		port, err := strconv.Atoi(normalized)
		if err != nil || port < minDirectUDPPort || port > maxDirectUDPPort {
			cfg.DirectUDPPort = 0
			adjust(EnvDirectUDPPort, requested, "0", reasonInvalid)
		} else {
			cfg.DirectUDPPort = port
		}
	}
	// Each enum's first choice is its default; an unrecognized value means off.
	// off is an allowed choice of every enum.
	enum := func(env string, allowed ...string) string {
		value := getenv(env)
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			return allowed[0]
		}
		if slices.Contains(allowed, normalized) {
			return normalized
		}
		adjust(env, value, knobOff, reasonInvalid)
		return knobOff
	}
	cfg.DirectGSO = enum(EnvDirectGSO, knobOn, knobOff) == knobOn
	cfg.DirectBBRLoss = enum(EnvDirectBBRLoss, directBBRLossBoth, directBBRLossNoLower, directBBRLossThreshold, knobOff)
	requested = getenv(EnvDirectReceiveWindow)
	switch normalized := strings.TrimSpace(requested); normalized {
	case "":
	default:
		window, err := strconv.Atoi(normalized)
		if err != nil {
			adjust(EnvDirectReceiveWindow, requested, strconv.Itoa(cfg.DirectReceiveWindowMiB), reasonInvalidDefault)
			break
		}
		cfg.DirectReceiveWindowMiB = min(max(window, minDirectReceiveWindowMiB), maxDirectReceiveWindowMiB)
		if cfg.DirectReceiveWindowMiB != window {
			adjust(EnvDirectReceiveWindow, requested, strconv.Itoa(cfg.DirectReceiveWindowMiB), reasonClamped)
		}
	}
	cfg.DirectACKDecimation = enum(EnvDirectACKDecimation, knobOn, knobOff) == knobOn
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

// Knob describes a startup knob. Min and Max bound numeric values; Allowed
// lists enum choices and is absent for numeric knobs.
type Knob struct {
	Env, Default string
	Allowed      []string
	Min, Max     string
}

// Knobs returns read-only metadata in startup parse order. Each call returns
// fresh slices; defaults and choices use the same constants as Parse.
func Knobs() []Knob {
	return []Knob{
		{Env: EnvTCPCongestion, Default: string(TCPCongestionBBR), Allowed: []string{string(TCPCongestionBBR), string(TCPCongestionOff)}},
		{Env: EnvTCPSendBuffer, Default: strconv.Itoa(DefaultTCPSendBuffer), Min: "1", Max: strconv.Itoa(1<<30 - 1)},
		{Env: EnvDirectUDPPort, Default: strconv.Itoa(defaultDirectUDPPort), Min: strconv.Itoa(minDirectUDPPort), Max: strconv.Itoa(maxDirectUDPPort)},
		{Env: EnvDirectGSO, Default: knobOn, Allowed: []string{knobOn, knobOff}},
		{Env: EnvDirectBBRLoss, Default: directBBRLossBoth, Allowed: []string{directBBRLossBoth, directBBRLossNoLower, directBBRLossThreshold, knobOff}},
		{Env: EnvDirectReceiveWindow, Default: strconv.Itoa(defaultDirectReceiveWindowMiB), Min: strconv.Itoa(minDirectReceiveWindowMiB), Max: strconv.Itoa(maxDirectReceiveWindowMiB)},
		{Env: EnvDirectACKDecimation, Default: knobOn, Allowed: []string{knobOn, knobOff}},
	}
}
