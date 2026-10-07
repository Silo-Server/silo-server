package apiv2

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// SubtitleSyncState is one subtitle's timing correction and latest sync job,
// for a stored subtitle or a subtitle file next to the media (a sidecar).
type SubtitleSyncState struct {
	// Key is the subtitle's sync key, as the playback inventory's sync_key.
	Key         string `json:"key"`
	MediaFileID ID     `json:"media_file_id"`
	Source      string `json:"source" enum:"downloaded,external"`
	// StoredSubtitleID is the stored subtitle's ID; absent for a sidecar.
	StoredSubtitleID ID     `json:"stored_subtitle_id,omitempty"`
	Language         string `json:"language"`
	Format           string `json:"format"`
	// Label is the stored subtitle's release name, or the sidecar's file name.
	Label string `json:"label"`
	// Timing is the correction every delivery path applies.
	Timing SubtitleTiming `json:"timing"`
	// Sync is the subtitle's latest sync job; absent when it has none.
	Sync *SubtitleSyncJobState `json:"sync,omitempty"`
}

type SubtitleSyncFileInput struct {
	MediaFileID ID `path:"media_file_id"`
}
type SubtitleSyncKeyInput struct {
	MediaFileID ID     `path:"media_file_id"`
	Key         string `path:"key" pattern:"^(stored-[1-9][0-9]*|external-[0-9a-f]{64})$" doc:"The subtitle's sync key, from the playback inventory or listSubtitleSync"`
}
type SubtitleSyncTimingInput struct {
	MediaFileID ID     `path:"media_file_id"`
	Key         string `path:"key" pattern:"^(stored-[1-9][0-9]*|external-[0-9a-f]{64})$" doc:"The subtitle's sync key, from the playback inventory or listSubtitleSync"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        SubtitleTiming
}
type SubtitleSyncListOutput struct {
	Body struct {
		Subtitles []SubtitleSyncState `json:"subtitles"`
	}
}
type SubtitleSyncStateOutput struct {
	// ETag is the validator setSubtitleTiming takes in If-Match. It follows
	// the timing, not the job's progress, so the read does not answer
	// If-None-Match.
	ETag string `header:"ETag"`
	Body struct {
		Subtitle SubtitleSyncState `json:"subtitle"`
	}
}
type SubtitleSyncStartOutput struct {
	Body struct {
		Subtitle SubtitleSyncState `json:"subtitle"`
	}
}

// subtitleSyncTag validates a subtitle's timing for one viewer: the stored
// row's revision, or a sidecar's bytes on disk and their correction's revision.
func subtitleSyncTag(ctx context.Context, state *handlers.SubtitleSyncState) EntityTag {
	return RenderETag("subtitle-sync/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx)+"/"+state.Key, state.ContentSHA256, state.Revision)
}

func subtitleSyncStateView(state handlers.SubtitleSyncState) SubtitleSyncState {
	view := SubtitleSyncState{Key: state.Key, MediaFileID: ID(strconv.Itoa(state.MediaFileID)),
		Timing: subtitleTimingView(state.Timing), Sync: subtitleSyncJobStateView(state.Job)}
	switch {
	case state.Stored != nil:
		row := state.Stored
		language, ok := subtitles.CanonicalProviderLanguage(row.Provider, row.Language)
		if !ok {
			language = row.Language
		}
		view.Source, view.StoredSubtitleID = playback.SubtitleSourceDownloadedV3, ID(strconv.Itoa(row.ID))
		view.Language, view.Format, view.Label = language, string(row.Format), row.ReleaseName
		if view.Label == "" {
			view.Label = row.Provider
		}
	case state.External != nil:
		view.Source = playback.SubtitleSourceExternalV3
		view.Language, view.Format, view.Label = state.External.Language, state.External.Format, filepath.Base(state.External.Path)
	}
	return view
}

func (reg *Registry) subtitleSyncFileAccess(ctx context.Context, raw ID) (int, catalogpkg.AccessFilter, *Problem) {
	var access catalogpkg.AccessFilter
	id, p := raw.positive("path.media_file_id")
	if p != nil {
		return 0, access, p
	}
	if reg.deps.SubtitleSync == nil || !reg.deps.SubtitleSync.SyncAvailable() || reg.deps.CatalogAccess == nil {
		return 0, access, NewProblem(TypeDependencyUnavailable, "Subtitle sync is unavailable.")
	}
	access, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return 0, access, catalogProblem(err, "path")
	}
	return id, access, nil
}

func registerSubtitleSyncTargets(reg *Registry) {
	const access = " Requires file access only: the subtitle's bytes never change, and its timing can always be reset."

	list := Operation{Operation: humaOp(http.MethodGet, Prefix+"/subtitles/{media_file_id}/sync", "listSubtitleSync", "subtitles", "List the timing and latest sync job of every subtitle of a media file that can be synced: stored subtitles, then subtitle files next to the media."), Class: ClassProfileScoped, ProfileOptional: true, HouseholdProfileGate: true, ServiceBacked: true}
	list.Description = "Each entry's key is the subtitle's sync key, which the playback inventory publishes as sync_key on the matching track. Only SRT, WebVTT, ASS, and SSA subtitles are listed. A subtitle file next to the media that cannot be read is left out." + access
	Register(reg, list, func(ctx context.Context, in *SubtitleSyncFileInput) (*SubtitleSyncListOutput, error) {
		fileID, access, p := reg.subtitleSyncFileAccess(ctx, in.MediaFileID)
		if p != nil {
			return nil, p
		}
		states, err := reg.deps.SubtitleSync.ListSubtitleSync(ctx, access, fileID)
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		out := new(SubtitleSyncListOutput)
		out.Body.Subtitles = make([]SubtitleSyncState, 0, len(states))
		for _, state := range states {
			out.Body.Subtitles = append(out.Body.Subtitles, subtitleSyncStateView(state))
		}
		return out, nil
	})

	read := Operation{Operation: humaOp(http.MethodGet, Prefix+"/subtitles/{media_file_id}/sync/{key}", "getSubtitleSync", "subtitles", "Read one subtitle's timing and latest sync job, and the validator for setSubtitleTiming."), Class: ClassProfileScoped, ProfileOptional: true, HouseholdProfileGate: true, ServiceBacked: true}
	read.Description = "Poll it while the job is pending or running, or follow the subtitle_sync_updated realtime event of a playback session." + access
	Register(reg, read, func(ctx context.Context, in *SubtitleSyncKeyInput) (*SubtitleSyncStateOutput, error) {
		fileID, access, p := reg.subtitleSyncFileAccess(ctx, in.MediaFileID)
		if p != nil {
			return nil, p
		}
		state, err := reg.deps.SubtitleSync.SubtitleSync(ctx, access, fileID, in.Key)
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		out := &SubtitleSyncStateOutput{ETag: subtitleSyncTag(ctx, state).String()}
		out.Body.Subtitle = subtitleSyncStateView(*state)
		return out, nil
	})

	start := Operation{Operation: humaOp(http.MethodPost, Prefix+"/subtitles/{media_file_id}/sync/{key}", "startSubtitleSync", "subtitles", "Align one subtitle to its file's audio."), Class: ClassProfileScoped, ProfileOptional: true, HouseholdProfileGate: true, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyCoalescing}
	start.DefaultStatus = http.StatusAccepted
	start.Description = "Starts a sync job, or returns the subtitle's active one, and returns the subtitle with that job. A request repeated after the job finished starts another job, which reaches the same timing. A synced job stores a correction that every delivery path applies; no_match leaves the timing unchanged and means the subtitle most likely belongs to another release. A subtitle file next to the media is never modified: its correction belongs to its current bytes." + access
	Register(reg, start, func(ctx context.Context, in *SubtitleSyncKeyInput) (*SubtitleSyncStartOutput, error) {
		fileID, access, p := reg.subtitleSyncFileAccess(ctx, in.MediaFileID)
		if p != nil {
			return nil, p
		}
		state, err := reg.deps.SubtitleSync.StartSubtitleSync(ctx, access, fileID, in.Key)
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		out := new(SubtitleSyncStartOutput)
		out.Body.Subtitle = subtitleSyncStateView(*state)
		return out, nil
	})

	timing := Operation{Operation: humaOp(http.MethodPut, Prefix+"/subtitles/{media_file_id}/sync/{key}/timing", "setSubtitleTiming", "subtitles", "Replace one subtitle's timing correction."), Class: ClassProfileScoped, ProfileOptional: true, HouseholdProfileGate: true, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
	timing.Description = "Sets the correction every delivery path applies: original time t plays at t * scale + offset_ms. Send {offset_ms: 0, scale: 1} to restore the original timing. Requires If-Match with the validator from getSubtitleSync." + access
	Register(reg, timing, func(ctx context.Context, in *SubtitleSyncTimingInput) (*SubtitleSyncStateOutput, error) {
		fileID, access, p := reg.subtitleSyncFileAccess(ctx, in.MediaFileID)
		if p != nil {
			return nil, p
		}
		state, err := reg.deps.SubtitleSync.SubtitleSync(ctx, access, fileID, in.Key)
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, subtitleSyncTag(ctx, state)); p != nil {
			return nil, p
		}
		updated, err := reg.deps.SubtitleSync.SetSubtitleSyncTiming(ctx, access, fileID, in.Key, state.Revision, state.ContentSHA256,
			subtitles.Timing{OffsetMS: in.Body.OffsetMS, Scale: in.Body.Scale})
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		out := &SubtitleSyncStateOutput{ETag: subtitleSyncTag(ctx, updated).String()}
		out.Body.Subtitle = subtitleSyncStateView(*updated)
		return out, nil
	})
}
