package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

type playbackConnectionService interface {
	PlaybackConnection(context.Context, string, bool) (connectionstore.State, error)
}
type PlaybackConnectionInput struct {
	DeviceID string `header:"X-Silo-Device-Id" maxLength:"128"`
}
type PlaybackLearnedLimit struct {
	Kbps       int       `json:"kbps"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type PlaybackConnection struct {
	Probe          *PlaybackProbe                       `json:"probe,omitempty"`
	PlaybackSample *PlaybackSample                      `json:"playback_sample,omitempty" doc:"The best recent playback throughput on this connection, a lower bound on its speed."`
	AutoBudgetKbps *int                                 `json:"auto_budget_kbps,omitempty"`
	Connection     string                               `json:"connection"`
	LearnedLimit   NullableObject[PlaybackLearnedLimit] `json:"learned_limit" doc:"The learned limit on this connection; null when there is none."`
}

// NullableObject is an object or an explicit null. Huma cannot mark a
// referenced object nullable, so the schema inlines the object, as
// NullableLoginSession does.
type NullableObject[T any] struct{ Value *T }

func (n NullableObject[T]) MarshalJSON() ([]byte, error)  { return json.Marshal(n.Value) }
func (n *NullableObject[T]) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &n.Value) }
func (NullableObject[T]) Schema(r huma.Registry) *huma.Schema {
	s := r.Schema(reflect.TypeFor[T](), false, "")
	out := *s
	out.Nullable = true
	return &out
}

type PlaybackConnectionOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         PlaybackConnection
}
type PlaybackConnectionResetOutput struct{ Status int }

func registerPlaybackConnection(reg *Registry) {
	registerPlaybackProbeResults(reg)
	op := func(method, path, id string) Operation {
		result := Operation{Operation: humaOp(method, Prefix+"/playback/connection"+path, id, playbackTag, "Read or reset the calling device's learned connection limit."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
		if method == http.MethodGet {
			result.RetrySafety = ""
		}
		return result
	}
	Register(reg, op(http.MethodGet, "", "getPlaybackConnection"), func(ctx context.Context, in *PlaybackConnectionInput) (*PlaybackConnectionOutput, error) {
		service, ok := reg.deps.Playback.(playbackConnectionService)
		if !ok {
			return nil, NewProblem(TypeCapabilityNotConfigured, "Connection observations are unavailable.")
		}
		state, err := service.PlaybackConnection(ctx, in.DeviceID, false)
		if err != nil {
			return nil, playbackProblem(err)
		}
		body := PlaybackConnection{Connection: state.Connection, Probe: playbackProbeOf(state.Probe), PlaybackSample: playbackSampleOf(state.PlaybackSample), AutoBudgetKbps: state.AutoBudgetKbps}
		if limit := state.LearnedLimit; limit != nil {
			body.LearnedLimit.Value = &PlaybackLearnedLimit{Kbps: limit.Kbps, ObservedAt: limit.ObservedAt, ExpiresAt: limit.ExpiresAt}
		}
		return &PlaybackConnectionOutput{CacheControl: playbackCacheControl, Body: body}, nil
	})
	Register(reg, op(http.MethodDelete, "/learned-limit", "resetPlaybackLearnedLimit"), func(ctx context.Context, in *PlaybackConnectionInput) (*PlaybackConnectionResetOutput, error) {
		service, ok := reg.deps.Playback.(playbackConnectionService)
		if !ok {
			return nil, NewProblem(TypeCapabilityNotConfigured, "Connection observations are unavailable.")
		}
		_, err := service.PlaybackConnection(ctx, in.DeviceID, true)
		if err != nil {
			return nil, playbackProblem(err)
		}
		return &PlaybackConnectionResetOutput{Status: http.StatusNoContent}, nil
	})
}

type playbackProbeResultService interface {
	ReportPlaybackProbe(context.Context, string, connectionstore.ProbeResult) error
}
type PlaybackProbeResultsInput struct {
	DeviceID string `header:"X-Silo-Device-Id" maxLength:"128"`
	Body     struct {
		Kbps       int    `json:"kbps" minimum:"100" maximum:"1000000" doc:"The bounds of bandwidth_estimate_kbps, so a kept reading is one a start can send."`
		Bytes      int64  `json:"bytes" minimum:"1" maximum:"1000000000"`
		DurationMS int    `json:"duration_ms" minimum:"1" maximum:"60000"`
		Transport  string `json:"transport" enum:"wifi,ethernet,cellular,other"`
		LowerBound bool   `json:"lower_bound,omitempty" doc:"The test proved only that the link carries at least kbps: a quick test that reached its target, or a rate above the bound."`
	}
}

func registerPlaybackProbeResults(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodPost, Prefix+"/playback/bandwidth-probe/results", "reportPlaybackBandwidthProbe", playbackTag, "Store the calling device's latest measured connection speed."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, func(ctx context.Context, in *PlaybackProbeResultsInput) (*PlaybackConnectionResetOutput, error) {
		service, ok := reg.deps.Playback.(playbackProbeResultService)
		if !ok {
			return nil, unavailable("connection observations")
		}
		err := service.ReportPlaybackProbe(ctx, in.DeviceID, connectionstore.ProbeResult{Kbps: in.Body.Kbps, Bytes: in.Body.Bytes, DurationMS: in.Body.DurationMS, Transport: in.Body.Transport, LowerBound: in.Body.LowerBound})
		if err != nil {
			return nil, playbackProblem(err)
		}
		return &PlaybackConnectionResetOutput{Status: http.StatusNoContent}, nil
	})
}

type PlaybackProbe struct {
	Kbps       int       `json:"kbps"`
	LowerBound bool      `json:"lower_bound" doc:"The test proved only that the link carries at least kbps."`
	MeasuredAt time.Time `json:"measured_at"`
	Transport  string    `json:"transport"`
}

func playbackProbeOf(p *connectionstore.Probe) *PlaybackProbe {
	if p == nil {
		return nil
	}
	return &PlaybackProbe{Kbps: p.Kbps, LowerBound: p.LowerBound, MeasuredAt: p.MeasuredAt, Transport: p.Transport}
}

type PlaybackSample struct {
	Kbps       int       `json:"kbps"`
	MeasuredAt time.Time `json:"measured_at"`
}

func playbackSampleOf(s *connectionstore.Sample) *PlaybackSample {
	if s == nil {
		return nil
	}
	return &PlaybackSample{Kbps: s.Kbps, MeasuredAt: s.MeasuredAt}
}
func playbackLimitOf(p *connectionstore.Limit) *PlaybackLearnedLimit {
	if p == nil {
		return nil
	}
	return &PlaybackLearnedLimit{Kbps: p.Kbps, ObservedAt: p.ObservedAt, ExpiresAt: p.ExpiresAt}
}
