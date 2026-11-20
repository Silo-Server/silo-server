package jellycompat

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A Jellyfin-compatible client cannot be asked to replan. When its direct play
// of a file fails it either posts a device profile that refuses direct play
// (Roku) or simply retries PlaybackInfo with the profile it already had. The
// second case would be handed the same original file that just failed, because
// the profile still claims it. Recording the failure server-side closes that
// loop: the next negotiation for the same device and file builds a packaged
// route (HLS remux or transcode) instead.
//
// The record is evidence about one device's direct route to one file, not a
// verdict on either: it expires, and a successful direct play of a different
// file is unaffected. Keeping it beside the device profiles (same store, same
// lifetime) means a retry served by another replica sees it too.

// compatDirectPlayFailureTTL bounds how long a failed direct-play route keeps
// the same device on a packaged route for that file. It matches the device
// profile TTL: long enough to cover a viewer retrying after a failed play,
// short enough that a client fixed by an app update is not held back.
const compatDirectPlayFailureTTL = 6 * time.Hour

// maxDirectPlayFailuresPerToken bounds the in-memory records one compat token
// can accumulate, so a client that fails across a large library cannot grow
// the map without limit.
const maxDirectPlayFailuresPerToken = 64

// directPlayFailureKey is the in-memory map key. The token is the compat
// session's authentication scope, the device id keeps two installations on one
// account apart, and the file id scopes the record to the bytes that failed.
func directPlayFailureKey(token, deviceID string, fileID int) string {
	return token + "\x00" + deviceProfileStorageID(deviceID) + "\x00" + strconv.Itoa(fileID)
}

// RecordDirectPlayFailure remembers that deviceID's direct play of fileID
// failed, so the next PlaybackInfo for that device and file does not offer it
// again. A nil or empty token/device cannot be scoped to a client and is
// ignored; the caller has already answered that request normally.
func (s *DeviceProfileStore) RecordDirectPlayFailure(ctx context.Context, token, deviceID string, fileID int) error {
	if s == nil || strings.TrimSpace(token) == "" || fileID <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	deviceID = deviceProfileStorageID(deviceID)
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sweepDirectPlayFailuresLocked(token, now)
		s.directPlayFailures[directPlayFailureKey(token, deviceID, fileID)] = now
		return nil
	}
	tokenHash := deviceProfileTokenHash(token)
	// Expired rows are removed on the write path so no background job is
	// needed for a table that only ever holds a bounded set of recent
	// failures. The delete is best effort: a failure to prune must not stop
	// the record from landing.
	_, _ = s.pool.Exec(ctx, `DELETE FROM jellycompat_direct_play_failures WHERE token_hash=$1 AND failed_at<=$2`, tokenHash, now.Add(-compatDirectPlayFailureTTL))
	_, err := s.pool.Exec(ctx, `INSERT INTO jellycompat_direct_play_failures(token_hash,device_id,file_id,failed_at)
 VALUES($1,$2,$3,$4)
 ON CONFLICT(token_hash,device_id,file_id) DO UPDATE SET failed_at=EXCLUDED.failed_at`, tokenHash, deviceID, fileID, now)
	return err
}

