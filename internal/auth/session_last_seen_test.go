package auth

import (
	"context"
	"time"
)

type sessionActivityCall struct {
	id                 string
	observed, deadline time.Time
}
type recordingSessionActivity struct{ calls chan sessionActivityCall }

func (u *recordingSessionActivity) UpdateLastSeen(ctx context.Context, id string, at time.Time) error {
	deadline, _ := ctx.Deadline()
	u.calls <- sessionActivityCall{id, at, deadline}
	return nil
}
