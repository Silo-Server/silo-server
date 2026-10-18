package subsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
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
	externalRows interface {
		ExternalTiming(ctx context.Context, mediaFileID int, contentSHA256 string) (*subtitles.ExternalTiming, error)
		ExternalTimingByID(ctx context.Context, id int64) (*subtitles.ExternalTiming, error)
		EnsureExternalTiming(ctx context.Context, mediaFileID int, contentSHA256, path string, format subtitles.SubtitleFormat) (*subtitles.ExternalTiming, error)
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
		CreateExternal(ctx context.Context, timing *subtitles.ExternalTiming, trigger string, requestedBy *int) (*Job, bool, error)
		Latest(ctx context.Context, subtitleID int) (*Job, error)
		LatestExternal(ctx context.Context, timingID int64) (*Job, error)
		LatestForSubtitles(ctx context.Context, subtitleIDs []int) (map[int]*Job, error)
		LatestForExternal(ctx context.Context, timingIDs []int64) (map[int64]*Job, error)
		HasJob(ctx context.Context, subtitleID int) (bool, error)
		HasExternalJob(ctx context.Context, timingID int64) (bool, error)
		MarkRunning(ctx context.Context, id int64) error
		Progress(ctx context.Context, id int64, phase string, progress float64) error
		Finish(ctx context.Context, id int64, o Outcome) error
		FinishUnchanged(ctx context.Context, job *Job, o Outcome) error
		Apply(ctx context.Context, job *Job, timing subtitles.Timing, o Outcome) (int64, error)
	}
)

// Notifier tells players of a file how syncing one of its subtitles goes,
// and when one was retimed.
type Notifier interface {
	SubtitleTimingChanged(ctx context.Context, target subtitles.SyncTarget)
	SubtitleSyncUpdated(ctx context.Context, update Update)
}

// Update is a sync job's state for the players of its subtitle's file: sent
// when the job is queued, as it runs, and when it finishes.
type Update struct {
	Target subtitles.SyncTarget
	// Timing is the subtitle's correction after this step: its current one
	// while the job runs, the result once a synced job applied it.
	Timing subtitles.Timing
	Job    Job
}

// Deps wires a Service.
type Deps struct {
	AppContext context.Context
	Jobs       *Store
	Subtitles  *subtitles.Manager
	Rows       subtitleRows
	// External stores sidecar timing corrections.
	External   externalRows
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
	jobs     jobStore
	rows     subtitleRows
	content  subtitleContent
	external externalRows
	readFile func(path string) ([]byte, error)
	// inline runs background work (player updates, syncs of played
	// subtitles) on the caller, so tests see it in order.
	inline bool
	// played remembers when this server last considered each played
	// subtitle for an automatic sync.
	playedMu  sync.Mutex
	played    map[subtitles.SyncTarget]time.Time
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
		jobs: d.Jobs, rows: d.Rows, content: d.Subtitles, external: d.External, readFile: os.ReadFile,
		files: d.Files, artifacts: d.Artifacts,
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
		s.start(ctx, job, subtitles.SyncTarget{MediaFileID: sub.MediaFileID, StoredID: sub.ID}, sub.Timing)
	}
	return job, nil
}

// RequestExternal starts a sync of a sidecar subtitle of the file, or returns
// its active job. The sidecar's bytes get a correction row (with the original
// timing) so the job has a revision to guard its result with. An automatic
// request, made when a player is first served the sidecar, follows the rules
// of an automatic Request: it does nothing (nil job) when auto sync is off,
// the format cannot be retimed, or these bytes were synced or retimed before.
func (s *Service) RequestExternal(ctx context.Context, mediaFileID int, sidecar models.ExternalSubtitle, trigger string, requestedBy *int) (*Job, error) {
	format := subtitles.SubtitleFormat(strings.ToLower(sidecar.Format))
	if !subtitles.SupportsRetime(format) {
		if trigger == TriggerAuto {
			return nil, nil
		}
		return nil, ErrUnsupportedFormat
	}
	if trigger == TriggerAuto && !s.AutoSyncEnabled(ctx) {
		return nil, nil
	}
	if s.external == nil {
		return nil, errors.New("sidecar subtitle timing is not configured")
	}
	data, err := s.readFile(sidecar.Path)
	if err != nil {
		return nil, fmt.Errorf("read sidecar subtitle: %w", err)
	}
	sha := subtitles.ContentSHA256(data)
	if trigger == TriggerAuto {
		existing, err := s.external.ExternalTiming(ctx, mediaFileID, sha)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if !existing.Timing.IsIdentity() {
				return nil, nil
			}
			done, err := s.jobs.HasExternalJob(ctx, existing.ID)
			if err != nil || done {
				return nil, err
			}
		}
	}
	row, err := s.external.EnsureExternalTiming(ctx, mediaFileID, sha, sidecar.Path, format)
	if err != nil {
		return nil, err
	}
	job, created, err := s.jobs.CreateExternal(ctx, row, trigger, requestedBy)
	if err != nil {
		return nil, err
	}
	if created {
		s.start(ctx, job, subtitles.SyncTarget{MediaFileID: mediaFileID, ExternalPath: sidecar.Path}, row.Timing)
	}
	return job, nil
}

