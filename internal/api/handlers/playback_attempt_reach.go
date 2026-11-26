package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// ownedAttemptV2 loads the live attempt row for sessionID and checks it
// belongs to the caller.
func (h *PlaybackHandler) ownedAttemptV2(ctx context.Context, caller PlaybackCaller, sessionID string) (*playback.AttemptRecordV3, error) {
	record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			return nil, playbackSessionNotFoundOperationError()
		}
		return nil, playbackStoreOperationError()
	}
	if !attemptOwnedBy(record, caller) {
		return nil, playbackOperationError(http.StatusForbidden, "forbidden", "Session belongs to another profile")
	}
	if err := mayActOnAttempt(ctx, record.LoginSessionID, caller); err != nil {
		return nil, err
	}
	return record, nil
}

// mayActOnAttempt refuses a caller that may act only on playback its own
// login session started when another login session (or none, a stream
// started before the record) started the attempt: a session another app
// opened must not stop, seek or report progress for the person's other
// devices. It asks only for an attempt the caller's session did not start,
// so a client acting on its own stream adds no query here.
func mayActOnAttempt(ctx context.Context, attemptLoginSessionID string, caller PlaybackCaller) error {
	if caller.MayReachOtherDevices == nil || (caller.LoginSessionID != "" && attemptLoginSessionID == caller.LoginSessionID) {
		return nil
	}
	err := caller.MayReachOtherDevices(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrSessionNotAllowed):
		return playbackOperationError(http.StatusForbidden, "forbidden", SessionNotAllowedDetail)
	case errors.Is(err, auth.ErrSessionRevoked):
		return playbackOperationError(http.StatusUnauthorized, "unauthorized", "Login session is no longer valid")
	}
	return playbackStoreOperationError()
}

// refuseOtherStream is mayActOnAttempt for a route that read the attempt
// itself: record and err are what GetAttempt answered. An attempt of
// another profile, or none, is left to the route's own answer. A store
// error refuses every caller that carries the rule (any login session when
// app keys are wired), not only another app's: the check needs the attempt,
// and the route would read it again and fail anyway.
func refuseOtherStream(ctx context.Context, caller PlaybackCaller, record *playback.AttemptRecordV3, err error) error {
	if caller.MayReachOtherDevices != nil && err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		return playbackStoreOperationError()
	}
	if err == nil && record != nil && attemptOwnedBy(record, caller) {
		return mayActOnAttempt(ctx, record.LoginSessionID, caller)
	}
	return nil
}
