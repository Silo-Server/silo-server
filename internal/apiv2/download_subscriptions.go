package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/downloads"
)

type DownloadSubscriptionService interface {
	ListSubscriptionsPage(context.Context, int, string, string, *downloads.RegistryPosition, int) ([]*downloads.Subscription, error)
	GetSubscription(context.Context, int, string, string, string) (*downloads.Subscription, error)
}

type DownloadSubscription struct {
	ID              ID      `json:"id"`
	SeriesID        string  `json:"series_id"`
	Mode            string  `json:"mode"`
	SeasonNumbers   []int   `json:"season_numbers"`
	TargetSeason    *int    `json:"target_season,omitempty"`
	DeleteWatched   bool    `json:"delete_watched"`
	MaxStorageBytes int64   `json:"max_storage_bytes"`
	Active          bool    `json:"active"`
	CreatedAt       Instant `json:"created_at"`
	UpdatedAt       Instant `json:"updated_at"`
	ETag            string  `json:"etag" doc:"Current validator for this device's subscription."`
}
type DownloadSubscriptionOutput struct {
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         DownloadSubscription
}
type DownloadSubscriptionsOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         Collection[DownloadSubscription]
}
type DownloadSubscriptionsInput struct {
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
	Limit    int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Cursor   string `query:"cursor"`
}
type DownloadSubscriptionInput struct {
	ID       string `path:"id" minLength:"1"`
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
}

func downloadSubscriptionOf(row *downloads.Subscription) DownloadSubscription {
	// Hash the full persisted row, including identity and database timestamp
	// precision. Millisecond wire timestamps alone cannot distinguish rapid edits.
	encoded, _ := json.Marshal(row)
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(encoded))
	return DownloadSubscription{ID: ID(row.ID), SeriesID: row.SeriesID, Mode: row.Mode, SeasonNumbers: append([]int{}, row.SeasonNumbers...), TargetSeason: row.TargetSeason, DeleteWatched: row.DeleteWatched, MaxStorageBytes: row.MaxStorageBytes, Active: row.Active, CreatedAt: NewInstant(row.CreatedAt), UpdatedAt: NewInstant(row.UpdatedAt), ETag: tag}
}

func registerDownloadSubscriptions(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/downloads/subscriptions", "listDownloadSubscriptions", "downloads", "Page the calling device's series monitors, including paused monitors."), Class: ClassProfileScoped, ServiceBacked: true}, func(ctx context.Context, in *DownloadSubscriptionsInput) (*DownloadSubscriptionsOutput, error) {
		return reg.listDownloadSubscriptions(ctx, cursors, in)
	})
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/downloads/subscriptions/{id}", "getDownloadSubscription", "downloads", "Read a series monitor and its current validator for this device."), Class: ClassProfileScoped, ServiceBacked: true}, reg.getDownloadSubscription)
}
func (reg *Registry) listDownloadSubscriptions(ctx context.Context, cursors *Cursors, in *DownloadSubscriptionsInput) (*DownloadSubscriptionsOutput, error) {
	if reg.deps.DownloadSubscriptions == nil {
		return nil, unavailable("download subscriptions")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{OperationID: "listDownloadSubscriptions", Security: strconv.Itoa(user) + "/" + profile + "/" + viewerScopeDigest(ctx), Filter: in.DeviceID, Sort: "-created_at,-id", Tiebreaker: "id"}
	var after *downloads.RegistryPosition
	if in.Cursor != "" {
		after = &downloads.RegistryPosition{}
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	rows, err := reg.deps.DownloadSubscriptions.ListSubscriptionsPage(ctx, user, profile, in.DeviceID, after, in.Limit+1)
	if err != nil {
		return nil, downloadProblem(err)
	}
	next := ""
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		next, err = cursors.Encode(scope, downloads.RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return nil, serviceProblem(err)
		}
	}
	items := make([]DownloadSubscription, 0, len(rows))
	for _, row := range rows {
		items = append(items, downloadSubscriptionOf(row))
	}
	return &DownloadSubscriptionsOutput{CacheControl: "private, no-cache", Body: Paginated(items, next)}, nil
}
func (reg *Registry) getDownloadSubscription(ctx context.Context, in *DownloadSubscriptionInput) (*DownloadSubscriptionOutput, error) {
	if reg.deps.DownloadSubscriptions == nil {
		return nil, unavailable("download subscriptions")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	row, err := reg.deps.DownloadSubscriptions.GetSubscription(ctx, user, profile, in.DeviceID, in.ID)
	if err != nil {
		return nil, downloadProblem(err)
	}
	out := downloadSubscriptionOf(row)
	return &DownloadSubscriptionOutput{ETag: out.ETag, CacheControl: "private, no-cache", Body: out}, nil
}
