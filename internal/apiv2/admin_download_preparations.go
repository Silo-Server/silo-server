package apiv2

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/downloads"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// AdminDownloadPreparationService reads the prepared-download encode queue.
type AdminDownloadPreparationService interface {
	Available() bool
	List(ctx context.Context, limit int) (*downloads.PreparationList, error)
}

// AdminDownloadPreparationControlService pauses, resumes and cancels
// prepared-download encode jobs.
type AdminDownloadPreparationControlService interface {
	PausePreparations(ctx context.Context, ids []string) ([]downloads.PreparationResult, error)
	ResumePreparations(ctx context.Context, ids []string) ([]downloads.PreparationResult, error)
	CancelPreparations(ctx context.Context, ids []string) ([]downloads.PreparationResult, error)
}

// AdminDownloadPreparation is one server-side remux or transcode that turns a
// library file into an offline download, as an operator watches it.
type AdminDownloadPreparation struct {
	ID            string `json:"id" doc:"Prepared artifact id; one job can serve many downloads"`
	State         string `json:"state" enum:"running,queued,retrying,paused,failed" doc:"running: an attempt is encoding. queued: waiting for a worker. retrying: an attempt failed and the job waits out its backoff (next_retry_at). paused: an administrator paused the job (paused_at); no worker claims it until it is resumed. failed: attempts are exhausted; listed for 24 hours after failing."`
	Format        string `json:"format" enum:"remux,transcode"`
	QueuePosition *int   `json:"queue_position,omitempty" minimum:"1" doc:"1-based position among queued jobs; present only when state is queued"`
	LogSessionID  string `json:"log_session_id" doc:"playback_session_id under which this job's FFmpeg output appears in the operational logs, across every attempt"`

	MediaFileID   ID     `json:"media_file_id"`
	ContentID     string `json:"content_id,omitempty" doc:"The movie, or the series an episode belongs to"`
	EpisodeID     string `json:"episode_id,omitempty"`
	MediaTitle    string `json:"media_title"`
	MediaType     string `json:"media_type"`
	SeriesName    string `json:"series_name,omitempty"`
	EpisodeName   string `json:"episode_name,omitempty"`
	SeasonNumber  *int   `json:"season_number,omitempty"`
	EpisodeNumber *int   `json:"episode_number,omitempty"`

	Source AdminDownloadPreparationSource  `json:"source"`
	Output AdminDownloadPreparationOutput  `json:"output"`
	Worker *AdminDownloadPreparationWorker `json:"worker,omitempty" doc:"Where the current or last attempt ran; absent before a worker was chosen"`

	Attempts            int                               `json:"attempts" minimum:"0"`
	MaxAttempts         int                               `json:"max_attempts" minimum:"0"`
	Error               string                            `json:"error,omitempty" doc:"Last attempt's failure, when there was one"`
	CreatedAt           Instant                           `json:"created_at"`
	StartedAt           *Instant                          `json:"started_at,omitempty" doc:"When the current or last attempt started"`
	FailedAt            *Instant                          `json:"failed_at,omitempty" doc:"Present only when state is failed"`
	NextRetryAt         *Instant                          `json:"next_retry_at,omitempty" doc:"Present only when state is retrying"`
	PausedAt            *Instant                          `json:"paused_at,omitempty" doc:"Present only when state is paused"`
	Progress            *AdminDownloadPreparationProgress `json:"progress,omitempty" doc:"Latest reading of a running attempt, written about every 5 seconds; absent until the first one"`
	ProgressUnavailable bool                              `json:"progress_unavailable" doc:"The running attempt's worker cannot report progress, so progress will stay absent"`

	Requesters []AdminDownloadPreparationRequester `json:"requesters" doc:"Download rows waiting on this job, oldest first"`
}

type AdminDownloadPreparationSource struct {
	Container       string                               `json:"container,omitempty"`
	VideoCodec      string                               `json:"video_codec,omitempty"`
	Resolution      string                               `json:"resolution,omitempty"`
	HDR             bool                                 `json:"hdr"`
	AudioTracks     []AdminDownloadPreparationAudioTrack `json:"audio_tracks"`
	FileSize        int64                                `json:"file_size" minimum:"0"`
	DurationSeconds float64                              `json:"duration_seconds" minimum:"0"`
	BitrateKbps     *int                                 `json:"bitrate_kbps,omitempty"`
}

type AdminDownloadPreparationAudioTrack struct {
	Codec    string `json:"codec,omitempty"`
	Channels *int   `json:"channels,omitempty"`
	Language string `json:"language,omitempty"`
}

