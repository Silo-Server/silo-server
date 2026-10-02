package subsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// SettingAutoSync turns automatic sync after a subtitle download or upload
// on or off.
const SettingAutoSync = "subtitles.auto_sync"

// concurrentJobs bounds how many syncs this server runs at once. Each one
// mostly waits on ffmpeg, here or on a node.
const concurrentJobs = 2

// ErrUnsupportedFormat reports a subtitle format sync cannot retime.
var ErrUnsupportedFormat = errors.New("subtitle format cannot be synced")

// ErrSubtitleNotFound reports a missing subtitle row.
var ErrSubtitleNotFound = errors.New("subtitle not found")

// Dependencies of the service, narrowed for tests.
type (
	subtitleRows interface {
		GetDownloadedSubtitle(ctx context.Context, id int) (*subtitles.DownloadedSubtitle, error)
	}
	subtitleContent interface {
		GetSubtitleContent(ctx context.Context, id int) (*subtitles.DownloadedSubtitle, []byte, error)
	}
	mediaFiles interface {
		GetByID(ctx context.Context, id int) (*models.MediaFile, error)
	}
	artifactStore interface {
		Load(ctx context.Context, fileID int, key mediaartifact.Key) (*mediaartifact.Artifact, error)
		Upsert(ctx context.Context, a mediaartifact.Artifact) error
		RecordFailure(ctx context.Context, failure mediaartifact.Failure) error
	}
	jobStore interface {
		jobrunner.Store
		Create(ctx context.Context, sub *subtitles.DownloadedSubtitle, trigger string, requestedBy *int) (*Job, bool, error)
		Latest(ctx context.Context, subtitleID int) (*Job, error)
		LatestForSubtitles(ctx context.Context, subtitleIDs []int) (map[int]*Job, error)
		HasJob(ctx context.Context, subtitleID int) (bool, error)
		MarkRunning(ctx context.Context, id int64) error
		Finish(ctx context.Context, id int64, o Outcome) error
		Apply(ctx context.Context, job *Job, timing subtitles.Timing, o Outcome) (int64, error)
	}
)

// Notifier tells players of a file that one of its subtitles was retimed.
type Notifier interface {
	SubtitleTimingChanged(ctx context.Context, mediaFileID, subtitleID int)
}

// Deps wires a Service.
type Deps struct {
	AppContext context.Context
	Jobs       *Store
	Subtitles  *subtitles.Manager
	Rows       subtitleRows
	Files      mediaFiles
	Artifacts  *mediaartifact.Store
	Settings   SettingsReader
	Nodes      NodeSource
	FFmpegPath func() string
	Notifier   Notifier
	// Node names this server in artifact failure records.
	Node string
}

// Service runs subtitle sync jobs.
type Service struct {
	jobs      jobStore
	rows      subtitleRows
	content   subtitleContent
	files     mediaFiles
	artifacts artifactStore
	settings  SettingsReader
	sampler   *sampler
	runner    *jobrunner.Runner
	notifier  Notifier
	node      string
	now       func() time.Time
}

// NewService builds a Service and starts its stale-job recovery.
func NewService(d Deps) *Service {
	s := &Service{
		jobs: d.Jobs, rows: d.Rows, content: d.Subtitles, files: d.Files, artifacts: d.Artifacts,
		settings: d.Settings, sampler: newSampler(d.Settings, d.Nodes, d.FFmpegPath),
		notifier: d.Notifier, node: d.Node, now: time.Now,
	}
	if s.node == "" {
		s.node = "silo"
	}
	s.runner = jobrunner.New(d.AppContext, jobrunner.NewSemaphore(concurrentJobs), d.Jobs, "subtitle sync", nil)
	s.runner.Recover()
	return s
}

// AutoSyncEnabled reports whether new subtitles are synced automatically.
// It is on unless the setting says otherwise.
func (s *Service) AutoSyncEnabled(ctx context.Context) bool {
	if s.settings == nil {
		return true
	}
	value, err := s.settings.Get(ctx, SettingAutoSync)
	return err != nil || value != "false"
}

