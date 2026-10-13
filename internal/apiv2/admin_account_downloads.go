package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/downloads"
)

// AdminAccountDownloadService reads one account's managed device downloads
// and series monitors (*downloads.Service).
type AdminAccountDownloadService interface {
	AdminListUserDownloadsPage(context.Context, int, downloads.AdminDownloadFilter, *downloads.RegistryPosition, int) ([]downloads.AdminDownloadRow, error)
	AdminUserDownloadSummary(context.Context, int) (downloads.AdminDownloadSummary, error)
	AdminListUserSubscriptionsPage(context.Context, int, downloads.AdminDownloadFilter, *downloads.RegistryPosition, int) ([]downloads.AdminSubscriptionRow, error)
}

// AdminUserDownloadsInput pages an account's managed rows, optionally for one
// profile or device.
type AdminUserDownloadsInput struct {
	ID ID `path:"id"`
	LimitParam
	Cursor    string `query:"cursor" maxLength:"8192" doc:"Opaque cursor from page.next_cursor"`
	ProfileID string `query:"profile_id" maxLength:"1024" doc:"Only rows of this household profile"`
	DeviceID  string `query:"device_id" maxLength:"128" doc:"Only rows of this device"`
}

// AdminUserDownloadEpisode places an episode download in its series.
type AdminUserDownloadEpisode struct {
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	Title         string `json:"title" doc:"Empty when unknown"`
}

// NullableAdminUserDownloadEpisode is an episode placement or an explicit
// null. Huma cannot mark a referenced object nullable, so the schema inlines
// the object, as Patch does for nullable request objects.
type NullableAdminUserDownloadEpisode struct {
	Valid bool
	Value AdminUserDownloadEpisode
}

// MarshalJSON renders null or the episode.
func (n NullableAdminUserDownloadEpisode) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return jsonNull, nil
	}
	return json.Marshal(n.Value)
}

// UnmarshalJSON accepts null or an episode.
func (n *NullableAdminUserDownloadEpisode) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), jsonNull) {
		*n = NullableAdminUserDownloadEpisode{}
		return nil
	}
	var v AdminUserDownloadEpisode
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*n = NullableAdminUserDownloadEpisode{Valid: true, Value: v}
	return nil
}

// Schema describes the field as a nullable episode object.
func (NullableAdminUserDownloadEpisode) Schema(r huma.Registry) *huma.Schema {
	s := r.Schema(reflect.TypeFor[AdminUserDownloadEpisode](), false, "")
	out := *s
	out.Nullable = true
	return &out
}

// AdminUserDownload is one managed download as an administrator sees it.
type AdminUserDownload struct {
	ID                ID                               `json:"id"`
	ProfileID         ID                               `json:"profile_id"`
	DeviceID          string                           `json:"device_id"`
	ContentID         string                           `json:"content_id" doc:"Movie, or the series for an episode"`
	EpisodeID         string                           `json:"episode_id,omitempty"`
	BatchID           ID                               `json:"batch_id,omitempty"`
	Title             string                           `json:"title" doc:"Catalog title of content_id; empty when the item left the catalog"`
	MediaType         string                           `json:"media_type" doc:"Catalog type of content_id (movie, series, …); empty when unknown"`
	Episode           NullableAdminUserDownloadEpisode `json:"episode" doc:"Null unless episode_id resolves to a catalog episode"`
	Status            string                           `json:"status" doc:"preparing, ready, downloading, completed, failed or revoked. More values may be added"`
	Quality           string                           `json:"quality"`
	EffectiveQuality  string                           `json:"effective_quality"`
	DeliveryFormat    string                           `json:"delivery_format"`
	TargetBitrateKbps int                              `json:"target_bitrate_kbps"`
	FileSize          int64                            `json:"file_size"`
	CreatedAt         Instant                          `json:"created_at"`
	UpdatedAt         Instant                          `json:"updated_at"`
	CompletedAt       NullableInstant                  `json:"completed_at"`
	StatusEventAt     NullableInstant                  `json:"status_event_at" doc:"Latest accepted client status report; null when the app never reported"`
}

type AdminUserDownloadsOutput struct {
	Body Collection[AdminUserDownload]
}

// AdminUserDownloadSummary totals an account's managed rows.
type AdminUserDownloadSummary struct {
	Total           int   `json:"total" minimum:"0" doc:"Managed rows that are not revoked"`
	Completed       int   `json:"completed" minimum:"0" doc:"Rows the app reported as completed"`
	InProgress      int   `json:"in_progress" minimum:"0" doc:"preparing, ready or downloading"`
	Failed          int   `json:"failed" minimum:"0"`
	Revoked         int   `json:"revoked" minimum:"0"`
	TotalBytes      int64 `json:"total_bytes" minimum:"0" doc:"file_size summed over non-revoked rows"`
	Devices         int   `json:"devices" minimum:"0" doc:"Distinct devices with a non-revoked row"`
	MonitoredSeries int   `json:"monitored_series" minimum:"0" doc:"Active series monitors"`
}