// ClearDirectPlayFailure forgets a recorded failed direct-play route, so the
// next PlaybackInfo for this device and file offers direct play again. It is
// how the downgrade is undone: either the packaged route it forced also
// failed to start (holding the device there would leave no working route at
// all), or compatDirectPlayFailureTTL expired the record on its own. A nil or
// empty scope clears nothing.
func (s *DeviceProfileStore) ClearDirectPlayFailure(ctx context.Context, token, deviceID string, fileID int) error {
	if s == nil || strings.TrimSpace(token) == "" || fileID <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deviceID = deviceProfileStorageID(deviceID)
	if s.pool == nil {
		s.mu.Lock()
		delete(s.directPlayFailures, directPlayFailureKey(token, deviceID, fileID))
		s.mu.Unlock()
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM jellycompat_direct_play_failures WHERE token_hash=$1 AND device_id=$2 AND file_id=$3`,
		deviceProfileTokenHash(token), deviceID, fileID)
	return err
}

// DirectPlayFailures returns the files whose direct-play route this device
// reported failing inside compatDirectPlayFailureTTL. A store or database
// error returns an empty set: a negotiation must not fail because this hint
// could not be read, and the client's own profile still governs the route.
func (s *DeviceProfileStore) DirectPlayFailures(ctx context.Context, token, deviceID string) map[int]struct{} {
	if s == nil || strings.TrimSpace(token) == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	now := s.now()
	cutoff := now.Add(-compatDirectPlayFailureTTL)
	deviceID = deviceProfileStorageID(deviceID)
	if s.pool == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.directPlayFailuresForLocked(token, deviceID, cutoff)
	}
	rows, err := s.pool.Query(ctx, `SELECT file_id FROM jellycompat_direct_play_failures
 WHERE token_hash=$1 AND device_id=$2 AND failed_at>$3`, deviceProfileTokenHash(token), deviceID, cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()
	failed := make(map[int]struct{})
	for rows.Next() {
		var fileID int
		if err := rows.Scan(&fileID); err != nil {
			return nil
		}
		failed[fileID] = struct{}{}
	}
	if rows.Err() != nil {
		return nil
	}
	return failed
}

// directPlayFailuresForLocked collects a device's unexpired records. The
// caller holds s.mu.
func (s *DeviceProfileStore) directPlayFailuresForLocked(token, deviceID string, cutoff time.Time) map[int]struct{} {
	prefix := token + "\x00" + deviceID + "\x00"
	var failed map[int]struct{}
	for key, failedAt := range s.directPlayFailures {
		if !failedAt.After(cutoff) {
			continue
		}
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		fileID, err := strconv.Atoi(strings.TrimPrefix(key, prefix))
		if err != nil {
			continue
		}
		if failed == nil {
			failed = make(map[int]struct{})
		}
		failed[fileID] = struct{}{}
	}
	return failed
}

// sweepDirectPlayFailuresLocked drops this token's expired records and, when
// the token is at its cap, the oldest ones until a new entry fits. The caller
// holds s.mu and is about to write one new record.
func (s *DeviceProfileStore) sweepDirectPlayFailuresLocked(token string, now time.Time) {
	if s.directPlayFailures == nil {
		s.directPlayFailures = make(map[string]time.Time)
	}
	prefix := token + "\x00"
	cutoff := now.Add(-compatDirectPlayFailureTTL)
	type entry struct {
		key      string
		failedAt time.Time
	}
	live := make([]entry, 0, len(s.directPlayFailures))
	for key, failedAt := range s.directPlayFailures {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if !failedAt.After(cutoff) {
			delete(s.directPlayFailures, key)
			continue
		}
		live = append(live, entry{key: key, failedAt: failedAt})
	}
	if len(live) < maxDirectPlayFailuresPerToken {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].failedAt.Before(live[j].failedAt) })
	for _, stale := range live[:len(live)-maxDirectPlayFailuresPerToken+1] {
		delete(s.directPlayFailures, stale.key)
	}
}

// compatDirectPlayStartupWindow is how long after a play's negotiation a stop
// that never advanced is read as a failed start rather than a viewer leaving.
// Inside it, a direct route that produced no playable frame and a viewer who
// backed out immediately look the same, and the cost of treating the second as
// the first is a packaged route on the next play, not a wrong stream.
const compatDirectPlayStartupWindow = 90 * time.Second

// recordCompatDirectPlayFailure remembers a failed direct-play route for this
// device and file. It is evidence from the client's own stop report: either
// Jellyfin's Failed flag, or a stop that never advanced the play inside the
// startup window. A route that delivered frames is never recorded, so a
// successful play leaves the next negotiation exactly as it was.
func (h *PlaybackHandler) recordCompatDirectPlayFailure(
	ctx context.Context,
	playSession *PlaybackSession,
	source *PlaybackMediaSource,
	failed bool,
	positionSeconds float64,
	priorPosition float64,
) {
	if h == nil || h.deviceProfiles == nil || playSession == nil || source == nil {
		return
	}
	neverRendered := positionSeconds <= 0 && priorPosition <= 0 &&
		time.Since(playSession.CreatedAt) <= compatDirectPlayStartupWindow
	if !compatSourceIsDirectPlay(playSession, source) {
		// This device is already on the packaged route the downgrade forced.
		// If that route failed to start too, the downgrade has not bought the
		// viewer anything and leaving it in place would hold the device on a
		// route that does not work: forget it, so the next negotiation tries
		// direct play again. A packaged play that did not fail leaves the
		// record alone; it keeps the device off a direct route already known
		// to fail until compatDirectPlayFailureTTL expires it.
		if !failed && !neverRendered {
			return
		}
		if err := h.deviceProfiles.ClearDirectPlayFailure(ctx, playSession.CompatToken, playSession.ClientDeviceID, source.Version.FileID); err != nil {
			slog.WarnContext(ctx, "jellycompat failed packaged route could not clear the direct play downgrade", compatLogComponentKey, compatLogComponentValue,
				"play_session_id", playSessionFingerprint(playSession.ID),
				"media_source_id", source.ID,
				"error", err,
			)
			return
		}
		slog.InfoContext(ctx, "jellycompat direct play downgrade cleared", compatLogComponentKey, compatLogComponentValue,
			"route_downgrade", compatRouteDowngradeDirectPlayFailed,
			"reason", "packaged_route_failed",
			"play_session_id", playSessionFingerprint(playSession.ID),
			"media_source_id", source.ID,
			"file_id", source.Version.FileID,
		)
		return
	}
	if !failed && !neverRendered {
		return
	}
	reason := "startup_stall"
	if failed {
		reason = "client_reported_failure"
	}
	if err := h.deviceProfiles.RecordDirectPlayFailure(ctx, playSession.CompatToken, playSession.ClientDeviceID, source.Version.FileID); err != nil {
		slog.WarnContext(ctx, "jellycompat direct play failure was not recorded", compatLogComponentKey, compatLogComponentValue,
			"play_session_id", playSessionFingerprint(playSession.ID),
			"media_source_id", source.ID,
			"error", err,
		)
		return
	}
	slog.InfoContext(ctx, "jellycompat direct play retired for this device", compatLogComponentKey, compatLogComponentValue,
		"route_downgrade", compatRouteDowngradeDirectPlayFailed,
		"reason", reason,
		"play_session_id", playSessionFingerprint(playSession.ID),
		"media_source_id", source.ID,
		"file_id", source.Version.FileID,
		"container", source.Version.Container,
	)
}

// compatSourceIsDirectPlay reports whether the play's selected source was the
// original file. The upstream method is authoritative once the session
// started; before that, a source negotiated with direct play and no transcode
// is the route the client opened.
func compatSourceIsDirectPlay(playSession *PlaybackSession, source *PlaybackMediaSource) bool {
	switch playSession.UpstreamPlayMethod {
	case string(playback.PlayDirect):
		return true
	case "":
		return source.SupportsDirectPlay && !source.HLSRemux && !source.TranscodeAudio
	default:
		return false
	}
}
