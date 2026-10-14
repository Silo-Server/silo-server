package handlers

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsync"
)

// SubtitleSyncState is the timing and latest sync job of one subtitle a
// media file can be played with: a stored subtitle or a sidecar file.
type SubtitleSyncState struct {
	Key         string
	MediaFileID int
	// Stored is the stored subtitle; nil for a sidecar.
	Stored *subtitles.DownloadedSubtitle
	// External is the sidecar's catalog entry; nil for a stored subtitle.
	External *models.ExternalSubtitle
	// ContentSHA256 identifies a sidecar's bytes on disk.
	ContentSHA256 string
	Timing        subtitles.Timing
	// Revision guards timing writes: the stored subtitle's revision, or the
	// sidecar correction's (0 before one exists).
	Revision int64
	Job      *subsync.Job
	// externalTimingID is the sidecar correction's row, once one exists.
	externalTimingID int64
}

var errSubtitleSyncUnavailable = apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Subtitle sync is not configured")

// errSidecarUnreadable reports a sidecar on disk that could not be read; a
// list leaves it out, and a lookup by its key fails with it.
var errSidecarUnreadable = apiError(http.StatusInternalServerError, "internal_error", "Unable to read subtitle file")

func (h *SubtitleSearchHandler) syncReady() error {
	if !h.SyncAvailable() || h.repo == nil {
		return errSubtitleSyncUnavailable
	}
	return nil
}

// ListSubtitleSync returns the timing and sync state of every subtitle of the
// file whose timing can be corrected: stored subtitles, then sidecars. A
// sidecar that cannot be read is left out.
func (h *SubtitleSearchHandler) ListSubtitleSync(ctx context.Context, access catalog.AccessFilter, fileID int) ([]SubtitleSyncState, error) {
	if err := h.syncReady(); err != nil {
		return nil, err
	}
	file, err := h.authorizedSubtitleFile(ctx, access, fileID)
	if err != nil {
		return nil, err
	}
	rows, err := h.repo.ListDownloadedSubtitles(ctx, fileID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read stored subtitles")
	}
	out := make([]SubtitleSyncState, 0, len(rows)+len(file.ExternalSubtitles))
	var storedIDs []int
	for i := range rows {
		if !subtitles.SupportsRetime(rows[i].Format) {
			continue
		}
		out = append(out, storedSyncState(&rows[i]))
		storedIDs = append(storedIDs, rows[i].ID)
	}
	external, err := h.externalSyncStates(ctx, file)
	if err != nil {
		return nil, err
	}
	out = append(out, external...)

	stored, err := h.sync.LatestForSubtitles(ctx, storedIDs)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read subtitle sync")
	}
	var timingIDs []int64
	for _, state := range out {
		if state.externalTimingID != 0 {
			timingIDs = append(timingIDs, state.externalTimingID)
		}
	}
	sidecars, err := h.sync.LatestForExternal(ctx, timingIDs)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read subtitle sync")
	}
	for i := range out {
		if out[i].Stored != nil {
			out[i].Job = stored[out[i].Stored.ID]
		} else if out[i].externalTimingID != 0 {
			out[i].Job = sidecars[out[i].externalTimingID]
		}
	}
	return out, nil
}

// SubtitleSync returns one subtitle's timing and sync state by sync key.
func (h *SubtitleSearchHandler) SubtitleSync(ctx context.Context, access catalog.AccessFilter, fileID int, key string) (*SubtitleSyncState, error) {
	if err := h.syncReady(); err != nil {
		return nil, err
	}
	state, err := h.subtitleSyncTarget(ctx, access, fileID, key)
	if err != nil {
		return nil, err
	}
	if err := h.attachLatestJob(ctx, state); err != nil {
		return nil, err
	}
	return state, nil
}

