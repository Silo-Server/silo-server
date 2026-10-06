package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsync"
)

// SubtitleSyncAPI aligns stored subtitles and sidecars to their file's audio.
type SubtitleSyncAPI interface {
	SyncAvailable() bool
	AutoSyncEnabled(context.Context) bool
	RequestStoredSubtitleSync(context.Context, catalogpkg.AccessFilter, int) (*subsync.Job, error)
	StoredSubtitleSync(context.Context, catalogpkg.AccessFilter, int) (*subtitles.DownloadedSubtitle, *subsync.Job, error)
	SubtitleSyncJobs(context.Context, []int) map[int]*subsync.Job
	SetStoredSubtitleTiming(context.Context, catalogpkg.AccessFilter, int, int64, subtitles.Timing) (*subtitles.DownloadedSubtitle, error)
	ExternalSyncAvailable() bool
	ListSubtitleSync(context.Context, catalogpkg.AccessFilter, int) ([]handlers.SubtitleSyncState, error)
	SubtitleSync(context.Context, catalogpkg.AccessFilter, int, string) (*handlers.SubtitleSyncState, error)
	StartSubtitleSync(context.Context, catalogpkg.AccessFilter, int, string) (*handlers.SubtitleSyncState, error)
	SetSubtitleSyncTiming(context.Context, catalogpkg.AccessFilter, int, string, int64, string, subtitles.Timing) (*handlers.SubtitleSyncState, error)
}

// SubtitleTiming maps an original subtitle time t to t * scale + offset_ms.
type SubtitleTiming struct {
	OffsetMS int     `json:"offset_ms" minimum:"-600000" maximum:"600000"`
	Scale    float64 `json:"scale" minimum:"0.9" maximum:"1.1"`
}

// SubtitleSyncJobState is one sync attempt, inside an object that names the
// subtitle it syncs.
type SubtitleSyncJobState struct {
	ID      ID     `json:"id"`
	Status  string `json:"status" enum:"pending,running,synced,already_synced,no_match,failed"`
	Trigger string `json:"trigger" enum:"auto,manual"`
	// Phase says what an active job is doing: queued, then analyzing (reading
	// the file's speech, most of the time), then matching. Absent once the
	// job finished.
	Phase string `json:"phase,omitempty" enum:"queued,analyzing,matching"`
	// Progress is an active job's progress, 0..1; absent once it finished.
	Progress *float64 `json:"progress,omitempty" minimum:"0" maximum:"1"`
	// Failure says why a failed job failed: subtitle_changed (the subtitle,
	// its file on disk, or its timing changed meanwhile), no_audio (the media
	// file has no audio to read), unavailable (no server could analyze the
	// audio now; try again later), or error.
	Failure string `json:"failure,omitempty" enum:"subtitle_changed,no_audio,unavailable,error"`
	// Confidence is the share of sampled audio windows that agree with the
	// result, 0..1; null until the alignment finished.
	Confidence *float64 `json:"confidence" nullable:"true"`
	// Result is the correction the alignment found; absent until it finished
	// with one. A synced job applied it to the subtitle.
	Result     *SubtitleTiming `json:"result,omitempty"`
	CreatedAt  Instant         `json:"created_at"`
	FinishedAt *Instant        `json:"finished_at" nullable:"true"`
}

// SubtitleSyncJob is one sync attempt for a stored subtitle.
type SubtitleSyncJob struct {
	SubtitleSyncJobState
	SubtitleID ID `json:"subtitle_id"`
}

type SubtitleSyncStatus struct {
	Capability
	AutoSync bool `json:"auto_sync"`
	// External reports that subtitle files next to the media (sidecars) can
	// be synced and retimed too.
	External bool `json:"external"`
}
type SubtitleSyncStatusOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         SubtitleSyncStatus
}

type StoredSubtitleIDInput struct {
	ID ID `path:"id" pattern:"^[1-9][0-9]*$"`
}
type SubtitleSyncRequestOutput struct {
	Body struct {
		Job SubtitleSyncJob `json:"job"`
	}
}
type SubtitleSyncReadOutput struct {
	// ETag is the validator setStoredSubtitleTiming takes in If-Match. It
	// follows the subtitle's revision, not the job's progress, so the read
	// does not answer If-None-Match.
	ETag string `header:"ETag"`
	Body struct {
		Subtitle StoredSubtitle `json:"subtitle"`
	}
}
type SubtitleTimingInput struct {
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        SubtitleTiming
}
type SubtitleTimingOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Subtitle StoredSubtitle `json:"subtitle"`
	}
}

func subtitleTimingView(t subtitles.Timing) SubtitleTiming {
	n := t.Normalized()
	return SubtitleTiming{OffsetMS: n.OffsetMS, Scale: n.Scale}
}

func subtitleSyncJobStateView(job *subsync.Job) *SubtitleSyncJobState {
	if job == nil {
		return nil
	}
	view := &SubtitleSyncJobState{
		ID: ID(strconv.FormatInt(job.ID, 10)), Status: job.Status, Trigger: job.Trigger,
		Phase: job.PublicPhase(), Progress: job.PublicProgress(), Failure: job.PublicFailure(),
		Confidence: job.Confidence, CreatedAt: NewInstant(job.CreatedAt),
	}
	if job.Result != nil {
		view.Result = new(subtitleTimingView(*job.Result))
	}
	if job.FinishedAt != nil {
		view.FinishedAt = new(NewInstant(*job.FinishedAt))
	}
	return view
}

