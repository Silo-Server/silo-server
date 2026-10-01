package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

type AdminTaskMetricsService interface {
	GetMetrics(context.Context, int) (*models.MetadataRefreshMetrics, error)
}
type AdminTaskMetricCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}
type AdminTaskAttemptBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}
type AdminTaskDebtSample struct {
	TargetType    string   `json:"target_type"`
	ContentID     ID       `json:"content_id"`
	Title         string   `json:"title"`
	Type          string   `json:"type"`
	ReasonMask    int64    `json:"reason_mask"`
	NextRefreshAt Instant  `json:"next_refresh_at"`
	LastAttemptAt *Instant `json:"last_attempt_at,omitempty"`
	AttemptCount  int      `json:"attempt_count"`
	LastError     string   `json:"last_error"`
}
type AdminTaskMetrics struct {
	Total                int                      `json:"total"`
	Due                  int                      `json:"due"`
	Leased               int                      `json:"leased"`
	OldestDueAt          *Instant                 `json:"oldest_due_at,omitempty"`
	OldestLeaseExpiresAt *Instant                 `json:"oldest_lease_expires_at,omitempty"`
	ReasonCounts         []AdminTaskMetricCount   `json:"reason_counts"`
	AttemptBuckets       []AdminTaskAttemptBucket `json:"attempt_buckets"`
	DueSamples           []AdminTaskDebtSample    `json:"due_samples"`
	RecentErrors         []AdminTaskDebtSample    `json:"recent_errors"`
}
type AdminTaskMetricsOutput struct{ Body AdminTaskMetrics }

func (reg *Registry) getAdminTaskMetrics(ctx context.Context, in *AdminTaskInput) (*AdminTaskMetricsOutput, error) {
	if _, err := reg.taskInfo(in.Key); err != nil {
		return nil, err
	}
	if in.Key != "refresh_metadata" || reg.deps.AdminTaskMetrics == nil {
		return nil, NewProblem(TypeNotFound, "Task metrics not found")
	}
	m, err := reg.deps.AdminTaskMetrics.GetMetrics(ctx, 10)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := AdminTaskMetrics{Total: m.Total, Due: m.Due, Leased: m.Leased, OldestDueAt: instantPtr(m.OldestDueAt), OldestLeaseExpiresAt: instantPtr(m.OldestLeaseExpiresAt), ReasonCounts: []AdminTaskMetricCount{}, AttemptBuckets: []AdminTaskAttemptBucket{}, DueSamples: taskDebtSamples(m.DueSamples), RecentErrors: taskDebtSamples(m.RecentErrors)}
	for _, c := range m.ReasonCounts {
		out.ReasonCounts = append(out.ReasonCounts, AdminTaskMetricCount{Reason: c.Reason, Count: c.Count})
	}
	for _, c := range m.AttemptBuckets {
		out.AttemptBuckets = append(out.AttemptBuckets, AdminTaskAttemptBucket{Label: c.Label, Count: c.Count})
	}
	return &AdminTaskMetricsOutput{Body: out}, nil
}
func taskDebtSamples(samples []models.MetadataRefreshDebtSample) []AdminTaskDebtSample {
	out := make([]AdminTaskDebtSample, 0, len(samples))
	for _, s := range samples {
		safe := AdminTaskDebtSample{TargetType: s.TargetType, ContentID: ID(s.ContentID), Title: s.Title, Type: s.Type, ReasonMask: s.ReasonMask, NextRefreshAt: NewInstant(s.NextRefreshAt), LastAttemptAt: instantPtr(s.LastAttemptAt), AttemptCount: s.AttemptCount}
		if s.LastError != "" {
			safe.LastError = "Refresh failed. Inspect administrator diagnostics for details."
		}
		out = append(out, safe)
	}
	return out
}