// Latest returns the subtitle's most recent job, or nil.
func (s *Service) Latest(ctx context.Context, subtitleID int) (*Job, error) {
	return s.jobs.Latest(ctx, subtitleID)
}

// LatestExternal returns the most recent job of a sidecar's correction row,
// or nil.
func (s *Service) LatestExternal(ctx context.Context, timingID int64) (*Job, error) {
	return s.jobs.LatestExternal(ctx, timingID)
}

// LatestForSubtitles returns each subtitle's most recent job.
func (s *Service) LatestForSubtitles(ctx context.Context, subtitleIDs []int) (map[int]*Job, error) {
	return s.jobs.LatestForSubtitles(ctx, subtitleIDs)
}

// LatestForExternal returns each sidecar correction row's most recent job.
func (s *Service) LatestForExternal(ctx context.Context, timingIDs []int64) (map[int64]*Job, error) {
	return s.jobs.LatestForExternal(ctx, timingIDs)
}

// playedTTL is how long this server remembers it considered a played
// subtitle: players fetch a subtitle in windows, many times a session.
// playedRequestTimeout bounds the checks and the request for one of them.
const (
	playedTTL            = 30 * time.Minute
	playedRemembered     = 4096
	playedRequestTimeout = 30 * time.Second
)

// SubtitlePlayed starts the automatic sync of a subtitle a player was just
// served, under the rules of an automatic request. Nobody has to ask: a
// subtitle is aligned the first time anyone plays it, and every player of
// the file picks the correction up. It returns at once.
func (s *Service) SubtitlePlayed(ctx context.Context, target subtitles.SyncTarget) {
	if target.MediaFileID <= 0 || (target.StoredID == 0 && target.ExternalPath == "") || !s.firstPlay(target) {
		return
	}
	if s.inline {
		s.syncPlayed(ctx, target)
		return
	}
	go s.syncPlayed(context.WithoutCancel(ctx), target)
}

// firstPlay reports whether target was not considered within playedTTL, and
// records that it is now.
func (s *Service) firstPlay(target subtitles.SyncTarget) bool {
	now := s.now()
	s.playedMu.Lock()
	defer s.playedMu.Unlock()
	if at, ok := s.played[target]; ok && now.Sub(at) < playedTTL {
		return false
	}
	if s.played == nil {
		s.played = map[subtitles.SyncTarget]time.Time{}
	}
	if len(s.played) >= playedRemembered {
		for key, at := range s.played {
			if now.Sub(at) >= playedTTL {
				delete(s.played, key)
			}
		}
	}
	s.played[target] = now
	return true
}

func (s *Service) syncPlayed(ctx context.Context, target subtitles.SyncTarget) {
	ctx, cancel := context.WithTimeout(ctx, playedRequestTimeout)
	defer cancel()
	var err error
	if target.StoredID != 0 {
		_, err = s.Request(ctx, target.StoredID, TriggerAuto, nil)
	} else {
		err = s.syncPlayedSidecar(ctx, target)
	}
	if err != nil && !errors.Is(err, ErrSubtitleNotFound) {
		slog.WarnContext(ctx, "automatic subtitle sync not started", "component", "subsync",
			"media_file_id", target.MediaFileID, "subtitle_id", target.StoredID, "error", err)
		// A failure to even ask (an unreadable file, the database) lets the
		// next fetch of the subtitle try again.
		s.forgetPlay(target)
	}
}