// Request starts a sync of the subtitle, or returns the job already running.
// An automatic request does nothing (nil job) when auto sync is off, the
// format cannot be retimed, or the subtitle was synced or retimed before. A
// download or upload of identical content returns the existing row, and
// automatic sync must not replace a timing someone already set.
func (s *Service) Request(ctx context.Context, subtitleID int, trigger string, requestedBy *int) (*Job, error) {
	sub, err := s.rows.GetDownloadedSubtitle(ctx, subtitleID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, ErrSubtitleNotFound
	}
	if !subtitles.SupportsRetime(sub.Format) {
		if trigger == TriggerAuto {
			return nil, nil
		}
		return nil, ErrUnsupportedFormat
	}
	if trigger == TriggerAuto {
		if !s.AutoSyncEnabled(ctx) {
			return nil, nil
		}
		if !sub.Timing.IsIdentity() {
			return nil, nil
		}
		done, err := s.jobs.HasJob(ctx, sub.ID)
		if err != nil || done {
			return nil, err
		}
	}
	job, created, err := s.jobs.Create(ctx, sub, trigger, requestedBy)
	if err != nil {
		return nil, err
	}
	if created {
		s.dispatch(job)
	}
	return job, nil
}

// Latest returns the subtitle's most recent job, or nil.
func (s *Service) Latest(ctx context.Context, subtitleID int) (*Job, error) {
	return s.jobs.Latest(ctx, subtitleID)
}

// LatestForSubtitles returns each subtitle's most recent job.
func (s *Service) LatestForSubtitles(ctx context.Context, subtitleIDs []int) (map[int]*Job, error) {
	return s.jobs.LatestForSubtitles(ctx, subtitleIDs)
}

// TimingChanged tells players a subtitle's timing changed outside a job,
// such as a manual adjustment.
func (s *Service) TimingChanged(ctx context.Context, mediaFileID, subtitleID int) {
	if s.notifier != nil {
		s.notifier.SubtitleTimingChanged(ctx, mediaFileID, subtitleID)
	}
}

func (s *Service) dispatch(job *Job) {
	s.runner.Dispatch(job.ID, func(ctx context.Context) {
		s.execute(ctx, job)
	}, func(ctx context.Context) {
		_ = s.jobs.Finish(ctx, job.ID, Outcome{Status: JobFailed, Error: "canceled"})
	})
}

func (s *Service) execute(ctx context.Context, job *Job) {
	if err := s.jobs.MarkRunning(ctx, job.ID); err != nil {
		return
	}
	started := s.now()
	outcome, err := s.align(ctx, job)
	log := slog.With("component", "subsync", "job_id", job.ID, "subtitle_id", job.SubtitleID,
		"media_file_id", job.MediaFileID, "trigger", job.Trigger, "executed_on", outcome.ExecutedOn,
		"duration_ms", s.now().Sub(started).Milliseconds())
	finishCtx := context.WithoutCancel(ctx)
	if err != nil {
		outcome.Status, outcome.Error = JobFailed, err.Error()
		log.WarnContext(ctx, "subtitle sync failed", "error", err)
		_ = s.jobs.Finish(finishCtx, job.ID, outcome)
		return
	}
	var found subtitles.Timing
	if outcome.Result != nil {
		found = *outcome.Result
	}
	log = log.With("status", outcome.Status, "confidence", deref(outcome.Confidence),
		"offset_ms", found.OffsetMS, "scale", found.Normalized().Scale)
	if outcome.Status != string(StatusSynced) {
		log.InfoContext(ctx, "subtitle sync finished")
		_ = s.jobs.Finish(finishCtx, job.ID, outcome)
		return
	}
	if err := subtitles.ValidateTiming(found); err != nil {
		outcome.Status, outcome.Error = JobFailed, err.Error()
		log.WarnContext(ctx, "subtitle sync result out of range", "error", err)
		_ = s.jobs.Finish(finishCtx, job.ID, outcome)
		return
	}
	if _, err := s.jobs.Apply(finishCtx, job, found, outcome); err != nil {
		// A job that is already terminal (reaped meanwhile) stays as it is;
		// any other failure ends it so a new sync can start.
		if !errors.Is(err, jobrunner.ErrJobTerminal) {
			outcome.Status, outcome.Error = JobFailed, err.Error()
			_ = s.jobs.Finish(finishCtx, job.ID, outcome)
		}
		log.WarnContext(ctx, "subtitle sync result not applied", "error", err)
		return
	}
	log.InfoContext(ctx, "subtitle sync applied")
	s.TimingChanged(finishCtx, job.MediaFileID, job.SubtitleID)
}

