package trickplay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Settings the service reads live, so a change applies to the next claim.
const (
	// WidthSetting is shared with chapter thumbnails: one preview image
	// width for both.
	WidthSetting     = config.PreviewImageWidthSettingKey
	IntervalSetting  = "playback.trickplay_interval_seconds"
	WorkersSetting   = "playback.trickplay_workers"
	ExecutionSetting = "playback.trickplay_execution"

	ffmpegPathSetting = "playback.ffmpeg_path"
	hwAccelSetting    = "playback.hw_accel"
	hwDeviceSetting   = "playback.hw_device"
)

// Execution modes of ExecutionSetting.
const (
	ExecutionLocal                = "local"
	ExecutionPreferTranscodeNodes = "prefer_transcode_nodes"
	ExecutionTranscodeNodesOnly   = "transcode_nodes_only"
)

const (
	// leaseDuration is how long a claim holds a file without a heartbeat;
	// heartbeatInterval renews it well before then.
	leaseDuration     = 90 * time.Second
	heartbeatInterval = 30 * time.Second
	// idlePoll is how often an idle server looks for work it was not told
	// about; Kick wakes it sooner.
	idlePoll = time.Minute
	// reconcileRetryDelay spaces another attempt at a requested reconcile
	// that another server's lock or an error skipped.
	reconcileRetryDelay = 30 * time.Second
	// maxChunkSheets bounds the sheets one ffmpeg run makes, which bounds
	// the memory of a run and the size of a transcode node's answer.
	maxChunkSheets = 16
	// Bound decoded output as well as sheet count. The first sheet learns
	// actual display geometry before batching subsequent sheets.
	maxChunkPixels = 64 << 20
	// runThreads caps each ffmpeg run's decoder and filter threads. Work
	// runs at idle priority, so more parallelism comes from more workers.
	runThreads = 2
	// reconcileBatch bounds each reconcile step.
	reconcileBatch = 5000
	// reconcilePassBatches yields the cluster lock after a bounded pass;
	// a full final batch schedules another pass immediately.
	reconcilePassBatches = 4
	// releaseDelay is how long a file waits after this server gave it back
	// through no fault of the file: its input unreadable here, or shutdown.
	releaseDelay = time.Hour
)

// reconcileLock serializes reconcile passes across servers ("SILOTRKP").
const reconcileLock int64 = 0x53494C4F54524B50

// errLeaseLost ends a generation whose lease another server now holds.
var errLeaseLost = errors.New("trickplay lease lost")

// SettingsReader reads admin settings.
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// Store is where sheets are written.
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Identity() string
}

// Extractor makes one sheets request's sheets for a job: on this server or
// on a transcode node.
type Extractor interface {
	Extract(ctx context.Context, job *Job, req mediasample.Request) (mediasample.Result, error)
}

// queue is the Repository surface the service uses; tests replace it.
type queue interface {
	Claim(ctx context.Context, owner string, lease time.Duration) (*Job, error)
	Heartbeat(ctx context.Context, fileID int, owner string, lease time.Duration) (bool, error)
	BeginUpload(ctx context.Context, fileID int, owner string) (int64, bool, error)
	Publish(ctx context.Context, fileID int, owner string, revision int64, p Published) (bool, error)
	Finish(ctx context.Context, fileID int, owner string, outcome Outcome, cause string, delay time.Duration) (bool, error)
	Reconcile(ctx context.Context, recipe Recipe, storeIdentity string, batch int) (ReconcileStats, error)
}

// Service generates the sheets of queued files. Every server runs one; the
// number of files a server works on at once follows WorkersSetting.
type Service struct {
	pool      *pgxpool.Pool
	queue     queue
	store     Store
	settings  SettingsReader
	extractor Extractor
	owner     string
	limiter   *mediasample.Limiter
	logger    *slog.Logger
	wake      chan struct{}
	reconcile chan struct{}
	now       func() time.Time
	// heartbeatEvery paces lease renewals, settingsEvery paces rereading
	// the settings, and reconcileRetry paces another attempt at a requested
	// pass that another server's lock or an error skipped; tests replace
	// them, and reconcilePass.
	heartbeatEvery time.Duration
	settingsEvery  time.Duration
	reconcileRetry time.Duration
	reconcilePass  func(ctx context.Context) (ReconcileStats, bool, error)
}

