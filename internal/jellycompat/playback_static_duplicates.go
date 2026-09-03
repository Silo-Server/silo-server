package jellycompat

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// ResolveDeviceClientPlaySessionID uses the request's explicit device identity
// to disambiguate a current play from older records that never stored a device.
// It does not merge or delete those records, or guess when identity is absent.
func (s *PlaybackSessionStore) ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID string, includeTerminal bool) (*PlaybackSession, error) {
	if deviceID != "" {
		if session, ok := s.findDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal); ok {
			return session, nil
		}
	}
	return nil, ErrSessionNotFound
}

func (d *DurableCompatPlaybackStore) ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID string, includeTerminal bool) (*PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal)
	}
	if token == "" || playID == "" || deviceID == "" {
		return nil, ErrSessionNotFound
	}
	cached, cachedOK := d.mem.findDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal)
	if cachedOK && !d.shouldRevalidateToken(token) {
		return cached, nil
	}
	if err := d.loadByCompatToken(token); err != nil {
		if cachedOK {
			return cached, nil
		}
		return nil, err
	}
	return d.mem.ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal)
}

// ResolveClientPlaySessionID preserves a storage failure for callers that may
// otherwise create a new static reservation after an ambiguous lookup.
func (s *PlaybackSessionStore) ResolveClientPlaySessionID(token, clientPlayID string) (*PlaybackSession, error) {
	s.reconcileLegacyStaticDuplicates(token, clientPlayID)
	if session, ok := s.findByClientPlaySessionID(token, clientPlayID, "", "", false); ok {
		return session, nil
	}
	return nil, ErrSessionNotFound
}

func (d *DurableCompatPlaybackStore) ResolveClientPlaySessionID(token, clientPlayID string) (*PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.ResolveClientPlaySessionID(token, clientPlayID)
	}
	if token == "" || clientPlayID == "" {
		return nil, ErrSessionNotFound
	}
	cached, cachedOK := d.mem.findByClientPlaySessionID(token, clientPlayID, "", "", false)
	if cachedOK && !d.shouldRevalidateToken(token) {
		return cached, nil
	}
	if err := d.loadByCompatToken(token); err != nil {
		if cachedOK {
			return cached, nil
		}
		return nil, err
	}
	if err := d.reconcileLegacyStaticDuplicates(token, clientPlayID); err != nil {
		return nil, err
	}
	if session, ok := d.mem.findByClientPlaySessionID(token, clientPlayID, "", "", false); ok {
		return session, nil
	}
	return nil, ErrSessionNotFound
}

// legacyStaticDuplicateGroup recognizes only the old parallel Static=true
// creation race. A shared title/route alone is never evidence of one play.
// Restrict repair to direct plays of the same selected file, with the same nonempty client
// play ID and identity, created within one second. New reservations use their
// own atomic key and are not candidates for this legacy repair.
func legacyStaticDuplicateGroup(sessions []PlaybackSession) (string, []string) {
	if len(sessions) < 2 || len(sessions) > 32 {
		return "", nil
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
	})
	first := sessions[0]
	if first.CompatToken == "" || first.UserID == "" || first.ItemID == "" || first.RouteItemID == "" || first.CreatedAt.IsZero() {
		return "", nil
	}
	selectedFile := legacyStaticSelectedFile(first)
	if selectedFile <= 0 {
		return "", nil
	}
	for _, s := range sessions {
		if s.Terminal || s.SupersededBy != "" || s.StaticPlaybackKey != "" || s.Recipe != nil || s.TranscodeStarted ||
			s.UpstreamPlayMethod != string(playback.PlayDirect) || s.UpstreamSessionID == "" ||
			s.ClientPlaySessionID == "" || s.ClientPlaySessionID == s.ID ||
			s.CompatToken != first.CompatToken || s.UserID != first.UserID || s.ClientDeviceID != first.ClientDeviceID ||
			s.ClientPlaySessionID != first.ClientPlaySessionID || s.ItemID != first.ItemID ||
			!mediaSourceIDsEqual(s.RouteItemID, first.RouteItemID) || s.CreatedAt.Sub(first.CreatedAt) > time.Second {
			return "", nil
		}
		if legacyStaticSelectedFile(s) != selectedFile {
			return "", nil
		}
	}
	ids := make([]string, 0, len(sessions)-1)
	for _, s := range sessions[1:] {
		ids = append(ids, s.ID)
	}
	return first.ID, ids
}

func legacyStaticSelectedFile(s PlaybackSession) int {
	if s.SelectedMediaFileID > 0 {
		for _, source := range s.MediaSources {
			if source.FileID == s.SelectedMediaFileID {
				return source.FileID
			}
		}
		return 0
	}
	if len(s.MediaSources) == 1 {
		return s.MediaSources[0].FileID
	}
	return 0
}