type AdminDownloadPreparationOutput struct {
	Container         string `json:"container"`
	VideoCodec        string `json:"video_codec" doc:"Target video codec, or copy for a remux"`
	AudioCodec        string `json:"audio_codec" doc:"Target audio codec, or copy"`
	Resolution        string `json:"resolution,omitempty"`
	BitrateKbps       *int   `json:"bitrate_kbps,omitempty" doc:"Video bitrate cap of a transcode"`
	ToneMapMode       string `json:"tone_map_mode,omitempty" doc:"Present when HDR is tone-mapped to SDR"`
	ToneMapSourceKind string `json:"tone_map_source_kind,omitempty"`
	AllAudioTracks    bool   `json:"all_audio_tracks" doc:"Every source audio track is kept, in source order"`
}

type AdminDownloadPreparationWorker struct {
	Kind   string `json:"kind" enum:"server,node" doc:"server: an API server. node: a transcode node."`
	NodeID *ID    `json:"node_id,omitempty" doc:"Transcode node id when kind is node"`
	Name   string `json:"name" doc:"Node name, or the API server's node id, when the attempt started"`
}

type AdminDownloadPreparationProgress struct {
	EncodedSeconds  float64 `json:"encoded_seconds" minimum:"0" doc:"Output timeline written so far"`
	DurationSeconds float64 `json:"duration_seconds" minimum:"0" doc:"Length progress is measured against; 0 when unknown"`
	Speed           float64 `json:"speed" minimum:"0" doc:"Encode rate as a multiple of realtime; 0 when not reported yet"`
	UpdatedAt       Instant `json:"updated_at"`
}

type AdminDownloadPreparationRequester struct {
	UserID         ID     `json:"user_id"`
	Username       string `json:"username"`
	ProfileID      string `json:"profile_id"`
	ProfileName    string `json:"profile_name,omitempty"`
	DeviceID       string `json:"device_id,omitempty" doc:"Absent for a one-off web download"`
	DeviceName     string `json:"device_name,omitempty"`
	DevicePlatform string `json:"device_platform,omitempty"`
	Status         string `json:"status" doc:"The download row's status"`
}

type AdminDownloadPreparationCounts struct {
	Running      int `json:"running" minimum:"0"`
	Queued       int `json:"queued" minimum:"0"`
	Retrying     int `json:"retrying" minimum:"0"`
	Paused       int `json:"paused" minimum:"0"`
	FailedRecent int `json:"failed_recent" minimum:"0" doc:"Jobs that failed in the last 24 hours"`
}

type AdminDownloadPreparationsInput struct {
	Limit int `query:"limit" minimum:"1" maximum:"500" default:"200" doc:"Maximum items; counts always cover every listed job"`
}

type AdminDownloadPreparationsOutput struct {
	Body struct {
		Counts AdminDownloadPreparationCounts `json:"counts"`
		Items  []AdminDownloadPreparation     `json:"items" maxItems:"500" doc:"Running jobs, then jobs waiting to retry, then the queue in claim order, then paused jobs in claim order, then recent failures, newest first"`
	}
}

type AdminDownloadPreparationCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminDownloadPreparationCapabilitiesOutputBody
}

type AdminDownloadPreparationCapabilitiesOutputBody struct {
	Capability
	Available           bool   `json:"available"`
	RealtimeChannel     string `json:"realtime_channel" doc:"Admin realtime channel carrying download_preparation.changed and download_preparation.progress"`
	FailedWindowSeconds int    `json:"failed_window_seconds" doc:"How long a failed job stays listed"`
	LiveProgress        bool   `json:"live_progress" doc:"Running jobs report progress"`
	Controls            bool   `json:"controls" doc:"Jobs can be paused, resumed and canceled"`
}

func (c AdminDownloadPreparationCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}

