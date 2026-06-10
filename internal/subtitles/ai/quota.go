package ai

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrQuotaExceeded is returned when a transcription request would exceed the
// per-user job quota, so handlers can map it to 429.
var ErrQuotaExceeded = errors.New("transcription quota exceeded")

// Quota period names, stored in subtitle_ai.transcribe_quota_period. Periods
// are rolling windows ending now, not calendar buckets, so there is no reset
// moment to game.
const (
	QuotaPeriodDay   = "day"
	QuotaPeriodWeek  = "week"
	QuotaPeriodMonth = "month"
)

// ValidQuotaPeriod reports whether period names a supported quota window.
// The settings write path and the config loader both validate against this,
// so the period vocabulary lives in one place.
func ValidQuotaPeriod(period string) bool {
	switch period {
	case QuotaPeriodDay, QuotaPeriodWeek, QuotaPeriodMonth:
		return true
	}
	return false
}

// QuotaPeriodWindow maps a period name to its rolling window length. Unknown
// values fall back to a day, matching the config loader's default.
func QuotaPeriodWindow(period string) time.Duration {
	switch period {
	case QuotaPeriodWeek:
		return 7 * 24 * time.Hour
	case QuotaPeriodMonth:
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// JobQuota is the per-user cap InsertJob enforces atomically with the insert,
// so concurrent requests cannot race past the limit. A nil *JobQuota means
// the insert is unconditional.
type JobQuota struct {
	UserID int
	Limit  int
	Since  time.Time
	Period string // window name, carried into QuotaExceededError for messages
}

// QuotaExceededError carries the usage details behind ErrQuotaExceeded.
type QuotaExceededError struct {
	Limit  int
	Used   int
	Period string
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("transcription limit reached: %d jobs per %s", e.Limit, e.Period)
}

func (e *QuotaExceededError) Unwrap() error { return ErrQuotaExceeded }

// QuotaStatus reports a user's transcription quota usage. Limited is false
// when no quota applies to the caller (quota disabled, or an exempt
// requester); the remaining fields are only meaningful when it is true.
type QuotaStatus struct {
	Limited   bool   `json:"limited"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
	Period    string `json:"period"`
}

// TranscribeQuota returns the transcription quota status for a user. Exempt
// callers always get an unlimited status. The count uses the same query the
// insert-time enforcement runs, so the number shown always matches the
// number enforced.
func (s *Service) TranscribeQuota(ctx context.Context, userID int, exempt bool) (QuotaStatus, error) {
	cfg := s.config()
	if exempt || cfg.TranscribeQuotaJobs <= 0 {
		return QuotaStatus{}, nil
	}
	used, err := s.repo.CountTranscribeJobsByUserSince(ctx, userID, quotaWindowStart(cfg))
	if err != nil {
		return QuotaStatus{}, err
	}
	remaining := cfg.TranscribeQuotaJobs - used
	if remaining < 0 {
		remaining = 0
	}
	return QuotaStatus{
		Limited:   true,
		Limit:     cfg.TranscribeQuotaJobs,
		Used:      used,
		Remaining: remaining,
		Period:    cfg.TranscribeQuotaPeriod,
	}, nil
}

// transcribeQuotaSpec returns the quota InsertJob must enforce for this
// request, or nil when none applies: translation-only jobs, exempt
// requesters, requests without an attributable user (internal callers), and
// servers with the quota disabled all pass through.
func (s *Service) transcribeQuotaSpec(req JobRequest) *JobQuota {
	if !req.Kind.IsTranscribe() || req.QuotaExempt || req.RequestedBy == nil {
		return nil
	}
	cfg := s.config()
	if cfg.TranscribeQuotaJobs <= 0 {
		return nil
	}
	return &JobQuota{
		UserID: *req.RequestedBy,
		Limit:  cfg.TranscribeQuotaJobs,
		Since:  quotaWindowStart(cfg),
		Period: cfg.TranscribeQuotaPeriod,
	}
}

func quotaWindowStart(cfg Config) time.Time {
	return time.Now().Add(-QuotaPeriodWindow(cfg.TranscribeQuotaPeriod))
}