type AdminUserDownloadSummaryOutput struct {
	Body AdminUserDownloadSummary
}

// AdminUserDownloadSubscription is one series monitor as an administrator
// sees it, with the state of its episodes on the device.
type AdminUserDownloadSubscription struct {
	ID              ID      `json:"id"`
	ProfileID       ID      `json:"profile_id"`
	DeviceID        string  `json:"device_id"`
	SeriesID        string  `json:"series_id"`
	SeriesTitle     string  `json:"series_title" doc:"Empty when the series left the catalog"`
	Mode            string  `json:"mode" doc:"all, future, latest_season or specific_seasons. More values may be added"`
	SeasonNumbers   []int   `json:"season_numbers" doc:"Monitored seasons for specific_seasons; empty otherwise"`
	TargetSeason    *int    `json:"target_season" nullable:"true" doc:"First kept season for latest_season"`
	DeleteWatched   bool    `json:"delete_watched"`
	MaxStorageBytes int64   `json:"max_storage_bytes" minimum:"0" doc:"0 means no limit"`
	Active          bool    `json:"active"`
	OnDevice        int     `json:"on_device" minimum:"0" doc:"This device and profile's completed rows for the series"`
	InProgress      int     `json:"in_progress" minimum:"0" doc:"preparing, ready or downloading rows for the series"`
	RemovedEpisodes int     `json:"removed_episodes" minimum:"0" doc:"Episodes the user deleted from the monitored series (download_subscription_exclusions)"`
	CreatedAt       Instant `json:"created_at"`
	UpdatedAt       Instant `json:"updated_at"`
}

type AdminUserDownloadSubscriptionsOutput struct {
	Body Collection[AdminUserDownloadSubscription]
}

const (
	opListAdminUserDownloads             = "listAdminUserDownloads"
	opListAdminUserDownloadSubscriptions = "listAdminUserDownloadSubscriptions"
	adminUserDownloadsSort               = "created_at:desc"
	adminUserDownloadsTiebreaker         = "id:desc"
)

func nullableInstantOf(t *time.Time) NullableInstant {
	if t == nil {
		return NullableInstant{}
	}
	return NullableInstant{Valid: true, Time: NewInstant(*t)}
}

func adminUserDownloadOf(row downloads.AdminDownloadRow) AdminUserDownload {
	out := AdminUserDownload{
		ID: ID(row.ID), ProfileID: ID(row.ProfileID), DeviceID: row.DeviceID, ContentID: row.ContentID, EpisodeID: row.EpisodeID, BatchID: ID(row.BatchID),
		Title: row.Title, MediaType: row.MediaType, Status: row.Status, Quality: row.Quality, EffectiveQuality: row.EffectiveQuality,
		DeliveryFormat: row.Format, TargetBitrateKbps: row.TargetBitrateKbps, FileSize: row.FileSize,
		CreatedAt: NewInstant(row.CreatedAt), UpdatedAt: NewInstant(row.UpdatedAt),
		CompletedAt: nullableInstantOf(row.CompletedAt), StatusEventAt: nullableInstantOf(row.StatusEventAt),
	}
	if row.SeasonNumber != nil && row.EpisodeNumber != nil {
		out.Episode = NullableAdminUserDownloadEpisode{Valid: true, Value: AdminUserDownloadEpisode{SeasonNumber: *row.SeasonNumber, EpisodeNumber: *row.EpisodeNumber, Title: row.EpisodeTitle}}
	}
	return out
}

func adminUserDownloadSubscriptionOf(row downloads.AdminSubscriptionRow) AdminUserDownloadSubscription {
	return AdminUserDownloadSubscription{
		ID: ID(row.ID), ProfileID: ID(row.ProfileID), DeviceID: row.DeviceID, SeriesID: row.SeriesID, SeriesTitle: row.SeriesTitle,
		Mode: row.Mode, SeasonNumbers: NonNil(row.SeasonNumbers), TargetSeason: row.TargetSeason, DeleteWatched: row.DeleteWatched,
		MaxStorageBytes: row.MaxStorageBytes, Active: row.Active, OnDevice: row.OnDevice, InProgress: row.InProgress, RemovedEpisodes: row.RemovedEpisodes,
		CreatedAt: NewInstant(row.CreatedAt), UpdatedAt: NewInstant(row.UpdatedAt),
	}
}

// adminUserDownloadsScope binds a page cursor to the operation, the acting
// administrator, the account, both filters and the page size.
func adminUserDownloadsScope(ctx context.Context, operation string, id int, filter downloads.AdminDownloadFilter, limit int) CursorScope {
	return CursorScope{
		OperationID: operation,
		Security:    strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx),
		Filter:      fmt.Sprintf("%d/%q/%q/%d", id, filter.ProfileID, filter.DeviceID, limit),
		Sort:        adminUserDownloadsSort,
		Tiebreaker:  adminUserDownloadsTiebreaker,
	}
}

