package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type DownloadRegistryService interface {
	Capability(context.Context, int) (downloads.Capability, error)
	ListPage(context.Context, int, string, string, *downloads.RegistryPosition, int) ([]*downloads.Download, error)
	ReportStatus(context.Context, int, string, string, string, downloads.StatusEvent) (*downloads.Download, error)
	Delete(context.Context, int, string, string, string) error
}
type DownloadEntry struct {
	ID                ID       `json:"id"`
	ContentID         string   `json:"content_id"`
	EpisodeID         string   `json:"episode_id,omitempty"`
	BatchID           ID       `json:"batch_id,omitempty"`
	DeviceID          ID       `json:"device_id,omitempty"`
	MediaFileID       ID       `json:"media_file_id"`
	FileSize          int64    `json:"file_size"`
	BytesSent         int64    `json:"bytes_sent"`
	Kind              string   `json:"kind"`
	Status            string   `json:"status"`
	Quality           string   `json:"quality"`
	EffectiveQuality  string   `json:"effective_quality"`
	DeliveryFormat    string   `json:"delivery_format"`
	TargetBitrateKbps int      `json:"target_bitrate_kbps"`
	Revision          int      `json:"revision"`
	CreatedAt         Instant  `json:"created_at"`
	CompletedAt       *Instant `json:"completed_at,omitempty"`
	StatusEventAt     *Instant `json:"status_event_at,omitempty" doc:"Latest accepted client status event time for this revision."`
}
type DownloadEntryOutput struct{ Body DownloadEntry }
type DownloadRegistryOutput struct{ Body Collection[DownloadEntry] }
type DownloadRegistryInput struct {
	DeviceID string `header:"X-Silo-Device-Id" maxLength:"128"`
	Limit    int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Cursor   string `query:"cursor"`
}
type DownloadStatusBody struct {
	Status    string  `json:"status" enum:"downloading,completed"`
	UpdatedAt Instant `json:"updated_at" doc:"Time of the local status event; retain on retry. Future times are rejected."`
	Revision  int     `json:"revision" minimum:"1" doc:"Registry revision whose bytes this event describes; retain on retry."`
}
type DownloadStatusInput struct {
	ID       string `path:"id" minLength:"1"`
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
	Body     DownloadStatusBody
}
type DownloadDeleteInput struct {
	ID       string `path:"id" minLength:"1"`
	DeviceID string `header:"X-Silo-Device-Id" maxLength:"128"`
}
type DownloadCapability struct {
	Capability
	BoundedCreation         bool     `json:"bounded_creation"`
	SubscriptionMutations   bool     `json:"subscription_mutations"`
	BoundedSubscriptionSync bool     `json:"bounded_subscription_sync"`
	SubscriptionReads       bool     `json:"subscription_reads"`
	BoundedManifests        bool     `json:"bounded_manifests"`
	FileDelivery            bool     `json:"file_delivery"`
	Enabled                 bool     `json:"enabled"`
	DownloadAllowed         bool     `json:"download_allowed"`
	ProxyDelivery           bool     `json:"proxy_delivery"`
	OrderedStatus           bool     `json:"ordered_status"`
	QualityPresets          []string `json:"quality_presets"`
	TranscodeEnabled        bool     `json:"transcode_enabled"`
	TranscodeUserAllowed    bool     `json:"transcode_user_allowed"`
	SeasonDownload          bool     `json:"season_download"`
	SeriesMonitoring        bool     `json:"series_monitoring"`
	MonitoringModes         []string `json:"monitoring_modes"`
}
type DownloadCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         DownloadCapability
}