// NewService returns a service that claims work as nodeID. It returns nil
// without a database or a store.
func NewService(pool *pgxpool.Pool, store Store, settings SettingsReader, extractor Extractor, nodeID string) *Service {
	if pool == nil || store == nil || extractor == nil {
		return nil
	}
	s := newService(NewRepository(pool), store, settings, extractor, nodeID)
	s.pool = pool
	return s
}

func newService(q queue, store Store, settings SettingsReader, extractor Extractor, nodeID string) *Service {
	s := &Service{
		queue:          q,
		store:          store,
		settings:       settings,
		extractor:      extractor,
		owner:          leaseOwner(nodeID),
		limiter:        mediasample.NewLimiter(1),
		logger:         slog.Default().With("component", "trickplay"),
		wake:           make(chan struct{}, 1),
		reconcile:      make(chan struct{}, 1),
		now:            time.Now,
		heartbeatEvery: heartbeatInterval,
		settingsEvery:  idlePoll,
		reconcileRetry: reconcileRetryDelay,
	}
	s.reconcilePass = s.Reconcile
	return s
}

// leaseOwner names this process in leases: the node, and a token that is
// new on every start, so a restarted server never acts on its previous
// process's leases.
func leaseOwner(nodeID string) string {
	var token [4]byte
	_, _ = rand.Read(token[:])
	return strings.TrimSpace(nodeID) + ":" + hex.EncodeToString(token[:])
}

// Kick wakes the service to look for work now.
func (s *Service) Kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ReconcileSoon runs a reconcile pass in the background, as after a
// settings change. Requests made while one runs fold into the next.
func (s *Service) ReconcileSoon() {
	select {
	case s.reconcile <- struct{}{}:
	default:
	}
}

// Start runs the service until ctx ends.
func (s *Service) Start(ctx context.Context) {
	go s.dispatch(ctx)
	go s.reconcileOnRequest(ctx)
}

func (s *Service) reconcileOnRequest(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.reconcile:
		}
		_, ran, err := s.reconcilePass(ctx)
		if err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "trickplay reconcile failed", "error", err)
		}
		if ran && err == nil {
			continue
		}
		// Another server held the lock or the pass failed. A settings change
		// is noticed once per server, so try again rather than leave the
		// old recipe in place until the periodic pass.
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.reconcileRetry):
		}
		s.ReconcileSoon()
	}
}

// dispatch claims files while a worker slot is free, and runs each in its
// own goroutine. The number of slots follows WorkersSetting.
func (s *Service) dispatch(ctx context.Context) {
	var running sync.WaitGroup
	defer running.Wait()
	running.Go(func() { s.followSettings(ctx) })
	for ctx.Err() == nil {
		release, err := s.limiter.Acquire(ctx)
		if err != nil {
			return
		}
		job, err := s.queue.Claim(ctx, s.owner, leaseDuration)
		if err != nil || job == nil {
			release()
			if err != nil && ctx.Err() == nil {
				s.logger.WarnContext(ctx, "claim trickplay work failed", "error", err)
			}
			s.idle(ctx)
			continue
		}
		running.Go(func() {
			defer release()
			s.process(ctx, job)
		})
	}
}

// followSettings applies setting changes as they are read: the worker count
// resizes the limiter, and a new width or interval reconciles right away
// rather than at the next periodic reconcile.
func (s *Service) followSettings(ctx context.Context) {
	var recipe string
	for {
		s.limiter.Resize(s.intSetting(ctx, WorkersSetting, 1, 1, 64))
		if current, err := s.Recipe(ctx); err == nil {
			if recipe != "" && current.String() != recipe {
				s.ReconcileSoon()
			}
			recipe = current.String()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.settingsEvery):
		}
	}
}