// StartSubtitleSync starts a manual sync of one subtitle, or finds its active
// job, and returns the subtitle's state with that job.
func (h *SubtitleSearchHandler) StartSubtitleSync(ctx context.Context, access catalog.AccessFilter, fileID int, key string) (*SubtitleSyncState, error) {
	if err := h.syncReady(); err != nil {
		return nil, err
	}
	state, err := h.subtitleSyncTarget(ctx, access, fileID, key)
	if err != nil {
		return nil, err
	}
	var job *subsync.Job
	if state.Stored != nil {
		job, err = h.sync.Request(ctx, state.Stored.ID, subsync.TriggerManual, new(access.UserID))
	} else {
		job, err = h.sync.RequestExternal(ctx, fileID, *state.External, subsync.TriggerManual, new(access.UserID))
	}
	switch {
	case errors.Is(err, subsync.ErrUnsupportedFormat):
		return nil, apiError(http.StatusUnprocessableEntity, "unsupported_format", "This subtitle format cannot be synced")
	case errors.Is(err, subsync.ErrSubtitleNotFound):
		return nil, subtitles.ErrSubtitleNotFound
	case err != nil:
		slog.ErrorContext(ctx, "subtitle sync request failed", "component", "api", "media_file_id", fileID, "sync_key", key, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to start subtitle sync")
	}
	// Starting a sidecar's first sync creates its correction row; read the
	// state again for the revision a timing write must name.
	if state.External != nil {
		if state, err = h.subtitleSyncTarget(ctx, access, fileID, key); err != nil {
			return nil, err
		}
	}
	state.Job = job
	return state, nil
}

// SetSubtitleSyncTiming replaces one subtitle's correction while it is still
// at revision and, for a sidecar, the bytes contentSHA256 names. Anyone who
// can play the file may set it.
func (h *SubtitleSearchHandler) SetSubtitleSyncTiming(ctx context.Context, access catalog.AccessFilter, fileID int, key string, revision int64, contentSHA256 string, timing subtitles.Timing) (*SubtitleSyncState, error) {
	if err := subtitles.ValidateTiming(timing); err != nil {
		return nil, apiError(http.StatusUnprocessableEntity, "validation_failed", err.Error())
	}
	if err := h.syncReady(); err != nil {
		return nil, err
	}
	state, err := h.subtitleSyncTarget(ctx, access, fileID, key)
	if err != nil {
		return nil, err
	}
	if state.Stored != nil {
		updated, err := h.SetStoredSubtitleTiming(ctx, access, state.Stored.ID, revision, timing)
		if err != nil {
			return nil, err
		}
		next := storedSyncState(updated)
		return &next, h.attachLatestJob(ctx, &next)
	}
	if h.external == nil {
		return nil, errSubtitleSyncUnavailable
	}
	if state.Revision != revision || state.ContentSHA256 != contentSHA256 {
		return nil, subtitles.ErrExternalTimingChanged
	}
	row, err := h.external.SetExternalTiming(ctx, fileID, state.ContentSHA256, state.External.Path,
		subtitles.SubtitleFormat(strings.ToLower(state.External.Format)), timing, revision)
	if errors.Is(err, subtitles.ErrExternalTimingChanged) {
		return nil, err
	}
	if err != nil {
		slog.ErrorContext(ctx, "sidecar subtitle timing update failed", "component", "api", "media_file_id", fileID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to update subtitle timing")
	}
	h.sync.TimingChanged(context.WithoutCancel(ctx), subtitles.SyncTarget{MediaFileID: fileID, ExternalPath: state.External.Path})
	state.Timing, state.Revision = row.Timing, row.Revision
	state.externalTimingID = row.ID
	return state, h.attachLatestJob(ctx, state)
}

// subtitleSyncTarget resolves a sync key to the subtitle it names under the
// file, after checking file access.
func (h *SubtitleSearchHandler) subtitleSyncTarget(ctx context.Context, access catalog.AccessFilter, fileID int, key string) (*SubtitleSyncState, error) {
	storedID, pathKey, ok := subtitles.ParseSyncKey(key)
	if !ok {
		return nil, subtitles.ErrSubtitleNotFound
	}
	if storedID > 0 {
		row, err := h.storedSubtitleForSync(ctx, access, storedID)
		if err != nil {
			return nil, err
		}
		if row.MediaFileID != fileID || !subtitles.SupportsRetime(row.Format) {
			return nil, subtitles.ErrSubtitleNotFound
		}
		state := storedSyncState(row)
		return &state, nil
	}
	file, err := h.authorizedSubtitleFile(ctx, access, fileID)
	if err != nil {
		return nil, err
	}
	for i := range file.ExternalSubtitles {
		sidecar := &file.ExternalSubtitles[i]
		if subtitles.ExternalPathKey(sidecar.Path) != pathKey {
			continue
		}
		state, err := h.externalSyncState(ctx, file.ID, sidecar, nil)
		if err != nil {
			return nil, err
		}
		if state == nil {
			return nil, subtitles.ErrSubtitleNotFound
		}
		return state, nil
	}
	return nil, subtitles.ErrSubtitleNotFound
}

func storedSyncState(row *subtitles.DownloadedSubtitle) SubtitleSyncState {
	return SubtitleSyncState{Key: subtitles.StoredSyncKey(row.ID), MediaFileID: row.MediaFileID, Stored: row,
		Timing: row.Timing, Revision: row.Revision}
}

// externalSyncStates returns the state of each sidecar of the file whose
// timing can be corrected and whose bytes can be read.
func (h *SubtitleSearchHandler) externalSyncStates(ctx context.Context, file *models.MediaFile) ([]SubtitleSyncState, error) {
	if h.external == nil || len(file.ExternalSubtitles) == 0 {
		return nil, nil
	}
	timings, err := h.external.ExternalTimings(ctx, file.ID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read subtitle timing")
	}
	var out []SubtitleSyncState
	for i := range file.ExternalSubtitles {
		state, err := h.externalSyncState(ctx, file.ID, &file.ExternalSubtitles[i], timings)
		if errors.Is(err, errSidecarUnreadable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if state != nil {
			out = append(out, *state)
		}
	}
	return out, nil
}

// externalSyncState reads a sidecar and finds its correction, in timings when
// given. It is nil for a sidecar that cannot be retimed or is gone from disk.
func (h *SubtitleSearchHandler) externalSyncState(ctx context.Context, fileID int, sidecar *models.ExternalSubtitle, timings map[string]*subtitles.ExternalTiming) (*SubtitleSyncState, error) {
	if h.external == nil || !subtitles.SupportsRetime(subtitles.SubtitleFormat(sidecar.Format)) {
		return nil, nil
	}
	data, err := os.ReadFile(sidecar.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		slog.WarnContext(ctx, "read sidecar subtitle failed", "component", "api", "media_file_id", fileID, "error", err)
		return nil, errSidecarUnreadable
	}
	state := &SubtitleSyncState{Key: subtitles.ExternalSyncKey(sidecar.Path), MediaFileID: fileID, External: sidecar,
		ContentSHA256: subtitles.ContentSHA256(data), Timing: subtitles.Timing{Scale: 1}}
	var row *subtitles.ExternalTiming
	if timings != nil {
		row = timings[state.ContentSHA256]
	} else if row, err = h.external.ExternalTiming(ctx, fileID, state.ContentSHA256); err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Unable to read subtitle timing")
	}
	if row != nil {
		state.Timing, state.Revision, state.externalTimingID = row.Timing, row.Revision, row.ID
	}
	return state, nil
}

func (h *SubtitleSearchHandler) attachLatestJob(ctx context.Context, state *SubtitleSyncState) error {
	var err error
	switch {
	case state.Stored != nil:
		state.Job, err = h.sync.Latest(ctx, state.Stored.ID)
	case state.externalTimingID != 0:
		state.Job, err = h.sync.LatestExternal(ctx, state.externalTimingID)
	}
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Unable to read subtitle sync")
	}
	return nil
}