// forgetPlay drops the record that target was considered.
func (s *Service) forgetPlay(target subtitles.SyncTarget) {
	s.playedMu.Lock()
	defer s.playedMu.Unlock()
	delete(s.played, target)
}

func (s *Service) syncPlayedSidecar(ctx context.Context, target subtitles.SyncTarget) error {
	file, err := s.files.GetByID(ctx, target.MediaFileID)
	if err != nil {
		return fmt.Errorf("load media file: %w", err)
	}
	if file == nil {
		return nil
	}
	for _, sidecar := range file.ExternalSubtitles {
		if sidecar.Path == target.ExternalPath {
			_, err := s.RequestExternal(ctx, file.ID, sidecar, TriggerAuto, nil)
			return err
		}
	}
	return nil
}

// TimingChanged tells players a subtitle's timing changed outside a job,
// such as a manual adjustment.
func (s *Service) TimingChanged(ctx context.Context, target subtitles.SyncTarget) {
	if s.notifier != nil {
		s.notifier.SubtitleTimingChanged(ctx, target)
	}
}

// updated tells players of the file about a job's state; best effort.
func (s *Service) updated(ctx context.Context, target subtitles.SyncTarget, timing subtitles.Timing, job Job) {
	if s.notifier != nil && target.MediaFileID > 0 {
		s.notifier.SubtitleSyncUpdated(context.WithoutCancel(ctx), Update{Target: target, Timing: timing, Job: job})
	}
}

// start runs a new job and tells players of the file it is queued. The job
// starts first, and the update goes out on its own: a slow player connection
// must not delay the request, or the job past its stale-job cutoff. Players
// treat a queued update that arrives after the job's progress as old.
func (s *Service) start(ctx context.Context, job *Job, target subtitles.SyncTarget, timing subtitles.Timing) {
	queued := *job
	s.dispatch(job)
	if s.inline {
		s.updated(ctx, target, timing, queued)
		return
	}
	go s.updated(context.WithoutCancel(ctx), target, timing, queued)
}

func (s *Service) dispatch(job *Job) {
	s.runner.Dispatch(job.ID, func(ctx context.Context) {
		s.execute(ctx, job)
	}, func(ctx context.Context) {
		_ = s.jobs.Finish(ctx, job.ID, Outcome{Status: JobFailed, Error: "canceled", Failure: FailureUnavailable})
	})
}

// run is one execution of a job: the subject it found and the state players
// last heard about.
type run struct {
	s       *Service
	job     Job
	subject subject
	// stop ends the run's work early once its job is known to have ended.
	stop context.CancelCauseFunc
	// progress sends progress steps off the worker; nil sends them inline.
	progress *updateQueue
}

// send tells players of the file about the job as given.
func (r *run) send(ctx context.Context, job Job) {
	r.s.updated(ctx, r.subject.target, r.timingNow(context.WithoutCancel(ctx)), job)
}

// report records a running job's phase and progress, on the job row and for
// players of its file.
func (r *run) report(ctx context.Context, phase string, progress float64) {
	r.job.Status, r.job.Phase, r.job.Progress = JobRunning, phase, &progress
	// A job reaped or deleted with a replaced file is no longer running:
	// stop working on it, and say nothing.
	if err := r.s.jobs.Progress(context.WithoutCancel(ctx), r.job.ID, phase, progress); errors.Is(err, jobrunner.ErrJobTerminal) {
		if r.stop != nil {
			r.stop(err)
		}
		return
	}
	if r.progress == nil {
		r.send(ctx, r.job)
		return
	}
	r.progress.offer(r.job)
}

// timingNow is the subject's correction as stored now: another viewer can
// retime it while the job runs, and an update must not undo that on their
// screens. The correction read at the start stands in when the row cannot
// be read.
func (r *run) timingNow(ctx context.Context) subtitles.Timing {
	switch {
	case r.job.ExternalTimingID != 0 && r.s.external != nil:
		if row, err := r.s.external.ExternalTimingByID(ctx, r.job.ExternalTimingID); err == nil && row != nil {
			return row.Timing
		}
	case r.job.SubtitleID != 0:
		if row, err := r.s.rows.GetDownloadedSubtitle(ctx, r.job.SubtitleID); err == nil && row != nil {
			return row.Timing
		}
	}
	return r.subject.timing
}

