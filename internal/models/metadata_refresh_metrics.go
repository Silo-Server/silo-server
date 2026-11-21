package models

import "time"

type MetadataRefreshReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type MetadataRefreshAttemptBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type MetadataRefreshDebtSample struct {
	TargetType    string     `json:"target_type"`
	ContentID     string     `json:"content_id"`
	Title         string     `json:"title"`
	Type          string     `json:"type"`
	SeriesTitle   string     `json:"series_title,omitempty"`
	SeasonNumber  *int       `json:"season_number,omitempty"`
	EpisodeNumber *int       `json:"episode_number,omitempty"`
	ReasonMask    int64      `json:"reason_mask"`
	NextRefreshAt time.Time  `json:"next_refresh_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	AttemptCount  int        `json:"attempt_count"`
	LastError     string     `json:"last_error"`
}

type MetadataRefreshMetrics struct {
	Total                int                            `json:"total"`
	Due                  int                            `json:"due"`
	Leased               int                            `json:"leased"`
	OldestDueAt          *time.Time                     `json:"oldest_due_at,omitempty"`
	OldestLeaseExpiresAt *time.Time                     `json:"oldest_lease_expires_at,omitempty"`
	ReasonCounts         []MetadataRefreshReasonCount   `json:"reason_counts"`
	AttemptBuckets       []MetadataRefreshAttemptBucket `json:"attempt_buckets"`
	DueSamples           []MetadataRefreshDebtSample    `json:"due_samples"`
	RecentErrors         []MetadataRefreshDebtSample    `json:"recent_errors"`
}