// idle waits for a kick or the idle poll.
func (s *Service) idle(ctx context.Context) {
	timer := time.NewTimer(idlePoll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-s.wake:
	case <-timer.C:
	}
}

// process generates job's sheets and records how it ended.
func (s *Service) process(ctx context.Context, job *Job) {
	started := s.now()
	workCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		s.heartbeat(workCtx, job, cancel)
	}()
	published, err := s.generate(workCtx, job)
	cancel(nil)
	<-heartbeatDone

	// The outcome is recorded even when the service is stopping.
	recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer recordCancel()
	log := s.logger.With("file_id", job.FileID, "duration", s.now().Sub(started).Round(time.Millisecond))
	if err == nil {
		log.InfoContext(ctx, "trickplay sheets published", "thumbnails", published.Count, "sheets", len(published.SheetBytes),
			"decoder", published.Decoder, "filled", published.Filled)
		// A reconcile run while this job held its lease skipped the row, so
		// sheets made to settings that changed meanwhile are requeued now
		// rather than at the periodic pass.
		if current, err := s.Recipe(recordCtx); err == nil && current != published.Recipe {
			s.ReconcileSoon()
		}
		return
	}
	outcome, delay := s.classify(ctx, err)
	if outcome < 0 {
		log.InfoContext(ctx, "trickplay generation abandoned", "reason", err)
		return
	}
	if _, finishErr := s.queue.Finish(recordCtx, job.FileID, job.LeaseToken, outcome, err.Error(), delay); finishErr != nil {
		log.WarnContext(ctx, "record trickplay outcome failed", "error", finishErr)
	}
	level := slog.LevelWarn
	if outcome == Released {
		level = slog.LevelInfo
	}
	log.Log(ctx, level, "trickplay generation did not publish", "outcome", outcome, "error", err)
}

// classify maps a generation error to how the row ends; a negative outcome
// leaves the row alone because this server no longer holds it.
func (s *Service) classify(ctx context.Context, err error) (Outcome, time.Duration) {
	_, unreadable := errors.AsType[*inputError](err)
	switch {
	case errors.Is(err, errLeaseLost):
		return -1, 0
	case ctx.Err() != nil:
		// Shutting down: give the file back for another server.
		return Released, 0
	case unreadable:
		return Released, releaseDelay
	case errors.Is(err, errNoNode), errors.Is(err, errSettingsUnreadable):
		return Released, time.Minute
	case mediasample.Classify(err).Permanent():
		return Unusable, 0
	}
	if failure, ok := errors.AsType[*ExtractError](err); ok && failure.Permanent {
		return Unusable, 0
	}
	return Failed, 0
}

// inputError is a job whose file this server cannot read, such as an
// offline mount: no fault of the file, so no failure is counted.
type inputError struct{ err error }

func (e *inputError) Error() string { return "input not readable here: " + e.err.Error() }
func (e *inputError) Unwrap() error { return e.err }

// errNoNode is a job that must run on a transcode node when none is
// available.
var errNoNode = errors.New("no transcode node can make trickplay sheets")

// heartbeat renews the lease until ctx ends, and cancels the work with
// errLeaseLost once the lease is gone: when a renewal is refused, or when
// renewals have failed for as long as the lease lasts.
func (s *Service) heartbeat(ctx context.Context, job *Job, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(s.heartbeatEvery)
	defer ticker.Stop()
	renewed := s.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		ok, err := s.queue.Heartbeat(ctx, job.FileID, job.LeaseToken, leaseDuration)
		switch {
		case err == nil && !ok:
			cancel(errLeaseLost)
			return
		case err == nil:
			renewed = s.now()
		case s.now().Sub(renewed) >= leaseDuration:
			cancel(errLeaseLost)
			return
		}
	}
}