// adminUserDownloadsPage resolves the account, filter and cursor position a
// paged account-download read shares.
func (reg *Registry) adminUserDownloadsPage(ctx context.Context, cursors *Cursors, operation string, in *AdminUserDownloadsInput) (int, downloads.AdminDownloadFilter, *downloads.RegistryPosition, CursorScope, *Problem) {
	filter := downloads.AdminDownloadFilter{ProfileID: strings.TrimSpace(in.ProfileID), DeviceID: strings.TrimSpace(in.DeviceID)}
	if reg.deps.AdminAccountDownloads == nil {
		return 0, filter, nil, CursorScope{}, unavailable("account downloads")
	}
	id, p := reg.adminAccountTarget(ctx, in.ID)
	if p != nil {
		return 0, filter, nil, CursorScope{}, p
	}
	scope := adminUserDownloadsScope(ctx, operation, id, filter, in.Limit)
	var after *downloads.RegistryPosition
	if in.Cursor != "" {
		after = new(downloads.RegistryPosition)
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return 0, filter, nil, scope, p
		}
		if after.ID == "" || after.CreatedAt.IsZero() {
			return 0, filter, nil, scope, NewProblem(TypeInvalidCursor, "The cursor position is invalid.")
		}
	}
	return id, filter, after, scope, nil
}

// adminUserDownloadsCursor encodes the cursor that resumes after a page's
// last row.
func adminUserDownloadsCursor(cursors *Cursors, scope CursorScope, createdAt time.Time, id string) (string, *Problem) {
	next, err := cursors.Encode(scope, downloads.RegistryPosition{CreatedAt: createdAt, ID: id})
	if err != nil {
		return "", NewProblem(TypeInternalError, "Unable to encode cursor.")
	}
	return next, nil
}

// registerAdminAccountDownloads registers the read-only views of one
// account's managed device downloads and series monitors.
func registerAdminAccountDownloads(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	list := adminAccountOperation(http.MethodGet, "/{id}/downloads", opListAdminUserDownloads, false)
	list.Summary = "List an account's managed device downloads."
	list.Description = "Every managed (device) download row of the account in every status, newest first. Ephemeral web downloads are not listed. A row the app never reported on stays ready."
	Register(reg, list, func(ctx context.Context, in *AdminUserDownloadsInput) (*AdminUserDownloadsOutput, error) {
		id, filter, after, scope, p := reg.adminUserDownloadsPage(ctx, cursors, opListAdminUserDownloads, in)
		if p != nil {
			return nil, p
		}
		rows, err := reg.deps.AdminAccountDownloads.AdminListUserDownloadsPage(ctx, id, filter, after, in.Limit+1)
		if err != nil {
			return nil, downloadProblem(err)
		}
		next := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			if next, p = adminUserDownloadsCursor(cursors, scope, last.CreatedAt, last.ID); p != nil {
				return nil, p
			}
		}
		items := make([]AdminUserDownload, 0, len(rows))
		for _, row := range rows {
			items = append(items, adminUserDownloadOf(row))
		}
		return &AdminUserDownloadsOutput{Body: Paginated(items, next)}, nil
	})
	summary := adminAccountOperation(http.MethodGet, "/{id}/downloads/summary", "getAdminUserDownloadSummary", false)
	summary.Summary = "Total an account's managed device downloads."
	Register(reg, summary, func(ctx context.Context, in *AdminAccountInput) (*AdminUserDownloadSummaryOutput, error) {
		if reg.deps.AdminAccountDownloads == nil {
			return nil, unavailable("account downloads")
		}
		id, p := reg.adminAccountTarget(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		s, err := reg.deps.AdminAccountDownloads.AdminUserDownloadSummary(ctx, id)
		if err != nil {
			return nil, downloadProblem(err)
		}
		return &AdminUserDownloadSummaryOutput{Body: AdminUserDownloadSummary(s)}, nil
	})
	subs := adminAccountOperation(http.MethodGet, "/{id}/download-subscriptions", opListAdminUserDownloadSubscriptions, false)
	subs.Summary = "List an account's series download monitors."
	Register(reg, subs, func(ctx context.Context, in *AdminUserDownloadsInput) (*AdminUserDownloadSubscriptionsOutput, error) {
		id, filter, after, scope, p := reg.adminUserDownloadsPage(ctx, cursors, opListAdminUserDownloadSubscriptions, in)
		if p != nil {
			return nil, p
		}
		rows, err := reg.deps.AdminAccountDownloads.AdminListUserSubscriptionsPage(ctx, id, filter, after, in.Limit+1)
		if err != nil {
			if errors.Is(err, downloads.ErrSubscriptionsUnavailable) {
				return nil, unavailable("download subscriptions")
			}
			return nil, downloadProblem(err)
		}
		next := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			last := rows[len(rows)-1]
			if next, p = adminUserDownloadsCursor(cursors, scope, last.CreatedAt, last.ID); p != nil {
				return nil, p
			}
		}
		items := make([]AdminUserDownloadSubscription, 0, len(rows))
		for _, row := range rows {
			items = append(items, adminUserDownloadSubscriptionOf(row))
		}
		return &AdminUserDownloadSubscriptionsOutput{Body: Paginated(items, next)}, nil
	})
}
