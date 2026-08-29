package abs

import (
	"context"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// absSubject normalizes an ABS user id onto the shared telemetry subject space.
//
// ABS carries the silo account id as a string — handler.go and native_sessions.go
// both recover it with strconv.Atoi — so a valid account id maps onto the same
// UserSubject native, compat and proxy publish, which is what lets a per-user
// total sum across families (§4.2b's identity normalization). Only a positive
// integer is a valid account id: "0" and "-1" parse but name no account, so they
// stay abs_user rather than being merged into the shared user space.
func absSubject(userID string) streamtelemetry.Subject {
	if userID == "" {
		return streamtelemetry.Subject{}
	}
	if id, err := strconv.Atoi(userID); err == nil && id > 0 {
		return streamtelemetry.UserSubject(id)
	}
	return streamtelemetry.Subject{Kind: streamtelemetry.SubjectABSUser, ID: userID}
}

// attachABSSession attributes a public-track observation to its ABS playback
// session. The session id is the capability on this route — there is no bearer
// token — so it is also the canonical session key.
//
// The attachment boundary here, as everywhere in this module, is AUTHORIZATION
// SUCCESS: the handler has resolved the session, passed accessFilterForAuth and
// resolved the track. Requests rejected before that point create no logical
// activity; a failure after it records an outcome on a real session.
func attachABSSession(ctx context.Context, sessionID, userID, profileID string, mediaFileID int, startedAt time.Time) {
	if sessionID == "" {
		return
	}
	attachment := streamtelemetry.Attachment{
		Subject: absSubject(userID), ProfileID: profileID, SessionID: sessionID,
		MediaFileID: mediaFileID, PlayMethod: "direct",
		StartedAtSource: streamtelemetry.StartedAtSourceFirstSeen,
		// ABS public tracks carry no signed stream token, so there is no issued-at
		// to verify. Recording anything else would be a fabrication.
		TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone,
	}
	if !startedAt.IsZero() {
		attachment.StartedAt = startedAt
		attachment.StartedAtSource = streamtelemetry.StartedAtSourceSession
	}
	streamtelemetry.Attach(ctx, attachment)
}

// attachABSTransfer attributes a download-class pour: the bare file routes, the
// RSS feed file, and anything else with a user but no stable playback session
// (§4.2b). Never a SessionID, never a play method, never cap-relevant.
func attachABSTransfer(ctx context.Context, userID, profileID string, mediaFileID int) {
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{
		Subject: absSubject(userID), ProfileID: profileID, MediaFileID: mediaFileID,
		StartedAtSource:     streamtelemetry.StartedAtSourceFirstSeen,
		TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone,
	})
}