// generate makes, uploads, and publishes job's sheets.
func (s *Service) generate(ctx context.Context, job *Job) (Published, error) {
	recipe, err := s.Recipe(ctx)
	if err != nil {
		return Published{}, err
	}
	track := primaryVideoTrack(job.VideoTracks)
	height := recipe.TileHeight(displayAspect(track))
	times := recipe.SampleTimes(float64(job.DurationSeconds))
	if len(times) == 0 {
		return Published{}, &mediasample.Error{Reason: mediasample.ReasonNoStream}
	}
	columns, rows := recipe.Grid()
	perSheet := columns * rows
	sheets := &mediasample.SheetsOutput{TileWidth: recipe.Width, TileHeight: height, Columns: columns, Rows: rows, Quality: Quality, UseInputAspect: true}
	if tonemap.NeedsToneMap(&models.MediaFile{HDR: job.HDR, VideoTracks: job.VideoTracks}) {
		sheets.ToneMap = &mediasample.ToneMap{AllowSoftware: true}
	}
	published := Published{Recipe: recipe, StoreIdentity: s.store.Identity(), Height: height, Count: len(times)}
	var revision int64
	chunkSheets := 1
	for start := 0; start < len(times); {
		chunk := times[start:min(len(times), start+perSheet*chunkSheets)]
		req := mediasample.Request{
			Input:         job.FilePath,
			Samples:       &mediasample.Samples{Seconds: chunk, ReadThrough: readThrough(job)},
			Sheets:        sheets,
			Threads:       runThreads,
			VideoBitDepth: mediasample.VideoBitDepthHint(track.BitDepth),
			Background:    true,
		}
		result, err := s.extractor.Extract(ctx, job, req)
		if err != nil {
			return Published{}, leaseAware(ctx, err)
		}
		actualHeight := height
		if result.SheetTileHeight > 0 {
			actualHeight = result.SheetTileHeight
		}
		if start == 0 {
			published.Height = actualHeight
		} else if actualHeight != published.Height {
			return Published{}, fmt.Errorf("trickplay chunks have different tile heights: %d and %d", published.Height, actualHeight)
		}
		chunkSheets = max(1, min(maxChunkSheets, maxChunkPixels/(recipe.Width*actualHeight*perSheet)))
		if revision == 0 {
			rev, ok, err := s.queue.BeginUpload(ctx, job.FileID, job.LeaseToken)
			if err != nil {
				return Published{}, leaseAware(ctx, err)
			}
			if !ok {
				return Published{}, errLeaseLost
			}
			revision = rev
		}
		for _, sheet := range result.Sheets {
			key := SheetKey(job.FileID, revision, start/perSheet+sheet.Index)
			if err := s.store.Put(ctx, key, sheet.JPEG); err != nil {
				return Published{}, leaseAware(ctx, fmt.Errorf("store %s: %w", key, err))
			}
			published.SheetBytes = append(published.SheetBytes, len(sheet.JPEG))
		}
		published.Filled += result.SheetFrames.Filled
		published.Decoder = result.Decoder
		start += len(chunk)
	}
	ok, err := s.queue.Publish(ctx, job.FileID, job.LeaseToken, revision, published)
	if err != nil {
		return Published{}, leaseAware(ctx, err)
	}
	if !ok {
		return Published{}, errLeaseLost
	}
	return published, nil
}

// leaseAware reports a lost lease rather than the error it caused.
func leaseAware(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), errLeaseLost) {
		return errLeaseLost
	}
	return err
}

// readThrough reports whether job's samples are read in one pass instead
// of seeking to each (see mediasample.Samples.ReadThrough). Seeking reads a
// fraction of most files and decodes a keyframe per thumbnail, but AVI
// seeks by its index poorly: on a 0.73 GB AVI fixture, seeking read 3 GB and
// took 46 s where one pass read the file once in 6 s.
func readThrough(job *Job) bool {
	return strings.EqualFold(strings.TrimSpace(job.Container), "avi")
}

// primaryVideoTrack is the first video track, the one mediasample decodes.
func primaryVideoTrack(tracks []models.VideoTrack) models.VideoTrack {
	if len(tracks) > 0 {
		return tracks[0]
	}
	return models.VideoTrack{}
}