// finished tells players of the file how the job ended; timing is the
// subtitle's correction now.
func (r *run) finished(ctx context.Context, o Outcome, timing subtitles.Timing) {
	if r.progress != nil {
		r.progress.close()
	}
	r.job.Status, r.job.Phase, r.job.Progress = o.Status, "", nil
	r.job.Confidence, r.job.Result, r.job.Error, r.job.Failure = o.Confidence, o.Result, o.Error, o.Failure
	r.job.FinishedAt = new(r.s.now())
	r.s.updated(ctx, r.subject.target, timing, r.job)
}

func (s *Service) execute(ctx context.Context, job *Job) {
	if err := s.jobs.MarkRunning(ctx, job.ID); err != nil {
		return
	}
	started := s.now()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	r := &run{s: s, job: *job, stop: stop}
	if !s.inline {
		r.progress = newUpdateQueue(func(job Job) { r.send(ctx, job) })
		defer r.progress.close()
	}
	outcome, err := s.align(ctx, r)
	log := slog.With("component", "subsync", "job_id", job.ID, "subtitle_id", job.SubtitleID,
		"external_timing_id", job.ExternalTimingID, "media_file_id", job.MediaFileID, "trigger", job.Trigger,
		"executed_on", outcome.ExecutedOn, "duration_ms", s.now().Sub(started).Milliseconds())
	if errors.Is(context.Cause(ctx), jobrunner.ErrJobTerminal) {
		log.InfoContext(ctx, "subtitle sync stopped: the job ended meanwhile")
		return
	}
	finishCtx := context.WithoutCancel(ctx)
	// stage says where a failure happened: aligning, or applying its result.
	fail := func(err error, stage string) {
		outcome.Status, outcome.Error, outcome.Failure = JobFailed, err.Error(), failureOf(err)
		log.WarnContext(ctx, "subtitle sync failed", "stage", stage, "error", err, "failure", outcome.Failure)
		if err := s.jobs.Finish(finishCtx, job.ID, outcome); err == nil {
			r.finished(finishCtx, outcome, r.timingNow(finishCtx))
		}
	}
	if err != nil {
		fail(err, "align")
		return
	}
	var found subtitles.Timing
	if outcome.Result != nil {
		found = *outcome.Result
	}
	log = log.With("status", outcome.Status, "confidence", deref(outcome.Confidence),
		"offset_ms", found.OffsetMS, "scale", found.Normalized().Scale)
	// The outcome speaks for the subject as the job read it; a subject that
	// changed meanwhile ends the job as changed instead.
	if err := s.subjectStillCurrent(finishCtx, r); err != nil {
		fail(err, "finish")
		return
	}
	if outcome.Status != string(StatusSynced) {
		err := s.jobs.FinishUnchanged(finishCtx, job, outcome)
		switch {
		case errors.Is(err, ErrSubtitleChanged):
			fail(err, "finish")
		case err == nil:
			log.InfoContext(ctx, "subtitle sync finished")
			r.finished(finishCtx, outcome, r.timingNow(finishCtx))
		case !errors.Is(err, jobrunner.ErrJobTerminal):
			log.WarnContext(ctx, "subtitle sync outcome not recorded", "error", err)
		}
		return
	}
	if err := subtitles.ValidateTiming(found); err != nil {
		fail(err, "result out of range")
		return
	}
	if _, err := s.jobs.Apply(finishCtx, job, found, outcome); err != nil {
		// A job that is already terminal (reaped, or deleted with a replaced
		// file) stays as it is; any other failure ends it so a new sync can
		// start.
		if errors.Is(err, jobrunner.ErrJobTerminal) {
			log.WarnContext(ctx, "subtitle sync result not applied", "error", err)
			return
		}
		fail(err, "apply")
		return
	}
	log.InfoContext(ctx, "subtitle sync applied")
	r.finished(finishCtx, outcome, found)
	s.TimingChanged(finishCtx, r.subject.target)
}

// subject is what a job aligns: the subtitle's original bytes, as stored or
// on disk, and its current correction.
type subject struct {
	target   subtitles.SyncTarget
	format   subtitles.SubtitleFormat
	language string
	timing   subtitles.Timing
	data     []byte
}