// subtitleSyncJobView projects a stored subtitle's job.
func subtitleSyncJobView(job *subsync.Job) *SubtitleSyncJob {
	state := subtitleSyncJobStateView(job)
	if state == nil {
		return nil
	}
	return &SubtitleSyncJob{SubtitleSyncJobState: *state, SubtitleID: ID(strconv.Itoa(job.SubtitleID))}
}

func subtitleSyncProblem(err error) *Problem {
	if errors.Is(err, subtitles.ErrSubtitleNotFound) {
		return NewProblem(TypeNotFound, "Subtitle not found.")
	}
	if _, ok := errors.AsType[*subtitles.SubtitleRevisionConflict](err); ok || errors.Is(err, subtitles.ErrExternalTimingChanged) {
		return NewProblem(TypePreconditionFailed, "Subtitle changed; read its current metadata before another attempt.")
	}
	if _, ok := errors.AsType[*handlers.APIError](err); ok {
		return serviceProblem(err)
	}
	return NewProblem(TypeInternalError, "Subtitle sync failed.")
}

func (reg *Registry) subtitleSyncAccess(ctx context.Context, raw ID) (int, catalogpkg.AccessFilter, *Problem) {
	var access catalogpkg.AccessFilter
	id, p := raw.positive("path.id")
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

func registerSubtitleSync(reg *Registry) {
	status := Operation{Operation: humaOp(http.MethodGet, Prefix+"/subtitles/sync/status", "getSubtitleSyncStatus", "subtitles", "Read whether stored subtitles can be synced to their file's audio, and whether new ones are synced automatically."), Class: ClassProfileScoped, ProfileOptional: true}
	Register(reg, status, func(ctx context.Context, _ *CapabilityInput) (*SubtitleSyncStatusOutput, error) {
		available := reg.deps.SubtitleSync != nil && reg.deps.SubtitleSync.SyncAvailable()
		auto := available && reg.deps.SubtitleSync.AutoSyncEnabled(ctx)
		external := available && reg.deps.SubtitleSync.ExternalSyncAvailable()
		return &SubtitleSyncStatusOutput{Body: SubtitleSyncStatus{Capability: Capability{State: configuredEnabledCapabilityState(available, available)}, AutoSync: auto, External: external}}, nil
	})

	start := Operation{Operation: humaOp(http.MethodPost, Prefix+"/subtitles/stored/{id}/sync", "syncStoredSubtitle", "subtitles", "Align a stored subtitle to its file's audio."), Class: ClassProfileScoped, ProfileOptional: true, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyCoalescing}
	start.DefaultStatus = http.StatusAccepted
	start.Description = "Starts a sync job, or returns the subtitle's active one. A request repeated after that job finished starts another job; it aligns the same stored bytes against cached speech, so it reaches the same timing. Requires file access only: the stored bytes never change, and the timing can always be reset. A synced job stores a timing correction that every delivery path applies; no_match leaves the timing unchanged and means the subtitle most likely belongs to another release. Poll the job through the read operation or the stored subtitle list."
	Register(reg, start, func(ctx context.Context, in *StoredSubtitleIDInput) (*SubtitleSyncRequestOutput, error) {
		id, access, p := reg.subtitleSyncAccess(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		job, err := reg.deps.SubtitleSync.RequestStoredSubtitleSync(ctx, access, id)
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		out := new(SubtitleSyncRequestOutput)
		out.Body.Job = *subtitleSyncJobView(job)
		return out, nil
	})

	read := Operation{Operation: humaOp(http.MethodGet, Prefix+"/subtitles/stored/{id}/sync", "getStoredSubtitleSync", "subtitles", "Read a stored subtitle's timing and latest sync job, and the validator for setStoredSubtitleTiming. Requires file access."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}
	Register(reg, read, func(ctx context.Context, in *StoredSubtitleIDInput) (*SubtitleSyncReadOutput, error) {
		id, access, p := reg.subtitleSyncAccess(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		row, job, err := reg.deps.SubtitleSync.StoredSubtitleSync(ctx, access, id)
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		out := &SubtitleSyncReadOutput{ETag: viewerSubtitleTag(ctx, row).String()}
		out.Body.Subtitle = storedSubtitleView(*row)
		out.Body.Subtitle.Sync = subtitleSyncJobView(job)
		return out, nil
	})

	timing := Operation{Operation: humaOp(http.MethodPut, Prefix+"/subtitles/stored/{id}/timing", "setStoredSubtitleTiming", "subtitles", "Replace a stored subtitle's timing correction."), Class: ClassProfileScoped, ProfileOptional: true, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
	timing.Description = "Sets the correction every delivery path applies: original time t plays at t * scale + offset_ms. Send {offset_ms: 0, scale: 1} to restore the original timing. Requires If-Match with the validator from getStoredSubtitleSync, and file access. The stored bytes never change."
	Register(reg, timing, func(ctx context.Context, in *SubtitleTimingInput) (*SubtitleTimingOutput, error) {
		id, access, p := reg.subtitleSyncAccess(ctx, in.ID)
		if p != nil {
			return nil, p
		}
		row, _, err := reg.deps.SubtitleSync.StoredSubtitleSync(ctx, access, id)
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, viewerSubtitleTag(ctx, row)); p != nil {
			return nil, p
		}
		updated, err := reg.deps.SubtitleSync.SetStoredSubtitleTiming(ctx, access, id, row.Revision, subtitles.Timing{OffsetMS: in.Body.OffsetMS, Scale: in.Body.Scale})
		if err != nil {
			return nil, subtitleSyncProblem(err)
		}
		out := &SubtitleTimingOutput{ETag: viewerSubtitleTag(ctx, updated).String()}
		out.Body.Subtitle = reg.storedSubtitleWithSync(ctx, *updated)
		return out, nil
	})
}
