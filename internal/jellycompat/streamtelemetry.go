package jellycompat

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// The attachment boundary for every compat media handler is AUTHORIZATION
// SUCCESS — the point where the handler has established who is asking and which
// play session or item they are entitled to. Requests rejected before that point
// (401, 403, and the 404s that stand in for "that play session is not yours")
// create no logical activity. A failure AFTER that point — a missing file, an
// upstream 502, an invalid subtitle index — still creates activity, because it is
// real traffic by an authorized principal and logicalSession.outcomes is what
// records how it ended.
//
// This matters most on HandleMasterManifest (streams.go:197): it finishes
// authorization at the CompatToken and media-source checks, then starts real work
// via ensureUpstreamPlayback and can still 404 below that. Design §4.2 enrolls
// manifest routes precisely because "a killed session that reaches an unenrolled
// manifest route can reconstruct or start ffmpeg before the next segment is ever
// cut", so the attach must land BEFORE that side effect for P1's cut to be able
// to prevent it.

// attachCompatStream attributes a compat playback observation to its play
// session. Identity comes from the authenticated compat session, never from the
// request: Session.StreamAppUserID is the numeric silo account id, so compat
// sessions land in the same subject space as native and proxy and a per-user
// total sums across families.
//
// The canonical key is the UPSTREAM playback session id, never the compat
// PlaybackSession.ID. Every other publisher — the proxy (from the stream token's
// SessionID claim), nodesessions, and playback_sessions_sync — keys on
// playback.Session.ID, and BuildGlobalView merges by exact SessionID string.
// Keying compat on its own play-session id would split one viewing into two
// merged sessions: a byte-less compat twin and the proxy record carrying all the
// traffic, and would make every compat session look telemetry_only in parity.
//
// A compat play session does not learn its upstream id until
// ensureUpstreamPlayback (or ensureTranscodeManifest) has run, so on the very
// first request of a session this is a no-op and the caller attaches again once
// the id exists. Skipping is deliberate: a provisional key would produce exactly
// the ghost session described above, and a brand-new session cannot have a
// pending cut against it because its id did not exist a moment ago. The
// consequence is that a request failing before the upstream session is minted
// lands in the unattributed counters rather than on a session — which is
// correct, since nothing else in the fleet knows that id either.
func attachCompatStream(ctx context.Context, session *Session, play *PlaybackSession, mediaFileID int) {
	if session == nil || play == nil || play.UpstreamSessionID == "" {
		return
	}
	attachment := streamtelemetry.Attachment{
		Subject:     streamtelemetry.UserSubject(session.StreamAppUserID),
		ProfileID:   session.ProfileID,
		MediaFileID: mediaFileID,
		// The compat token is a session token, not a signed stream token whose
		// iat this path verifies. Recording "verified" would be a lie.
		TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone,
		StartedAtSource:     streamtelemetry.StartedAtSourceFirstSeen,
	}
	attachment.SessionID = play.UpstreamSessionID
	attachment.PlayMethod = play.UpstreamPlayMethod
	if !play.CreatedAt.IsZero() {
		// P0a established the top-level compat CreatedAt as the source of
		// truth for a compat session's start time.
		attachment.StartedAt = play.CreatedAt
		attachment.StartedAtSource = streamtelemetry.StartedAtSourceSession
	}
	streamtelemetry.Attach(ctx, attachment)
}

// attachCompatTransfer attributes a download-class pour. Per §4.2b these carry a
// user but no stable playback session, so they never get a SessionID or a play
// method and never participate in per-session ratio rules.
func attachCompatTransfer(ctx context.Context, session *Session, mediaFileID int) {
	if session == nil {
		return
	}
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{
		Subject:             streamtelemetry.UserSubject(session.StreamAppUserID),
		ProfileID:           session.ProfileID,
		MediaFileID:         mediaFileID,
		StartedAtSource:     streamtelemetry.StartedAtSourceFirstSeen,
		TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone,
	})
}