// loadSubject reads the job's subject. It is ErrSubtitleChanged when the
// subject no longer has the job's base revision or belongs to another file,
// or a sidecar's bytes or place in the file's catalog changed. The target is set as soon as it is
// known, failure or not.
func (s *Service) loadSubject(ctx context.Context, job *Job, file func() (*models.MediaFile, error)) (subject, error) {
	if job.ExternalTimingID == 0 {
		subj := subject{target: subtitles.SyncTarget{MediaFileID: job.MediaFileID, StoredID: job.SubtitleID}}
		sub, data, err := s.content.GetSubtitleContent(ctx, job.SubtitleID)
		if err != nil {
			return subj, err
		}
		subj.timing = sub.Timing
		if sub.Revision != job.BaseRevision || sub.MediaFileID != job.MediaFileID {
			return subj, ErrSubtitleChanged
		}
		subj.format, subj.language, subj.data = sub.Format, sub.Language, data
		return subj, nil
	}
	if s.external == nil {
		return subject{}, errors.New("sidecar subtitle timing is not configured")
	}
	row, err := s.external.ExternalTimingByID(ctx, job.ExternalTimingID)
	if err != nil {
		return subject{}, err
	}
	if row == nil {
		return subject{}, ErrSubtitleChanged
	}
	subj := subject{target: subtitles.SyncTarget{MediaFileID: job.MediaFileID, ExternalPath: row.Path}, timing: row.Timing}
	if row.Revision != job.BaseRevision || row.MediaFileID != job.MediaFileID {
		return subj, ErrSubtitleChanged
	}
	f, err := file()
	if err != nil {
		return subj, err
	}
	data, sidecar, err := s.currentSidecar(row.Path, row.ContentSHA256, f)
	if err != nil {
		return subj, err
	}
	subj.format, subj.language, subj.data = row.Format, sidecar.Language, data
	return subj, nil
}

// currentSidecar reads the sidecar at path. It is ErrSubtitleChanged when
// the file is gone, its bytes are not the ones contentSHA256 names, or the
// scanner no longer lists it under file.
func (s *Service) currentSidecar(path, contentSHA256 string, file *models.MediaFile) ([]byte, *models.ExternalSubtitle, error) {
	data, err := s.readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ErrSubtitleChanged
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read sidecar subtitle: %w", err)
	}
	if subtitles.ContentSHA256(data) != contentSHA256 {
		return nil, nil, ErrSubtitleChanged
	}
	for i := range file.ExternalSubtitles {
		if file.ExternalSubtitles[i].Path == path {
			return data, &file.ExternalSubtitles[i], nil
		}
	}
	return nil, nil, ErrSubtitleChanged
}

// subjectStillCurrent checks, before a job records its outcome, that its
// subject did not change while the audio was analyzed: a new revision (another
// viewer's timing, an edit), or a sidecar edited, removed, or dropped from the
// catalog. The outcome would describe, or correct, a subtitle no longer
// served.
func (s *Service) subjectStillCurrent(ctx context.Context, r *run) error {
	if r.job.ExternalTimingID == 0 {
		row, err := s.rows.GetDownloadedSubtitle(ctx, r.job.SubtitleID)
		if err != nil {
			return fmt.Errorf("read stored subtitle: %w", err)
		}
		if row == nil || row.Revision != r.job.BaseRevision || row.MediaFileID != r.job.MediaFileID {
			return ErrSubtitleChanged
		}
		return nil
	}
	if s.external == nil {
		return errors.New("sidecar subtitle timing is not configured")
	}
	row, err := s.external.ExternalTimingByID(ctx, r.job.ExternalTimingID)
	if err != nil {
		return fmt.Errorf("read sidecar subtitle timing: %w", err)
	}
	if row == nil || row.Revision != r.job.BaseRevision {
		return ErrSubtitleChanged
	}
	file, err := s.files.GetByID(ctx, r.job.MediaFileID)
	if err != nil {
		return fmt.Errorf("load media file: %w", err)
	}
	if file == nil {
		return ErrSubtitleChanged
	}
	_, _, err = s.currentSidecar(r.subject.target.ExternalPath, subtitles.ContentSHA256(r.subject.data), file)
	return err
}

