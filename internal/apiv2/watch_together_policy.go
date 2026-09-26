package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherPolicyService interface {
	UpdateWatchTogetherPolicy(context.Context, string, int, string, watchtogether.GuestControlPolicy) (watchtogether.Snapshot, string, error)
}
type WatchTogetherPolicyInput struct {
	RoomID string `path:"room_id"`
	Body   struct {
		GuestControlPolicy watchtogether.GuestControlPolicy `json:"guest_control_policy" enum:"host_only,guest_play_pause"`
	}
}

func registerWatchTogetherPolicy(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPatch, Prefix+"/watch-together/rooms/{room_id}/policy", "updateWatchTogetherRoomPolicy", "realtime", "Set guest transport permissions as the host. Use the returned authoritative snapshot; concurrent persistence may retain a competing policy."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 1024
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherPolicyInput) (*WatchTogetherRoomReadOutput, error) {
		if reg.deps.WatchTogetherPolicy == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, token, err := reg.deps.WatchTogetherPolicy.UpdateWatchTogetherPolicy(ctx, in.RoomID, user, profile, in.Body.GuestControlPolicy)
		if err != nil {
			if errors.Is(err, watchtogether.ErrRoomForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Only the host account and profile may change room policy.")
			}
			if errors.Is(err, watchtogether.ErrTransportNotAllowed) {
				return nil, NewProblem(TypeValidationFailed, "Invalid guest-control policy.")
			}
			return nil, suggestionProblem(err)
		}
		return watchTogetherRoomOutput(row, token)
	})
}