// displayAspect is the track's display aspect ratio: the probed one, or its
// pixel dimensions when the probe gave none. Zero means unknown.
func displayAspect(track models.VideoTrack) float64 {
	if aspect, ok := ParseAspectRatio(track.AspectRatio); ok {
		return aspect
	}
	if track.Width > 0 && track.Height > 0 {
		return float64(track.Width) / float64(track.Height)
	}
	return 0
}

// errSettingsUnreadable is a recipe the settings could not be read for.
// Neither a reconcile nor a generation goes on with the default recipe
// instead: every file published to another one would be made again.
var errSettingsUnreadable = errors.New("trickplay settings could not be read")

// Recipe is the recipe the current settings describe. An unset setting takes
// its default; one that cannot be read is errSettingsUnreadable.
func (s *Service) Recipe(ctx context.Context) (Recipe, error) {
	values := map[string]int{WidthSetting: DefaultWidth, IntervalSetting: DefaultIntervalSeconds}
	for key := range values {
		if s.settings == nil {
			break
		}
		raw, err := s.settings.Get(ctx, key)
		if err != nil {
			return Recipe{}, fmt.Errorf("%w: %s: %w", errSettingsUnreadable, key, err)
		}
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			values[key] = value
		}
	}
	return NewRecipe(values[WidthSetting], values[IntervalSetting]), nil
}

// Reconcile runs one reconcile pass, unless another server is running one,
// and wakes the workers.
func (s *Service) Reconcile(ctx context.Context) (ReconcileStats, bool, error) {
	// Settings may use the same pool. Read them before reserving its session.
	recipe, err := s.Recipe(ctx)
	if err != nil {
		return ReconcileStats{}, false, err
	}
	lock, acquired, err := pglock.TryAcquire(ctx, s.pool, reconcileLock)
	if err != nil {
		return ReconcileStats{}, false, fmt.Errorf("take the trickplay reconcile lock: %w", err)
	}
	if !acquired {
		return ReconcileStats{}, false, nil
	}
	more := false
	defer func() {
		_ = lock.Release(context.WithoutCancel(ctx))
		if more && ctx.Err() == nil {
			s.ReconcileSoon()
		}
	}()
	reconcile := s.queue.Reconcile
	if repository, ok := s.queue.(*Repository); ok {
		// Keep the SQL on the lock's session, including with a pool of one
		// connection. Losing that session also stops the guarded work.
		reconcile = func(ctx context.Context, recipe Recipe, storeIdentity string, batch int) (ReconcileStats, error) {
			return repository.reconcile(ctx, lock.Conn().Exec, recipe, storeIdentity, batch)
		}
	}
	stats, more, err := s.reconcileBatches(ctx, recipe, reconcile)
	return stats, true, err
}

// reconcileBatches drains bounded steps, waking workers after each batch.
// A pass yields after reconcilePassBatches so other servers and settings
// changes can take the lock before a continuation.
func (s *Service) reconcileBatches(ctx context.Context, recipe Recipe, reconcile func(context.Context, Recipe, string, int) (ReconcileStats, error)) (ReconcileStats, bool, error) {
	var total ReconcileStats
	for range reconcilePassBatches {
		if err := ctx.Err(); err != nil {
			return total, false, err
		}
		stats, err := reconcile(ctx, recipe, s.store.Identity(), reconcileBatch)
		total.Reclaimed += stats.Reclaimed
		total.Added += stats.Added
		total.Removed += stats.Removed
		total.Stale += stats.Stale
		if err != nil {
			return total, false, err
		}
		s.Kick()
		if stats.Reclaimed < reconcileBatch && stats.Added < reconcileBatch && stats.Removed < reconcileBatch && stats.Stale < reconcileBatch {
			return total, false, nil
		}
	}
	return total, true, nil
}

// intSetting reads an integer setting, falling back to def when it is unset
// or unreadable, and clamps it.
func (s *Service) intSetting(ctx context.Context, key string, def, lo, hi int) int {
	value, err := strconv.Atoi(readSetting(ctx, s.settings, key))
	if err != nil {
		value = def
	}
	return min(max(value, lo), hi)
}