// Progress the phases report: decoding speech fills most of the bar, since
// it takes almost all of a job's time; matching takes a fraction of a second.
const (
	analyzingShare = 0.9
	matchingStart  = 0.95
)

// align computes the job's outcome, reporting progress as it goes. The
// outcome's Result is the correction found, which a synced outcome applies.
func (s *Service) align(ctx context.Context, r *run) (Outcome, error) {
	job := &r.job
	var file *models.MediaFile
	loadFile := func() (*models.MediaFile, error) {
		if file != nil {
			return file, nil
		}
		f, err := s.files.GetByID(ctx, job.MediaFileID)
		if err != nil {
			return nil, fmt.Errorf("load media file: %w", err)
		}
		if f == nil {
			return nil, errors.New("media file not found")
		}
		file = f
		return file, nil
	}
	subj, err := s.loadSubject(ctx, job, loadFile)
	r.subject = subj
	if err != nil {
		return Outcome{}, err
	}
	r.report(ctx, PhaseAnalyzing, 0)
	cues, err := subtitles.ParseCuesForFormat(subj.format, subj.data)
	if err != nil {
		return Outcome{}, fmt.Errorf("read subtitle cues: %w", err)
	}
	if _, err := loadFile(); err != nil {
		return Outcome{}, err
	}
	decoded := func(done, total int) {
		r.report(ctx, PhaseAnalyzing, analyzingShare*float64(done)/float64(total))
	}
	windows, plan, executedOn, err := s.speech(ctx, file, subj.language, job.Trigger == TriggerAuto, decoded)
	outcome := Outcome{ExecutedOn: executedOn}
	if err != nil {
		return outcome, err
	}
	r.report(ctx, PhaseMatching, matchingStart)
	alignment, err := Align(windows, cues)
	if errors.Is(err, ErrNoSpeech) {
		outcome.Status = string(StatusNoMatch)
		outcome.Confidence = new(0.0)
		return outcome, nil
	}
	if err != nil {
		return outcome, err
	}
	outcome.Status = string(Decide(alignment, subj.timing, time.Duration(plan.Runtime*float64(time.Second))))
	outcome.Confidence = &alignment.Confidence
	outcome.Result = &alignment.Timing
	return outcome, nil
}

// Causes failureOf tells apart.
var (
	errNoUsableAudio   = errors.New("no usable audio")
	errAnalysisBackoff = errors.New("speech analysis is backing off after a failure")
)

// failureOf names why a job failed, for clients.
func failureOf(err error) string {
	switch {
	case errors.Is(err, ErrSubtitleChanged):
		return FailureSubtitleChanged
	case errors.Is(err, errNoUsableAudio):
		return FailureNoAudio
	case errors.Is(err, errNoNode), errors.Is(err, errAnalysisBackoff),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureUnavailable
	}
	var sampleErr *mediasample.Error
	var remoteErr *mediasample.RemoteError
	if errors.As(err, &sampleErr) || errors.As(err, &remoteErr) {
		// The file itself fails the decode, or the host, node, or ffmpeg
		// does and a later attempt can pass.
		if failureReason(err).Permanent() {
			return FailureNoAudio
		}
		return FailureUnavailable
	}
	return FailureError
}

// speech returns the file's speech levels from the artifact cache or by
// decoding them. A center-channel plan that the file cannot satisfy is
// recorded unusable and the plan falls back to a downmix of every channel.
// decoded, when set, hears how many of a plan's windows are done.
func (s *Service) speech(ctx context.Context, file *models.MediaFile, language string, background bool, decoded func(done, total int)) ([]mediasample.SpeechLevels, speechPlan, string, error) {
	plan, err := planSpeech(file, language)
	if err != nil {
		return nil, plan, "", fmt.Errorf("%w: %w", errNoUsableAudio, err)
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
			if cached.Status == mediaartifact.StatusUnusable {
				lastErr = fmt.Errorf("%w: %s", errNoUsableAudio, cached.Detail)
				continue
			}
			if background {
				lastErr = fmt.Errorf("%w: %s", errAnalysisBackoff, cached.LastError)
				continue
			}
		}

		reqs := p.requests(file.FilePath, background)
		var done func(int)
		if decoded != nil {
			done = func(n int) { decoded(n, len(reqs)) }
		}
		results, executedOn, err := s.sampler.run(ctx, reqs, done)
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
