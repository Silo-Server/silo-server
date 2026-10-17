package playback

// RouteMoveIdentityV3 is a process-local identity copied before an executor
// move. It carries no authority and never appears in recipes or public DTOs.
type RouteMoveIdentityV3 struct {
	native                   bool
	attempt, plan, replan    string
	work, profile            string
	userID, effectiveMediaID int
}

// NewRouteMoveIdentityV3 copies the identity before issuing a route mutation.
func NewRouteMoveIdentityV3(record *AttemptRecordV3, work string) RouteMoveIdentityV3 {
	identity := RouteMoveIdentityV3{work: work}
	if record != nil {
		identity.native = true
		identity.attempt, identity.plan, identity.replan = record.PlaybackAttemptID, record.CurrentPlanID, record.CurrentReplanRequestID
		identity.userID, identity.profile, identity.effectiveMediaID = record.UserID, record.ProfileID, record.EffectiveMediaFileID
	}
	return identity
}

// Matches checks the original attempt identity against a freshly locked row.
func (identity RouteMoveIdentityV3) Matches(record *AttemptRecordV3, work string) bool {
	return identity == NewRouteMoveIdentityV3(record, work)
}

// MarkPendingRouteMoveV3 binds this runtime to the exact attempted move.
func (s *TranscodeSession) MarkPendingRouteMoveV3(record *AttemptRecordV3, work string) {
	identity := NewRouteMoveIdentityV3(record, work)
	s.mu.Lock()
	s.pendingRouteMove = &identity
	s.mu.Unlock()
}

// PendingRouteMoveV3 returns an immutable copy, bounded by the runtime lifetime.
func (s *TranscodeSession) PendingRouteMoveV3() (RouteMoveIdentityV3, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRouteMove == nil {
		return RouteMoveIdentityV3{}, false
	}
	return *s.pendingRouteMove, true
}

// ConfirmRouteMoveV3 clears the marker after the exact runtime was adopted.
func (s *TranscodeSession) ConfirmRouteMoveV3() {
	s.mu.Lock()
	s.pendingRouteMove = nil
	s.mu.Unlock()
}
