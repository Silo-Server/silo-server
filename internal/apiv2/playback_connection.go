package apiv2

import (
	"context"
	"net/http"
	"time"

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
	Connection   string                `json:"connection"`
	LearnedLimit *PlaybackLearnedLimit `json:"learned_limit"`
}
type PlaybackConnectionOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         PlaybackConnection
}
type PlaybackConnectionResetOutput struct{ Status int }

func registerPlaybackConnection(reg *Registry) {
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
		body := PlaybackConnection{Connection: state.Connection}
		if limit := state.LearnedLimit; limit != nil {
			body.LearnedLimit = &PlaybackLearnedLimit{Kbps: limit.Kbps, ObservedAt: limit.ObservedAt, ExpiresAt: limit.ExpiresAt}
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
