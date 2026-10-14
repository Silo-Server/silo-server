package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsync"
)

// SubtitleSyncService aligns stored subtitles and sidecars to their file's
// audio.
type SubtitleSyncService interface {
	Request(ctx context.Context, subtitleID int, trigger string, requestedBy *int) (*subsync.Job, error)
	RequestExternal(ctx context.Context, mediaFileID int, sidecar models.ExternalSubtitle, trigger string, requestedBy *int) (*subsync.Job, error)
	Latest(ctx context.Context, subtitleID int) (*subsync.Job, error)
	LatestExternal(ctx context.Context, timingID int64) (*subsync.Job, error)
	LatestForSubtitles(ctx context.Context, subtitleIDs []int) (map[int]*subsync.Job, error)
	LatestForExternal(ctx context.Context, timingIDs []int64) (map[int64]*subsync.Job, error)
	AutoSyncEnabled(ctx context.Context) bool
	TimingChanged(ctx context.Context, target subtitles.SyncTarget)
}

// ExternalTimingStore keeps sidecar timing corrections.
type ExternalTimingStore interface {
	subtitles.ExternalTimingLookup
	ExternalTimings(ctx context.Context, mediaFileID int) (map[string]*subtitles.ExternalTiming, error)
	SetExternalTiming(ctx context.Context, mediaFileID int, contentSHA256, path string, format subtitles.SubtitleFormat, timing subtitles.Timing, revision int64) (*subtitles.ExternalTiming, error)
}

// SetSyncService enables subtitle sync, including automatic sync of new
// downloads and uploads. external stores sidecar corrections; nil leaves
// sidecars out of sync.
func (h *SubtitleSearchHandler) SetSyncService(sync SubtitleSyncService, external ExternalTimingStore) {
	h.sync, h.external = sync, external
}

// SyncAvailable reports whether subtitle sync is configured.
func (h *SubtitleSearchHandler) SyncAvailable() bool { return h != nil && h.sync != nil }

// ExternalSyncAvailable reports whether sidecars can be synced and retimed.
func (h *SubtitleSearchHandler) ExternalSyncAvailable() bool {
	return h.SyncAvailable() && h.external != nil
}

// AutoSyncEnabled reports whether new subtitles are synced automatically.
func (h *SubtitleSearchHandler) AutoSyncEnabled(ctx context.Context) bool {
	return h.SyncAvailable() && h.sync.AutoSyncEnabled(ctx)
}

// requestAutoSync starts automatic sync of a newly stored subtitle. It is
// best effort: storing the subtitle already succeeded.
func (h *SubtitleSearchHandler) requestAutoSync(ctx context.Context, sub *subtitles.DownloadedSubtitle) {
	if !h.SyncAvailable() || sub == nil {
		return
	}
	if _, err := h.sync.Request(context.WithoutCancel(ctx), sub.ID, subsync.TriggerAuto, sub.DownloadedBy); err != nil {
		slog.WarnContext(ctx, "automatic subtitle sync not started", "component", "api", "subtitle_id", sub.ID, "error", err)
	}
}

// storedSubtitleForSync reads a stored subtitle the viewer can play. Anyone
// with access to the file may sync or retime it: the stored bytes never
// change, and the correction can always be reset.
func (h *SubtitleSearchHandler) storedSubtitleForSync(ctx context.Context, access catalog.AccessFilter, id int) (*subtitles.DownloadedSubtitle, error) {
	if !h.SyncAvailable() || h.repo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle sync is not configured")
	}
	row, err := h.repo.GetDownloadedSubtitle(ctx, id)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read stored subtitle")
	}
	if row == nil {
		return nil, subtitles.ErrSubtitleNotFound
	}
	if err := h.authorizeSubtitleRead(ctx, access, row.MediaFileID); err != nil {
		return nil, err
	}
	return row, nil
}

// RequestStoredSubtitleSync starts a manual sync of a subtitle the viewer
// can play.
func (h *SubtitleSearchHandler) RequestStoredSubtitleSync(ctx context.Context, access catalog.AccessFilter, id int) (*subsync.Job, error) {
	row, err := h.storedSubtitleForSync(ctx, access, id)
	if err != nil {
		return nil, err
	}
	job, err := h.sync.Request(ctx, row.ID, subsync.TriggerManual, new(access.UserID))
	switch {
	case errors.Is(err, subsync.ErrUnsupportedFormat):
		return nil, apiError(http.StatusUnprocessableEntity, "unsupported_format", "This subtitle format cannot be synced")
	case errors.Is(err, subsync.ErrSubtitleNotFound):
		return nil, subtitles.ErrSubtitleNotFound
	case err != nil:
		slog.ErrorContext(ctx, "subtitle sync request failed", "component", "api", "subtitle_id", id, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to start subtitle sync")
	}
	return job, nil
}

// StoredSubtitleSync reads a stored subtitle and its latest sync job with
// file access.
func (h *SubtitleSearchHandler) StoredSubtitleSync(ctx context.Context, access catalog.AccessFilter, id int) (*subtitles.DownloadedSubtitle, *subsync.Job, error) {
	row, err := h.storedSubtitleForSync(ctx, access, id)
	if err != nil {
		return nil, nil, err
	}
	job, err := h.sync.Latest(ctx, row.ID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read subtitle sync")
	}
	return row, job, nil
}

// SubtitleSyncJobs returns the latest sync job of each subtitle; the caller
// already authorized the subtitles. It is empty when sync is unavailable.
func (h *SubtitleSearchHandler) SubtitleSyncJobs(ctx context.Context, ids []int) map[int]*subsync.Job {
	if !h.SyncAvailable() || len(ids) == 0 {
		return nil
	}
	jobs, err := h.sync.LatestForSubtitles(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "subtitle sync state unavailable", "component", "api", "error", err)
		return nil
	}
	return jobs
}

// SetStoredSubtitleTiming replaces a stored subtitle's timing correction at
// the given revision. File access is enough, as for sync.
func (h *SubtitleSearchHandler) SetStoredSubtitleTiming(ctx context.Context, access catalog.AccessFilter, id int, revision int64, timing subtitles.Timing) (*subtitles.DownloadedSubtitle, error) {
	if err := subtitles.ValidateTiming(timing); err != nil {
		return nil, apiError(http.StatusUnprocessableEntity, "validation_failed", err.Error())
	}
	if h.manager == nil {
		return nil, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle sync is not configured")
	}
	row, err := h.storedSubtitleForSync(ctx, access, id)
	if err != nil {
		return nil, err
	}
	if row.Revision != revision {
		return nil, &subtitles.SubtitleRevisionConflict{Current: row}
	}
	updated, err := h.manager.UpdateDownloadedSubtitleWithRevision(ctx, id, subtitles.SubtitleMetadataPatch{Timing: &timing}, &revision)
	switch {
	case errors.Is(err, subtitles.ErrTimingUnsupported):
		return nil, apiError(http.StatusUnprocessableEntity, "unsupported_format", "This subtitle format cannot be retimed")
	case err != nil:
		var conflict *subtitles.SubtitleRevisionConflict
		if errors.As(err, &conflict) || errors.Is(err, subtitles.ErrSubtitleNotFound) {
			return nil, err
		}
		slog.ErrorContext(ctx, "subtitle timing update failed", "component", "api", "subtitle_id", id, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to update subtitle timing")
	}
	h.sync.TimingChanged(context.WithoutCancel(ctx), subtitles.SyncTarget{MediaFileID: updated.MediaFileID, StoredID: updated.ID})
	return updated, nil
}