// align computes the job's outcome. Its Result is the correction found, which
// a synced outcome applies.
func (s *Service) align(ctx context.Context, job *Job) (Outcome, error) {
	sub, data, err := s.content.GetSubtitleContent(ctx, job.SubtitleID)
	if err != nil {
		return Outcome{}, err
	}
	if sub.Revision != job.BaseRevision {
		return Outcome{}, ErrSubtitleChanged
	}
	cues, err := subtitles.ParseCuesForFormat(sub.Format, data)
	if err != nil {
		return Outcome{}, fmt.Errorf("read subtitle cues: %w", err)
	}
	file, err := s.files.GetByID(ctx, sub.MediaFileID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load media file: %w", err)
	}
	if file == nil {
		return Outcome{}, errors.New("media file not found")
	}
	windows, plan, executedOn, err := s.speech(ctx, file, sub.Language, job.Trigger == TriggerAuto)
	outcome := Outcome{ExecutedOn: executedOn}
	if err != nil {
		return outcome, err
	}
	alignment, err := Align(windows, cues)
	if errors.Is(err, ErrNoSpeech) {
		outcome.Status = string(StatusNoMatch)
		outcome.Confidence = new(0.0)
		return outcome, nil
	}
	if err != nil {
		return outcome, err
	}
	outcome.Status = string(Decide(alignment, sub.Timing, time.Duration(plan.Runtime*float64(time.Second))))
	outcome.Confidence = &alignment.Confidence
	outcome.Result = &alignment.Timing
	return outcome, nil
}

// speech returns the file's speech levels from the artifact cache or by
// decoding them. A center-channel plan that the file cannot satisfy is
// recorded unusable and the plan falls back to a downmix of every channel.
func (s *Service) speech(ctx context.Context, file *models.MediaFile, language string, background bool) ([]mediasample.SpeechLevels, speechPlan, string, error) {
	plan, err := planSpeech(file, language)
	if err != nil {
		return nil, plan, "", err
	}
	plans := []speechPlan{plan}
	if plan.CenterChannel {
		downmix := plan
		downmix.CenterChannel = false
		plans = append(plans, downmix)
	}
	var lastErr error
	for i, p := range plans {
		last := i == len(plans)-1
		key, identity := p.key(), p.identity(file)
		cached, err := s.artifacts.Load(ctx, file.ID, key)
		if err != nil {
			return nil, p, "", err
		}
		switch cached.State(identity, s.node, s.now()) {
		case mediaartifact.Ready:
			windows, err := decodeSpeech(cached.Payload)
			if err == nil {
				return windows, p, "cache", nil
			}
		case mediaartifact.Skipped:
			// Unusable, or failing on this server and backing off; a
			// manual request retries a failure at once.
			if cached.Status == mediaartifact.StatusUnusable || background {
				lastErr = fmt.Errorf("speech analysis unavailable: %s%s", cached.Detail, cached.LastError)
				continue
			}
		}

		results, executedOn, err := s.sampler.run(ctx, p.requests(file.FilePath, background))
		if err == nil {
			windows := make([]mediasample.SpeechLevels, 0, len(results))
			for _, r := range results {
				if r.Speech != nil {
					windows = append(windows, *r.Speech)
				}
			}
			_ = s.artifacts.Upsert(context.WithoutCancel(ctx), mediaartifact.Artifact{
				MediaFileID: file.ID, Key: key, Identity: identity, Status: mediaartifact.StatusComplete,
				PayloadFormat: speechPayloadFormat, ItemCount: len(windows), Payload: encodeSpeech(windows),
				SampleDurationSeconds: float64(len(windows) * windowSeconds), RecordedBy: s.node,
			})
			return windows, p, executedOn, nil
		}
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, errNoNode) {
			return nil, p, executedOn, err
		}
		// A failure the file itself causes rules the plan out. So does an
		// unexplained ffmpeg failure of the center-channel decode, which a
		// stream without that channel produces, while a downmix can follow.
		// Failures of the host, node, or process (timeouts, missing filters,
		// kills) back off and are retried, on any server.
		reason := failureReason(err)
		unusable := reason.Permanent() || (!last && reason == mediasample.ReasonFailed)
		if unusable {
			_ = s.artifacts.Upsert(context.WithoutCancel(ctx), mediaartifact.Artifact{
				MediaFileID: file.ID, Key: key, Identity: identity, Status: mediaartifact.StatusUnusable,
				Detail: string(reason), RecordedBy: s.node,
			})
		} else {
			_ = s.artifacts.RecordFailure(context.WithoutCancel(ctx), mediaartifact.Failure{
				MediaFileID: file.ID, Key: key, Identity: identity, RecordedBy: s.node, Error: err.Error(),
			})
		}
		if last {
			return nil, p, executedOn, err
		}
	}
	return nil, plan, "", lastErr
}

func failureReason(err error) mediasample.Reason {
	var remoteErr *mediasample.RemoteError
	if errors.As(err, &remoteErr) {
		return remoteErr.Reason
	}
	return mediasample.Classify(err)
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
