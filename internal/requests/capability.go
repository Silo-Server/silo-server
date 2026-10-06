package requests

import (
	"context"
	"errors"
)

// RequestCapabilityAllowed reports durable account permission independently of
// server configuration and consumed quota. The capability combines it with the
// configured state; mutations repeat policy checks and enforce capacity.
func (s *Service) RequestCapabilityAllowed(ctx context.Context, viewer Viewer) (bool, error) {
	if s.users == nil {
		return false, nil
	}
	if err := s.ensureViewerRequestsAllowed(ctx, viewer.UserID); err != nil {
		if errors.Is(err, ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	blocked, err := s.userLimitBlocked(ctx, viewer.UserID)
	if err != nil {
		return false, err
	}
	return !blocked, nil
}

// userLimitBlocked reports whether the account's request limit or approval
// mode is set to blocked.
func (s *Service) userLimitBlocked(ctx context.Context, userID int) (bool, error) {
	limit, err := s.store.GetUserLimit(ctx, userID)
	if err != nil {
		return false, err
	}
	return limit != nil && (limit.LimitMode == LimitModeBlocked || limit.ApprovalMode == ApprovalModeBlocked), nil
}