func registerAdminDownloadPreparations(reg *Registry) {
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/downloads/preparations"+path, id, "admin", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("/capabilities", "getAdminDownloadPreparationCapabilities", "Discover the admin view of offline-download preparation."), func(_ context.Context, _ *CapabilityInput) (*AdminDownloadPreparationCapabilitiesOutput, error) {
		out := new(AdminDownloadPreparationCapabilitiesOutput)
		out.Body.Available = reg.deps.AdminDownloadPreparations != nil && reg.deps.AdminDownloadPreparations.Available()
		out.Body.RealtimeChannel = string(evt.ChannelDownloadPreparations)
		out.Body.FailedWindowSeconds = int(downloads.PreparationFailedWindow.Seconds())
		out.Body.LiveProgress = out.Body.Available
		out.Body.Controls = out.Body.Available && reg.deps.AdminDownloadPreparationControls != nil
		return out, nil
	})
	Register(reg, op("", "listAdminDownloadPreparations", "Read the offline-download preparation queue: running, queued and retrying jobs with live progress, and failures from the last 24 hours."), func(ctx context.Context, in *AdminDownloadPreparationsInput) (*AdminDownloadPreparationsOutput, error) {
		if reg.deps.AdminDownloadPreparations == nil || !reg.deps.AdminDownloadPreparations.Available() {
			return nil, unavailable("download preparation")
		}
		list, err := reg.deps.AdminDownloadPreparations.List(ctx, in.Limit)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := new(AdminDownloadPreparationsOutput)
		out.Body.Counts = AdminDownloadPreparationCounts{
			Running: list.Counts.Running, Queued: list.Counts.Queued,
			Retrying: list.Counts.Retrying, Paused: list.Counts.Paused, FailedRecent: list.Counts.FailedRecent,
		}
		out.Body.Items = make([]AdminDownloadPreparation, 0, len(list.Items))
		for _, item := range list.Items {
			out.Body.Items = append(out.Body.Items, adminDownloadPreparationOf(item))
		}
		return out, nil
	})
	registerAdminDownloadPreparationControls(reg)
}

// AdminDownloadPreparationActionInput names the jobs one action applies to.
type AdminDownloadPreparationActionInput struct {
	Body struct {
		IDs []string `json:"ids" minItems:"1" maxItems:"500" doc:"Preparation job ids (the id of a listed job). A repeated id is reported once."`
	}
}

// AdminDownloadPreparationActionResult is what an action did to one job.
type AdminDownloadPreparationActionResult struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome" enum:"applied,unchanged,not_found,not_applicable" doc:"applied: the action took effect. unchanged: the job was already in the requested state. not_found: no such job is being prepared; it finished, was canceled, or never existed. not_applicable: the action does not apply to the job's state, such as pausing or resuming a failed job."`
}

type AdminDownloadPreparationActionOutput struct {
	Body struct {
		Results []AdminDownloadPreparationActionResult `json:"results" maxItems:"500" doc:"One result per distinct requested id, in request order"`
	}
}

func registerAdminDownloadPreparationControls(reg *Registry) {
	type action func(AdminDownloadPreparationControlService, context.Context, []string) ([]downloads.PreparationResult, error)
	register := func(suffix, id, summary string, run action) {
		op := Operation{
			Operation:      humaOp("POST", Prefix+"/admin/downloads/preparations/"+suffix, id, "admin", summary),
			Class:          ClassActingAdmin,
			DemoRestricted: true,
			ServiceBacked:  true,
			RetrySafety:    RetrySafetyNaturalIdempotent,
		}
		Register(reg, op, func(ctx context.Context, in *AdminDownloadPreparationActionInput) (*AdminDownloadPreparationActionOutput, error) {
			controls := reg.deps.AdminDownloadPreparationControls
			if controls == nil {
				return nil, unavailable("download preparation controls")
			}
			for _, id := range in.Body.IDs {
				if !downloadprepare.ValidArtifactID(id) {
					return nil, NewProblem(TypeValidationFailed, "Invalid preparation job id.")
				}
			}
			results, err := run(controls, ctx, in.Body.IDs)
			if err != nil {
				return nil, serviceProblem(err)
			}
			out := new(AdminDownloadPreparationActionOutput)
			out.Body.Results = make([]AdminDownloadPreparationActionResult, 0, len(results))
			for _, r := range results {
				out.Body.Results = append(out.Body.Results, AdminDownloadPreparationActionResult{ID: r.ArtifactID, Outcome: string(r.Outcome)})
			}
			return out, nil
		})
	}
	register("pause", "pauseAdminDownloadPreparations",
		"Pause offline-download preparation jobs. A paused job keeps its place in the queue and no worker claims it until it is resumed. A running encode stops and restarts from the beginning when resumed; the stopped attempt does not count against the job. Waiting downloads stay preparing.",
		AdminDownloadPreparationControlService.PausePreparations)
	register("resume", "resumeAdminDownloadPreparations",
		"Resume paused offline-download preparation jobs. Each job returns to the queue at its original position, or waits out a pending retry backoff.",
		AdminDownloadPreparationControlService.ResumePreparations)
	register("cancel", "cancelAdminDownloadPreparations",
		"Cancel offline-download preparation jobs, including failed ones. A running encode stops, the job is removed, and every download still waiting on it fails with the message 'Canceled by an administrator'. A job that already finished preparing is not affected.",
		AdminDownloadPreparationControlService.CancelPreparations)
}