func (s *PlaybackSessionStore) legacyStaticCandidates(token, clientPlayID string) []PlaybackSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var candidates []PlaybackSession
	for _, session := range s.sessions {
		if session.CompatToken == token && session.ClientPlaySessionID == clientPlayID &&
			!session.Terminal && session.ExpiresAt.After(s.now()) {
			candidates = append(candidates, session)
		}
	}
	return candidates
}

func (s *PlaybackSessionStore) supersedeStaticSessions(token, winner string, ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if session, ok := s.sessions[id]; ok && session.CompatToken == token {
			session.Terminal = true
			session.SupersededBy = winner
			session.UpdatedAt = s.now()
			s.sessions[id] = session
		}
	}
}

func (s *PlaybackSessionStore) reconcileLegacyStaticDuplicates(token, clientPlayID string) {
	// Recheck under the mutation lock: a stop or a recipe update between the
	// candidate read and repair must not be overwritten.
	s.mu.Lock()
	defer s.mu.Unlock()
	var candidates []PlaybackSession
	for _, session := range s.sessions {
		if session.CompatToken == token && session.ClientPlaySessionID == clientPlayID &&
			!session.Terminal && session.ExpiresAt.After(s.now()) {
			candidates = append(candidates, session)
		}
	}
	winner, ids := legacyStaticDuplicateGroup(candidates)
	for _, id := range ids {
		session := s.sessions[id]
		session.Terminal, session.SupersededBy = true, winner
		session.UpdatedAt = s.now()
		s.sessions[id] = session
	}
}

func (d *DurableCompatPlaybackStore) reconcileLegacyStaticDuplicates(token, clientPlayID string) error {
	if d.pool == nil {
		d.mem.reconcileLegacyStaticDuplicates(token, clientPlayID)
		return nil
	}
	if len(d.mem.legacyStaticCandidates(token, clientPlayID)) < 2 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT data FROM jellycompat_playback_sessions
		WHERE compat_token = $1 AND data->>'ClientPlaySessionID' = $2
		AND expires_at > $3 AND COALESCE((data->>'Terminal')::boolean, false) = false
		ORDER BY id LIMIT 33 FOR UPDATE`, token, clientPlayID, d.now())
	if err != nil {
		return err
	}
	var candidates []PlaybackSession
	for rows.Next() {
		var data []byte
		var session PlaybackSession
		if err = rows.Scan(&data); err == nil {
			err = json.Unmarshal(data, &session)
		}
		if err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, session)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Legacy multi-version negotiations did not store the selected source.
	// Use the native session snapshot as evidence, never the first edition.
	var upstreamIDs []string
	for _, candidate := range candidates {
		if legacyStaticSelectedFile(candidate) == 0 && candidate.UpstreamSessionID != "" {
			upstreamIDs = append(upstreamIDs, candidate.UpstreamSessionID)
		}
	}
	if len(upstreamIDs) > 0 {
		selectedRows, queryErr := tx.Query(ctx, `SELECT session_id, media_file_id FROM playback_sessions_sync WHERE session_id = ANY($1::text[])`, upstreamIDs)
		if queryErr != nil {
			return queryErr
		}
		selectedFiles := make(map[string]int)
		for selectedRows.Next() {
			var id string
			var fileID int
			if err = selectedRows.Scan(&id, &fileID); err != nil {
				selectedRows.Close()
				return err
			}
			selectedFiles[id] = fileID
		}
		err = selectedRows.Err()
		selectedRows.Close()
		if err != nil {
			return err
		}
		for i := range candidates {
			if candidates[i].SelectedMediaFileID == 0 {
				candidates[i].SelectedMediaFileID = selectedFiles[candidates[i].UpstreamSessionID]
			}
		}
	}
	winner, ids := legacyStaticDuplicateGroup(candidates)
	if len(ids) == 0 {
		// Another node may have committed the same repair while this request
		// waited for row locks. Refresh its tombstones before resolving again.
		_ = tx.Rollback(ctx)
		return d.loadByCompatToken(token)
	}
	// Preserve the records and their unknown JSON fields. Superseded rows
	// cannot be reconstructed or selected by a later Stopped report, even
	// after the canonical session ends. No artificial scrobble is emitted.
	_, err = tx.Exec(ctx, `UPDATE jellycompat_playback_sessions
		SET data = jsonb_set(jsonb_set(data, '{Terminal}', 'true'::jsonb, true),
		'{SupersededBy}', to_jsonb($3::text), true)
		WHERE compat_token = $1 AND id = ANY($2::text[])`, token, ids, winner)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	d.cacheMutationMu.RLock()
	defer d.cacheMutationMu.RUnlock()
	d.mem.supersedeStaticSessions(token, winner, ids)
	for _, id := range ids {
		d.clearPendingUpdates(id)
		d.clearUnpersisted(id)
		d.invalidateValidation(id, token)
		d.bumpCacheGenerations(id, token)
	}
	return nil
}
