package playback

// BindTranslation captures the requesting viewer's local playback identity for
// a new AI job. Persisted job execution and remote session discovery are not
// implied. An inaccessible session is indistinguishable from a missing session.
func (n *SubtitleReadyNotifier) BindTranslation(accountID int, profileID, sessionID string, fileID int) (*SubtitleReadyNotifier, error) {
	if n == nil || n.sessions == nil || accountID <= 0 || profileID == "" || sessionID == "" || fileID <= 0 {
		return nil, ErrSessionNotFound
	}
	for _, session := range n.sessions.GetSessionsByMediaFileID(fileID) {
		if session == nil || session.ID != sessionID || session.UserID != accountID || session.ProfileID != profileID {
			continue
		}
		bound := *n
		captured := *session
		bound.translationSession = &captured
		return &bound, nil
	}
	return nil, ErrSessionNotFound
}

// translationSessionMatches is checked immediately before building/sending a
// live event. It does not hold a manager lock over a potentially slow socket
// write, revoke already delivered cues, or authorize media delivery grants.
func (n *SubtitleReadyNotifier) translationSessionMatches(sessionID string, fileID int) bool {
	captured := n.translationSession
	if captured == nil {
		return true
	} // Frozen bridge behavior.
	if captured.ID != sessionID || (captured.MediaFileID != fileID && captured.RequestedMediaFileID != fileID) {
		return false
	}
	for _, current := range n.sessions.GetSessionsByMediaFileID(fileID) {
		if current == nil || current.ID != captured.ID {
			continue
		}
		if current.UserID != captured.UserID || current.ProfileID != captured.ProfileID || current.MediaFileID != captured.MediaFileID || current.RequestedMediaFileID != captured.RequestedMediaFileID || !current.StartedAt.Equal(captured.StartedAt) {
			return false
		}
		return true
	}
	return false
}
