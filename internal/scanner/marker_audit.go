package scanner

import (
	"context"
	"time"
)

type MarkerAuditContext struct {
	UserID             *int
	ImpersonatorUserID *int
	APIKeyID           *int64
	RequestID          string
	ClientIP           string
	UserAgent          string
}

type markerAuditContextKey struct{}

func WithMarkerAuditContext(ctx context.Context, audit MarkerAuditContext) context.Context {
	return context.WithValue(ctx, markerAuditContextKey{}, audit)
}

func MarkerAuditContextFromContext(ctx context.Context) (MarkerAuditContext, bool) {
	audit, ok := ctx.Value(markerAuditContextKey{}).(MarkerAuditContext)
	return audit, ok
}

type MarkerAuditSegment struct {
	Start      *float64   `json:"start"`
	End        *float64   `json:"end"`
	Source     *string    `json:"source"`
	Provider   *string    `json:"provider"`
	Confidence *float64   `json:"confidence"`
	Algorithm  *string    `json:"algorithm"`
	DetectedAt *time.Time `json:"detected_at"`
}

type MarkerEditAuditRow struct {
	ID                   int64
	MediaFileID          int
	ItemID               *string
	ItemType             *string
	MediaTitle           *string
	FilePath             *string
	SegmentKind          string
	Action               string
	Before               *MarkerAuditSegment
	After                *MarkerAuditSegment
	UserID               *int
	Username             *string
	ImpersonatorUserID   *int
	ImpersonatorUsername *string
	APIKeyID             *int64
	RequestID            *string
	ClientIP             *string
	UserAgent            *string
	CreatedAt            time.Time
}