func registerDownloadRegistry(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/downloads", "listDownloads", "downloads", "Page device-managed downloads or account ephemeral downloads."), Class: ClassProfileScoped, ServiceBacked: true}, func(ctx context.Context, in *DownloadRegistryInput) (*DownloadRegistryOutput, error) {
		return reg.listDownloads(ctx, cursors, in)
	})
	op := Operation{Operation: humaOp(http.MethodPatch, Prefix+"/downloads/{id}", "reportDownloadStatus", "downloads", "Record a revision-bound local status event; older or equal events return current state."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyDomainIdentity}
	op.MaxBodyBytes = 4096
	op.Errors = []int{409}
	Register(reg, op, reg.reportDownloadStatus)
	remove := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/downloads/{id}", "deleteDownload", "downloads", "Remove a managed download or cancel an ephemeral transfer."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNaturalIdempotent}
	remove.DefaultStatus = 204
	Register(reg, remove, reg.deleteDownload)
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/capabilities/downloads", "getDownloadCapability", "downloads", "Discover download policy and ordered registry status support."), Class: ClassProfileScoped, ServiceBacked: true}, reg.getDownloadCapability)
}
func downloadEntryOf(row *downloads.Download) DownloadEntry {
	out := DownloadEntry{ID: ID(row.ID), ContentID: row.ContentID, EpisodeID: row.EpisodeID, BatchID: ID(row.BatchID), DeviceID: ID(row.DeviceID), MediaFileID: ID(strconv.Itoa(row.MediaFileID)), FileSize: row.FileSize, BytesSent: row.BytesSent, Kind: row.Kind, Status: row.Status, Quality: row.Quality, EffectiveQuality: row.EffectiveQuality, DeliveryFormat: row.Format, TargetBitrateKbps: row.TargetBitrateKbps, Revision: row.Revision, CreatedAt: NewInstant(row.CreatedAt)}
	if row.CompletedAt != nil {
		out.CompletedAt = new(NewInstant(*row.CompletedAt))
	}
	if row.StatusEventAt != nil {
		out.StatusEventAt = new(NewInstant(*row.StatusEventAt))
	}
	return out
}
func downloadProblem(err error) *Problem {
	switch {
	case errors.Is(err, downloads.ErrNotFound), errors.Is(err, downloads.ErrSubscriptionNotFound), errors.Is(err, downloads.ErrAssetNotFound), errors.Is(err, catalogpkg.ErrItemNotFound):
		return NewProblem(TypeNotFound, "Download not found.")
	case errors.Is(err, downloads.ErrDownloadNotActive):
		return NewProblem(TypeConflict, "The download is no longer active.")
	case errors.Is(err, downloads.ErrFeatureDisabled), errors.Is(err, downloads.ErrDownloadNotAllowed):
		return NewProblem(TypePermissionDenied, "Downloads are not allowed.")
	case errors.Is(err, downloads.ErrManifestUnavailable), errors.Is(err, downloads.ErrSubscriptionsUnavailable):
		return NewProblem(TypeDependencyUnavailable, "Offline assets are not configured.")
	case errors.Is(err, downloads.ErrInvalidSubtitleRef):
		return NewProblem(TypeMalformedRequest, "Invalid subtitle reference.")
	case errors.Is(err, downloads.ErrStatusConflict):
		return NewProblem(TypeConflict, "The download revision changed; reload the registry before reporting new events.")
	case errors.Is(err, downloads.ErrInvalidStatus), errors.Is(err, downloads.ErrInvalidStatusEvent), errors.Is(err, downloads.ErrProfileRequired):
		return NewProblem(TypeMalformedRequest, err.Error())
	default:
		return serviceProblem(err)
	}
}
func (reg *Registry) listDownloads(ctx context.Context, cursors *Cursors, in *DownloadRegistryInput) (*DownloadRegistryOutput, error) {
	if reg.deps.Downloads == nil {
		return nil, unavailable("downloads")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{OperationID: "listDownloads", Security: strconv.Itoa(user) + "/" + profile + "/" + viewerScopeDigest(ctx), Filter: in.DeviceID, Sort: "-created_at,-id", Tiebreaker: "id"}
	var after *downloads.RegistryPosition
	if in.Cursor != "" {
		after = &downloads.RegistryPosition{}
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	rows, err := reg.deps.Downloads.ListPage(ctx, user, profile, in.DeviceID, after, in.Limit+1)
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
	items := make([]DownloadEntry, 0, len(rows))
	for _, row := range rows {
		items = append(items, downloadEntryOf(row))
	}
	return &DownloadRegistryOutput{Body: Paginated(items, next)}, nil
}
func (reg *Registry) reportDownloadStatus(ctx context.Context, in *DownloadStatusInput) (*DownloadEntryOutput, error) {
	if reg.deps.Downloads == nil {
		return nil, unavailable("downloads")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	row, err := reg.deps.Downloads.ReportStatus(ctx, user, profile, in.DeviceID, in.ID, downloads.StatusEvent{Status: in.Body.Status, UpdatedAt: in.Body.UpdatedAt.Time, Revision: in.Body.Revision})
	if err != nil {
		return nil, downloadProblem(err)
	}
	return &DownloadEntryOutput{Body: downloadEntryOf(row)}, nil
}
func (reg *Registry) deleteDownload(ctx context.Context, in *DownloadDeleteInput) (*struct{}, error) {
	if reg.deps.Downloads == nil {
		return nil, unavailable("downloads")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.Downloads.Delete(ctx, user, profile, in.DeviceID, in.ID); err != nil {
		return nil, downloadProblem(err)
	}
	return &struct{}{}, nil
}
func (reg *Registry) getDownloadCapability(ctx context.Context, _ *CapabilityInput) (*DownloadCapabilityOutput, error) {
	out := DownloadCapability{Capability: Capability{State: StateNotConfigured}, QualityPresets: []string{}, MonitoringModes: []string{}}
	if reg.deps.Downloads != nil {
		user, _, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		view, err := reg.deps.Downloads.Capability(ctx, user)
		if err != nil {
			return nil, downloadProblem(err)
		}
		out.Enabled = view.Enabled
		out.DownloadAllowed = view.DownloadAllowed
		out.OrderedStatus = true
		out.FileDelivery = reg.deps.DownloadDelivery != nil
		out.BoundedManifests = reg.deps.DownloadManifests != nil
		out.SubscriptionReads = reg.deps.DownloadSubscriptions != nil
		out.SubscriptionMutations = reg.deps.DownloadSubscriptionMutations != nil
		out.BoundedCreation = reg.deps.DownloadCreation != nil
		out.BoundedSubscriptionSync = reg.deps.DownloadSubscriptionSync != nil
		if reg.deps.DownloadProxyDelivery != nil {
			out.ProxyDelivery = reg.deps.DownloadProxyDelivery()
		}
		out.QualityPresets = append([]string{}, view.QualityPresets...)
		out.MonitoringModes = append([]string{}, view.MonitoringModes...)
		out.TranscodeEnabled = view.TranscodeEnabled
		out.TranscodeUserAllowed = view.TranscodeUserAllowed
		out.SeasonDownload = view.SeasonDownload
		out.SeriesMonitoring = view.SeriesMonitoring
		out.State = enabledCapabilityState(view.Enabled)
		// Demo mode refuses download creation, deletion and subscription
		// mutations to non-admins, so the effective answer is no even when
		// the account itself is permitted. Discovered the same way as every
		// other demo-restricted capability (notification email verification).
		out.Allowed = new(view.DownloadAllowed && !demoRestricted(ctx, reg.deps.DemoSettings))
	}
	return &DownloadCapabilityOutput{CacheControl: "private, no-cache", Body: out}, nil
}
