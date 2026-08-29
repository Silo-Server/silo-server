package handlers

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Reconstruction replays a recipe that was committed before the H.264
// copy-safety verdict for its source was known. That is safe for everything
// except a video stream-copy: the optimistic-remux race resolves the verdict
// behind the play, and the only mechanism that withdraws a condemned remux —
// CopySafetyNotifier — reaches the in-process sessions of the replica that
// reached the verdict.
//
// A client whose replica dies between the verdict landing and the notification
// retries its signed stream URL elsewhere. The replacement replica has no live
// session, so it rebuilds one from the card and re-serves the same unsafe
// remux, with nothing left to withdraw it. Re-checking the persisted verdict at
// the moment of reconstruction closes that hole: the row is the one piece of
// state every replica shares.
//
// The check belongs BEFORE the session is rebuilt, for two reasons. Rebuilding
// registers the session against the user's stream caps, so a later refusal
// leaves a session nobody serves holding a slot the client's replacement
// attempt has to admit through. And the transport is not the only thing a card
// can revive: an HLS recipe pinned to a transcode node is served by proxying to
// that node, a path that never reaches a local transport rebuild — gating there
// would let exactly the remote remux keep streaming.
//
// The refusal is a plain not-found, matching an expired or missing recipe,
// because that is the failure a client's recovery already knows how to handle
// — it mints a fresh attempt, which plans against the persisted verdict and
// lands on a transcode.
//
// The gate is only half the answer, because the row is only half the state. A
// verdict is reached on one replica and may not be on the row at all: the write
// can fail, or the scan may still be running elsewhere. A revival whose verdict
// is *unknown* is therefore allowed — the whole point of optimistic remuxing —
// but this replica puts itself back on the race for the file before serving it,
// so the session it just built is owned by a live race here rather than by a
// verdict on a replica that may already be gone. That race costs no ffmpeg when
// the answer is already known locally or on the row; it simply re-runs the
// notification for the sessions this replica now holds.

// videoCopyRevivalRefused decides one revived video stream-copy: refuse it when
// this replica can already call the source copy-unsafe, and otherwise re-engage
// the race so a verdict reached later still reaches the session being revived.
//
// The persisted row is consulted first because it is the state every replica
// shares. When it says nothing, the racer is asked, because a verdict this
// process reached but failed to write lives only in its memo — and the row
// cannot tell that apart from "never scanned". Without that second question a
// failed write would leave the condemned recipe rebuildable forever.
//
// A missing racer (tests, minimal setups) collapses this to the row check plus
// no race, which is the pre-optimistic behavior.
func videoCopyRevivalRefused(ctx context.Context, racer PlaybackCopySafetyRacer, file *models.MediaFile, sessionID string) bool {
	if file == nil {
		return false
	}
	if multi, known := file.PersistedVideoCopyVerdict(); known {
		if multi {
			logVideoCopyRevivalRefusal(ctx, file, sessionID)
		}
		return multi
	}
	if racer == nil {
		return false
	}
	if racer.VideoCopyUnsafeKnown(ctx, file) {
		logVideoCopyRevivalRefusal(ctx, file, sessionID)
		return true
	}
	racer.RaceScan(file.ID)
	return false
}

// videoCopyReconstructRefused reports whether rebuilding a lost transport from
// card must be refused because the verdict now says its source cannot be video
// stream-copied, and re-engages the race when it does not. Only copy deliveries
// are gated; a transcode reconstruct is never touched.
//
// An unreadable row is not evidence of anything and does not refuse: the
// verdict is re-checked on every request, so a database blip costs a later
// refusal rather than a spurious one. The same applies to a handler with no
// file resolver wired (optional on PlaybackHandler).
func videoCopyReconstructRefused(ctx context.Context, files FilePathResolver, racer PlaybackCopySafetyRacer, card *playback.RecipeCard) bool {
	if card == nil || files == nil || card.MediaFileID <= 0 || !card.VideoStreamCopy() {
		return false
	}
	file, err := files.GetByID(ctx, card.MediaFileID)
	if err != nil || file == nil {
		return false
	}
	return videoCopyRevivalRefused(ctx, racer, file, card.SessionID)
}

// reconstructTransportForServe rebuilds a lost local transport from the token
// recipe for the manifest and segment serve routes. A nil card yields a nil
// result, which is the caller's not-found: to a client, a recipe it cannot
// present and a recipe that no longer rebuilds are the same thing.
//
// The copy-safety verdict is not consulted here. It is consulted in
// loadTranscodeServeSession, which is the only producer of the cards that reach
// this function and runs before the session is registered — see the file
// comment for why the earlier point is the correct one.
// The returned error carries a tone-map execution failure so the serve handler
// can render it as a 422 instead of a generic not-found; it is nil for every
// other rebuild outcome, including a plain miss.
func (h *PlaybackHandler) reconstructTransportForServe(ctx context.Context, sessionID string, requestedSegment int, card *playback.RecipeCard) (*playback.TranscodeSession, error) {
	if card == nil {
		return nil, nil
	}
	return h.tm.ReconstructTranscodeWithError(ctx, sessionID, requestedSegment, *card)
}

func logVideoCopyRevivalRefusal(ctx context.Context, file *models.MediaFile, sessionID string) {
	slog.InfoContext(ctx, "refusing to revive a copy-unsafe video stream-copy",
		"component", "api",
		"session", sessionID,
		"playback_session_id", sessionID,
		"file_id", file.ID,
		"reason", playback.PlanInvalidatedVideoCopyUnsafe,
	)
}