func adminDownloadPreparationOf(p downloads.Preparation) AdminDownloadPreparation {
	out := AdminDownloadPreparation{
		ID: p.ArtifactID, State: p.State, Format: p.Format,
		LogSessionID:  playback.DownloadPrepareLogSessionID(p.ArtifactID),
		MediaFileID:   IDFromInt(int64(p.MediaFileID)),
		ContentID:     p.ContentID,
		EpisodeID:     p.EpisodeID,
		MediaTitle:    p.MediaTitle,
		MediaType:     p.MediaType,
		SeriesName:    p.SeriesName,
		EpisodeName:   p.EpisodeName,
		SeasonNumber:  p.SeasonNumber,
		EpisodeNumber: p.EpisodeNumber,
		Source: AdminDownloadPreparationSource{
			Container: p.SourceContainer, VideoCodec: p.SourceVideoCodec, Resolution: p.SourceResolution, HDR: p.SourceHDR,
			AudioTracks:     make([]AdminDownloadPreparationAudioTrack, 0, len(p.SourceAudioTracks)),
			FileSize:        max(p.SourceFileSize, 0),
			DurationSeconds: max(p.SourceDuration, 0),
			BitrateKbps:     positiveInt(p.SourceBitrateKbps),
		},
		Output: AdminDownloadPreparationOutput{
			Container: p.TargetContainer, VideoCodec: p.TargetVideoCodec, AudioCodec: p.TargetAudioCodec,
			Resolution: p.TargetResolution, BitrateKbps: positiveInt(p.TargetBitrateKbps),
			ToneMapMode: p.ToneMapMode, ToneMapSourceKind: p.ToneMapSourceKind, AllAudioTracks: p.AllAudioTracks,
		},
		Attempts: p.Attempts, MaxAttempts: p.MaxAttempts, Error: logredact.SanitizeText(strings.TrimSpace(p.ErrorMessage)),
		CreatedAt:           NewInstant(p.CreatedAt),
		StartedAt:           instantPtr(p.StartedAt),
		NextRetryAt:         instantPtr(p.NextRetryAt),
		PausedAt:            instantPtr(p.PausedAt),
		ProgressUnavailable: p.ProgressUnavailable,
		Requesters:          make([]AdminDownloadPreparationRequester, 0, len(p.Requesters)),
	}
	if p.QueuePosition > 0 {
		out.QueuePosition = &p.QueuePosition
	}
	if p.State == downloads.PreparationFailed {
		out.FailedAt = instantPtr(p.CompletedAt)
	}
	for _, track := range p.SourceAudioTracks {
		out.Source.AudioTracks = append(out.Source.AudioTracks, AdminDownloadPreparationAudioTrack{
			Codec: track.Codec, Channels: positiveInt(track.Channels), Language: track.Language,
		})
	}
	if p.WorkerKind == downloads.WorkerServer || p.WorkerKind == downloads.WorkerNode {
		worker := &AdminDownloadPreparationWorker{Kind: p.WorkerKind, Name: p.WorkerName}
		if p.WorkerNodeID != nil {
			worker.NodeID = new(IDFromInt(int64(*p.WorkerNodeID)))
		}
		out.Worker = worker
	}
	if p.Progress != nil {
		out.Progress = &AdminDownloadPreparationProgress{
			EncodedSeconds:  max(p.Progress.EncodedSeconds, 0),
			DurationSeconds: max(p.Progress.DurationSeconds, 0),
			Speed:           max(p.Progress.Speed, 0),
			UpdatedAt:       NewInstant(p.Progress.UpdatedAt),
		}
	}
	for _, r := range p.Requesters {
		out.Requesters = append(out.Requesters, AdminDownloadPreparationRequester{
			UserID: IDFromInt(int64(r.UserID)), Username: r.Username,
			ProfileID: r.ProfileID, ProfileName: r.ProfileName,
			DeviceID: r.DeviceID, DeviceName: r.DeviceName, DevicePlatform: r.DevicePlatform,
			Status: r.Status,
		})
	}
	return out
}

func positiveInt(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}
