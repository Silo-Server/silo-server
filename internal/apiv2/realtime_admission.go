package apiv2

import (
	"errors"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
)

// RealtimeWorkAdmission projects the startup enforcement status into the native
// contract. Store implementation types do not own published API schemas.
type RealtimeWorkAdmission struct {
	Version       int                    `json:"version"`
	Backend       string                 `json:"backend"`
	Scope         string                 `json:"scope"`
	Epoch         string                 `json:"epoch"`
	Semantics     string                 `json:"semantics"`
	DeadlineMS    int                    `json:"deadline_ms"`
	EventsAccount RealtimeWorkLimit      `json:"events_account"`
	RoomRecovery  RealtimeRoomWorkLimits `json:"room_recovery"`
	RoomAction    RealtimeRoomWorkLimits `json:"room_action"`
}

type RealtimeWorkLimit struct {
	PerSecond int `json:"per_second"`
	PerMinute int `json:"per_minute"`
}

type RealtimeRoomWorkLimits struct {
	Member  RealtimeWorkLimit `json:"member"`
	Account RealtimeWorkLimit `json:"account"`
}

type RealtimeWorkRefusal struct {
	Type         string `json:"type"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMS int64  `json:"retry_after_ms"`
	WorkStarted  bool   `json:"work_started"`
}

type realtimeWorkStatusProvider interface {
	RealtimeWorkStatus() *realtimeadmission.Status
}

func realtimeWorkStatus(service any) *RealtimeWorkAdmission {
	if provider, ok := service.(realtimeWorkStatusProvider); ok {
		status := provider.RealtimeWorkStatus()
		if status == nil {
			return nil
		}
		return &RealtimeWorkAdmission{
			Version: status.Version, Backend: status.Backend, Scope: status.Scope,
			Epoch: status.Epoch, Semantics: status.Semantics, DeadlineMS: status.DeadlineMS,
			EventsAccount: RealtimeWorkLimit(status.EventsAccount),
			RoomRecovery:  RealtimeRoomWorkLimits{Member: RealtimeWorkLimit(status.RoomRecovery.Member), Account: RealtimeWorkLimit(status.RoomRecovery.Account)},
			RoomAction:    RealtimeRoomWorkLimits{Member: RealtimeWorkLimit(status.RoomAction.Member), Account: RealtimeWorkLimit(status.RoomAction.Account)},
		}
	}
	return nil
}

// Handshake refusal is JSON for work admission; other handshake failures keep
// the existing plain-text representation.
func addRealtimeWorkResponses(reg *Registry, responses map[string]*huma.Response) {
	for _, status := range []string{"429", "503"} {
		responses[status].Headers = map[string]*huma.Param{
			"Retry-After": {Schema: &huma.Schema{Type: huma.TypeString}, Description: "Minimum retry delay in seconds."},
		}
		responses[status].Content[mediaTypeJSON] = &huma.MediaType{Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[RealtimeWorkRefusal](), true, "")}
	}
}

func realtimeWorkProblem(err error) *Problem {
	refusal, ok := errors.AsType[*realtimeadmission.Refusal](err)
	if !ok {
		return nil
	}
	problemType := TypeRateLimited
	if refusal.Code == "realtime_unavailable" {
		problemType = TypeDependencyUnavailable
	}
	problem := NewProblem(problemType, refusal.Message)
	problem.Code = refusal.Code
	problem.RetryAfterMS = max(1, int64((refusal.RetryAfter+time.Millisecond-1)/time.Millisecond))
	problem.WorkStarted = new(false)
	return problem.WithRetryAfter(int((problem.RetryAfterMS + 999) / 1000))
}
