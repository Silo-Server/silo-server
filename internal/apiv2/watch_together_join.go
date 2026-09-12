package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherJoinService interface {
	JoinWatchTogetherRoom(context.Context, int, string, watchtogether.JoinInput) (watchtogether.Snapshot, string, error)
}
type WatchTogetherJoinInput struct {
	Body struct {
		Code      string `json:"code,omitempty" maxLength:"128"`
		JoinToken string `json:"join_token,omitempty" maxLength:"4096"`
	}
}

func registerWatchTogetherJoin(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/join", "joinWatchTogetherRoom", "realtime", "Resolve an active room by invite token or code and issue account/profile room proof. Connected membership is established separately by the room socket."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 8192
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherJoinInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherJoin == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		input := watchtogether.JoinInput{Code: strings.TrimSpace(in.Body.Code), JoinToken: strings.TrimSpace(in.Body.JoinToken)}
		if input.Code == "" && input.JoinToken == "" {
			return nil, NewProblem(TypeValidationFailed, "Room code or invite token is required.")
		}
		row, token, err := reg.deps.WatchTogetherJoin.JoinWatchTogetherRoom(ctx, user, profile, input)
		if err != nil {
			if errors.Is(err, watchtogether.ErrInvalidJoinRequest) {
				return nil, NewProblem(TypeValidationFailed, "Room code or invite token is required.")
			}
			return nil, suggestionProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}
