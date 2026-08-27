package scanqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const (
	defaultPollInterval      = 2 * time.Second
	defaultHeartbeatInterval = 10 * time.Second
	defaultStaleAfter        = 2 * time.Minute
)

type folderLoader interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}

type libraryIngester interface {
	IngestFolder(ctx context.Context, folder *models.MediaFolder) (*libraryingest.Result, error)
	IngestSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*libraryingest.Result, error)
	IngestFile(ctx context.Context, folder *models.MediaFolder, filePath string) (*libraryingest.Result, error)
}

type Service struct {
	repo                   *Repository
	folders                folderLoader
	ingester               libraryIngester
	eventsHub              *evt.Hub
	appCtx                 context.Context
	maxConcurrentLibraries int
	maxConcurrentScoped    int
	pollInterval           time.Duration
	heartbeatInterval      time.Duration
	staleAfter             time.Duration
	stop                   chan struct{}
	stopOnce               sync.Once
	runningMu              sync.Mutex
	runningCancels         map[scanClaimKey]runningCancel
}

type runningCancel struct {
	libraryID int
	cancel    context.CancelFunc
}

type scanClaimKey struct {
	runID      string
	claimToken string
}

type claimLease struct {
	cancel     context.CancelCauseFunc
	staleAfter time.Duration
	deadline   atomic.Int64
	renewed    chan struct{}
	done       chan struct{}
	doneOnce   sync.Once
}

type claimLeaseContext struct {
	context.Context
	lease *claimLease
}

func newClaimLeaseContext(
	parent context.Context,
	cancel context.CancelCauseFunc,
	staleAfter time.Duration,
	renewedAt time.Time,
) (context.Context, *claimLease) {
	lease := &claimLease{
		cancel:     cancel,
		staleAfter: staleAfter,
		renewed:    make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	lease.deadline.Store(renewedAt.Add(staleAfter).UnixNano())
	context.AfterFunc(parent, lease.closeDone)
	return &claimLeaseContext{Context: parent, lease: lease}, lease
}

func (c *claimLeaseContext) Done() <-chan struct{} {
	c.lease.expireIfDue(time.Now())
	if c.Context.Err() != nil {
		c.lease.closeDone()
	}
	return c.lease.done
}

func (c *claimLeaseContext) Err() error {
	c.lease.expireIfDue(time.Now())
	if err := c.Context.Err(); err != nil {
		c.lease.closeDone()
		return err
	}
	select {
	case <-c.lease.done:
		return context.Canceled
	default:
		return nil
	}
}

func (l *claimLease) closeDone() {
	l.doneOnce.Do(func() { close(l.done) })
}

func (l *claimLease) expireIfDue(now time.Time) bool {
	for {
		deadline := l.deadline.Load()
		if deadline <= 0 {
			return true
		}
		if now.UnixNano() < deadline {
			return false
		}
		if l.deadline.CompareAndSwap(deadline, 0) {
			l.closeDone()
			l.cancel(ErrScanRunClaimLost)
			return true
		}
	}
}

func (l *claimLease) renew(succeededAt time.Time) bool {
	for {
		deadline := l.deadline.Load()
		if deadline <= 0 || succeededAt.UnixNano() >= deadline {
			l.expireIfDue(time.Now())
			return false
		}
		newDeadline := succeededAt.Add(l.staleAfter).UnixNano()
		if time.Now().UnixNano() >= newDeadline {
			l.expireIfDue(time.Now())
			return false
		}
		if !l.deadline.CompareAndSwap(deadline, newDeadline) {
			continue
		}
		select {
		case l.renewed <- struct{}{}:
		default:
		}
		return true
	}
}

func NewService(
	repo *Repository,
	folders folderLoader,
	ingester libraryIngester,
	eventsHub *evt.Hub,
	appCtx context.Context,
	maxConcurrentLibraries int,
	maxConcurrentScoped int,
) *Service {
	if maxConcurrentLibraries < 1 {
		maxConcurrentLibraries = 1
	}
	if maxConcurrentScoped < 1 {
		maxConcurrentScoped = 1
	}
	if appCtx == nil {
		appCtx = context.Background()
	}
	return &Service{
		repo:                   repo,
		folders:                folders,
		ingester:               ingester,
		eventsHub:              eventsHub,
		appCtx:                 appCtx,
		maxConcurrentLibraries: maxConcurrentLibraries,
		maxConcurrentScoped:    maxConcurrentScoped,
		pollInterval:           defaultPollInterval,
		heartbeatInterval:      defaultHeartbeatInterval,
		staleAfter:             defaultStaleAfter,
		stop:                   make(chan struct{}),
		runningCancels:         make(map[scanClaimKey]runningCancel),
	}
}

func (s *Service) Start() {
	if s == nil || s.repo == nil || s.folders == nil || s.ingester == nil {
		return
	}

	go s.maintenanceLoop()
	workerCount := s.maxConcurrentLibraries + s.maxConcurrentScoped
	for i := 0; i < workerCount; i++ {
		go s.workerLoop()
	}
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stop)
	})
}

func (s *Service) EnqueueLibraryScan(ctx context.Context, folderID int, trigger string) (bool, error) {
	return s.EnqueueScan(ctx, folderID, ModeLibrary, "", trigger)
}

func (s *Service) EnqueueScan(ctx context.Context, folderID int, mode, path, trigger string) (bool, error) {
	if s == nil || s.repo == nil {
		return false, fmt.Errorf("scan queue is not configured")
	}
	run, created, err := s.repo.Create(ctx, CreateInput{
		LibraryID: folderID,
		Mode:      mode,
		Path:      path,
		Trigger:   trigger,
	})
	if err != nil {
		return false, err
	}
	if created {
		s.publish(ctx, "scan.accepted", run)
	}
	return created, nil
}

func (s *Service) EnqueueScans(ctx context.Context, targets []scantrigger.Target) error {
	_, _, err := s.enqueueScans(ctx, targets, nil)
	return err
}

func (s *Service) EnqueueAutoscanScans(ctx context.Context, targets []scantrigger.Target, eventID int64) (int, int, error) {
	return s.enqueueScans(ctx, targets, &eventID)
}

func (s *Service) enqueueScans(ctx context.Context, targets []scantrigger.Target, autoscanEventID *int64) (int, int, error) {
	if s == nil || s.repo == nil {
		return 0, 0, fmt.Errorf("scan queue is not configured")
	}
	inputs := make([]CreateInput, 0, len(targets))
	for _, target := range targets {
		if target.Folder == nil {
			return 0, 0, fmt.Errorf("scan queue: target is missing folder")
		}
		inputs = append(inputs, CreateInput{
			LibraryID:       target.Folder.ID,
			Mode:            target.Mode,
			Path:            target.Path,
			Trigger:         target.Trigger,
			AutoscanEventID: autoscanEventID,
		})
	}
	runs, created, err := s.repo.CreateBatch(ctx, inputs)
	if err != nil {
		return 0, 0, err
	}
	createdCount := 0
	reusedCount := 0
	for i, run := range runs {
		if i < len(created) && created[i] {
			createdCount++
			s.publish(ctx, "scan.accepted", run)
		} else {
			reusedCount++
		}
	}
	return createdCount, reusedCount, nil
}

func (s *Service) CancelAcceptedByLibrary(ctx context.Context, libraryID int) (int, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	runs, err := s.repo.CancelAcceptedByLibrary(ctx, libraryID)
	if err != nil {
		return 0, err
	}
	for _, run := range runs {
		s.publish(ctx, "scan.cancelled", run)
	}
	return len(runs), nil
}

func (s *Service) CancelByLibrary(ctx context.Context, libraryID int) (int, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}

	s.runningMu.Lock()
	for _, running := range s.runningCancels {
		if running.libraryID != libraryID || running.cancel == nil {
			continue
		}
		running.cancel()
	}
	s.runningMu.Unlock()

	activeRuns, err := s.repo.CancelActiveByLibrary(ctx, libraryID)
	if err != nil {
		return 0, err
	}
	for _, run := range activeRuns {
		s.publish(ctx, "scan.cancelled", run)
	}
	return len(activeRuns), nil
}

func (s *Service) ListActive(ctx context.Context) ([]evt.ScanRun, error) {
	return s.listActive(ctx, 0)
}

func (s *Service) ListActiveSnapshot(ctx context.Context, limit int) ([]evt.ScanRun, error) {
	return s.listActive(ctx, limit)
}

func (s *Service) listActive(ctx context.Context, limit int) ([]evt.ScanRun, error) {
	if s == nil || s.repo == nil {
		return []evt.ScanRun{}, nil
	}
	var (
		rows []*models.ScanRun
		err  error
	)
	if limit > 0 {
		rows, err = s.repo.ListActiveLimit(ctx, limit)
	} else {
		rows, err = s.repo.ListActive(ctx)
	}
	if err != nil {
		return nil, err
	}
	runs := make([]evt.ScanRun, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, toEventRun(row))
	}
	return runs, nil
}

func (s *Service) maintenanceLoop() {
	s.requeueStale()

	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-s.appCtx.Done():
			return
		case <-ticker.C:
			s.requeueStale()
		}
	}
}

func (s *Service) requeueStale() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	requeued, err := s.repo.RequeueStaleRunning(ctx, time.Now().UTC().Add(-s.staleAfter))
	if err != nil {
		slog.Warn("scan queue: failed to requeue stale runs", "error", err)
		return
	}
	for _, retry := range requeued {
		s.publish(ctx, "scan.failed", retry.Retired)
		s.publish(ctx, "scan.accepted", retry.Successor)
	}
	if len(requeued) > 0 {
		slog.Info("scan queue: requeued stale runs", "count", len(requeued))
	}
}

func (s *Service) workerLoop() {
	for {
		select {
		case <-s.stop:
			return
		case <-s.appCtx.Done():
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		run, err := s.repo.ClaimNextAccepted(ctx, s.maxConcurrentLibraries, s.maxConcurrentScoped)
		cancel()
		if err != nil {
			slog.Warn("scan queue: failed to claim run", "error", err)
			s.wait()
			continue
		}
		if run == nil {
			s.wait()
			continue
		}

		s.process(run)
	}
}

func (s *Service) wait() {
	timer := time.NewTimer(s.pollInterval)
	defer timer.Stop()

	select {
	case <-s.stop:
	case <-s.appCtx.Done():
	case <-timer.C:
	}
}

func (s *Service) process(run *models.ScanRun) {
	if run == nil {
		return
	}

	ctx, cancelCause := context.WithCancelCause(s.appCtx)
	heartbeatStartedAt := time.Now()
	ctx, lease := newClaimLeaseContext(ctx, cancelCause, s.staleAfter, heartbeatStartedAt)
	cancel := func() {
		cancelCause(context.Canceled)
		lease.closeDone()
	}
	defer cancel()
	s.trackRunning(run, cancel)
	defer s.untrackRunning(run)

	touchCtx, touchCancel := context.WithTimeout(ctx, 15*time.Second)
	err := s.repo.TouchHeartbeat(touchCtx, run.ID, run.ClaimToken)
	touchCancel()
	if errors.Is(err, ErrScanRunClaimLost) {
		return
	}
	if err != nil {
		slog.WarnContext(ctx, "scan queue: failed to establish claim lease", "component", "scanqueue", "scan_id", run.ID, "error", err)
		return
	}
	if lease.expireIfDue(time.Now()) || ctx.Err() != nil {
		return
	}
	ctx = libraryingest.WithLeaseGuard(ctx, func() error {
		if lease.expireIfDue(time.Now()) {
			return ErrScanRunClaimLost
		}
		return nil
	})
	var afterCommitMu sync.Mutex
	afterCommit := make([]func(context.Context), 0, 1)
	ctx = libraryingest.WithAfterCommitRegistrar(ctx, func(callback func(context.Context)) {
		afterCommitMu.Lock()
		defer afterCommitMu.Unlock()
		afterCommit = append(afterCommit, callback)
	})
	progressReporter := newScanProgressReporter(s, run, ctx, cancelCause)
	ctx = libraryingest.WithProgressReporter(ctx, progressReporter.Report)
	s.publish(context.Background(), "scan.started", run)

	heartbeatStop := make(chan struct{})
	go s.claimLeaseWatchdog(ctx, run, lease)
	go s.heartbeatLoop(ctx, run, cancelCause, heartbeatStop, lease)
	defer close(heartbeatStop)

	folder, err := s.folders.GetByID(ctx, run.MediaFolderID)
	switch {
	case errors.Is(context.Cause(ctx), ErrScanRunClaimLost):
		return
	case errors.Is(err, catalog.ErrFolderNotFound):
		s.cancelRun(run)
		return
	case err != nil:
		s.failRun(run, fmt.Errorf("load library for scan: %w", err))
		return
	case folder == nil || !folder.Enabled:
		s.cancelRun(run)
		return
	}

	var result *libraryingest.Result
	switch run.Mode {
	case ModeLibrary:
		result, err = s.ingester.IngestFolder(ctx, folder)
	case ModeSubtree:
		result, err = s.ingester.IngestSubtree(ctx, folder, run.Path)
	case ModeFile:
		result, err = s.ingester.IngestFile(ctx, folder, run.Path)
	default:
		err = fmt.Errorf("unsupported scan mode %q", run.Mode)
	}
	if s.finishRun(ctx, lease, run, result, err) {
		afterCommitMu.Lock()
		callbacks := slices.Clone(afterCommit)
		afterCommitMu.Unlock()
		for _, callback := range callbacks {
			go callback(context.Background())
		}
	}
}

func (s *Service) finishRun(
	ctx context.Context,
	lease *claimLease,
	run *models.ScanRun,
	result *libraryingest.Result,
	runErr error,
) bool {
	lease.expireIfDue(time.Now())
	switch {
	case errors.Is(context.Cause(ctx), ErrScanRunClaimLost):
		return false
	case errors.Is(runErr, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		s.cancelRun(run)
		return false
	case runErr != nil:
		s.failRun(run, runErr)
		return false
	default:
		return s.completeRun(run, result)
	}
}

type scanProgressReporter struct {
	service *Service
	run     *models.ScanRun
	ctx     context.Context
	cancel  context.CancelCauseFunc
	mu      sync.Mutex
	last    evt.ScanRunResult
}

func newScanProgressReporter(service *Service, run *models.ScanRun, ctx context.Context, cancel context.CancelCauseFunc) *scanProgressReporter {
	return &scanProgressReporter{
		service: service,
		run:     run,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (r *scanProgressReporter) Report(update libraryingest.ProgressUpdate) {
	if r == nil || r.service == nil || r.service.repo == nil || r.run == nil {
		return
	}
	if context.Cause(r.ctx) != nil {
		return
	}

	r.mu.Lock()
	r.last.New = update.New
	r.last.Updated = update.Updated
	r.last.Unchanged = update.Unchanged
	r.last.Errors = update.Errors
	r.last.MatchedFiles = update.MatchedFiles
	r.last.RetriedItems = update.RetriedItems
	r.last.Phase = update.Phase
	r.last.Message = update.Message
	r.last.CurrentScope = update.CurrentScope
	r.last.TotalFiles = update.TotalFiles
	r.last.FilesDiscovered = update.FilesDiscovered
	r.last.FilesProcessed = update.FilesProcessed
	current := r.last
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	run, err := r.service.repo.UpdateProgress(ctx, r.run.ID, r.run.ClaimToken, &current)
	cancel()
	if errors.Is(err, ErrScanRunClaimLost) {
		r.cancel(ErrScanRunClaimLost)
		return
	}
	if err != nil {
		slog.Warn("scan queue: failed to persist scan progress", "scan_id", r.run.ID, "error", err)
		return
	}
	if run != nil {
		slog.Info("scan queue: progress",
			"scan_id", run.ID,
			"library_id", run.MediaFolderID,
			"phase", current.Phase,
			"message", current.Message,
			"processed_files", current.FilesProcessed,
			"total_files", current.TotalFiles,
			"matched_files", current.MatchedFiles,
			"retried_items", current.RetriedItems,
		)
		r.service.publish(context.Background(), "scan.progress", run)
	}
}

func (s *Service) trackRunning(run *models.ScanRun, cancel context.CancelFunc) {
	if s == nil || run == nil || cancel == nil {
		return
	}
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	s.runningCancels[scanClaimKey{runID: run.ID, claimToken: run.ClaimToken}] = runningCancel{
		libraryID: run.MediaFolderID,
		cancel:    cancel,
	}
}

func (s *Service) untrackRunning(run *models.ScanRun) {
	if s == nil || run == nil {
		return
	}
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	delete(s.runningCancels, scanClaimKey{runID: run.ID, claimToken: run.ClaimToken})
}

func (s *Service) claimLeaseWatchdog(
	ctx context.Context,
	run *models.ScanRun,
	lease *claimLease,
) {
	timer := time.NewTimer(time.Until(time.Unix(0, lease.deadline.Load())))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-lease.renewed:
		case <-timer.C:
		}

		if lease.expireIfDue(time.Now()) {
			slog.InfoContext(ctx, "scan queue: local claim lease expired; canceling ingestion", "component", "scanqueue", "scan_id", run.ID)
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(time.Until(time.Unix(0, lease.deadline.Load())))
	}
}

func (s *Service) heartbeatLoop(
	ctx context.Context,
	run *models.ScanRun,
	cancelClaim context.CancelCauseFunc,
	stop <-chan struct{},
	lease *claimLease,
) {
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatStartedAt := time.Now()
			touchCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := s.repo.TouchHeartbeat(touchCtx, run.ID, run.ClaimToken)
			cancel()
			if errors.Is(err, ErrScanRunClaimLost) {
				slog.InfoContext(ctx, "scan queue: claim lost; canceling ingestion", "component", "scanqueue", "scan_id", run.ID)
				cancelClaim(ErrScanRunClaimLost)
				return
			}
			if err != nil {
				slog.WarnContext(ctx, "scan queue: failed to touch heartbeat", "component", "scanqueue", "scan_id", run.ID, "error", err)
				continue
			}
			if !lease.renew(heartbeatStartedAt) {
				slog.InfoContext(ctx, "scan queue: heartbeat arrived after local claim lease expiry", "component", "scanqueue", "scan_id", run.ID)
				cancelClaim(ErrScanRunClaimLost)
				return
			}
		}
	}
}

func (s *Service) cancelRun(claim *models.ScanRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run, err := s.repo.CancelClaim(ctx, claim.ID, claim.ClaimToken)
	if errors.Is(err, ErrScanRunClaimLost) {
		return
	}
	if err != nil {
		slog.Warn("scan queue: failed to mark canceled", "scan_id", claim.ID, "error", err)
		return
	}
	s.publish(context.Background(), "scan.cancelled", run)
}

func (s *Service) failRun(claim *models.ScanRun, runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run, err := s.repo.Fail(ctx, claim.ID, claim.ClaimToken, errString(runErr))
	if errors.Is(err, ErrScanRunClaimLost) {
		return
	}
	if err != nil {
		slog.Warn("scan queue: failed to mark failed", "scan_id", claim.ID, "error", err)
		return
	}
	s.publish(context.Background(), "scan.failed", run)
}

func (s *Service) completeRun(claim *models.ScanRun, result *libraryingest.Result) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run, err := s.repo.Complete(ctx, claim.ID, claim.ClaimToken, scanResultFromIngest(result))
	if errors.Is(err, ErrScanRunClaimLost) {
		return false
	}
	if err != nil {
		slog.Warn("scan queue: failed to mark completed", "scan_id", claim.ID, "error", err)
		return false
	}
	s.publish(context.Background(), "scan.completed", run)
	return true
}

func (s *Service) publish(ctx context.Context, eventName string, run *models.ScanRun) {
	if s == nil || s.eventsHub == nil || run == nil {
		return
	}
	_ = s.eventsHub.PublishJSON(ctx, evt.ChannelScans, eventName, toEventRun(run), evt.PublishOptions{
		AdminOnly: true,
	})
}

func toEventRun(run *models.ScanRun) evt.ScanRun {
	if run == nil {
		return evt.ScanRun{}
	}
	out := evt.ScanRun{
		ID:           run.ID,
		LibraryID:    run.MediaFolderID,
		Mode:         run.Mode,
		Path:         run.Path,
		Trigger:      run.Trigger,
		Status:       run.Status,
		StartedAt:    run.StartedAt,
		CompletedAt:  run.CompletedAt,
		ErrorMessage: run.ErrorMessage,
	}
	if len(run.ResultPayload) > 0 && string(run.ResultPayload) != "{}" {
		var result evt.ScanRunResult
		if err := json.Unmarshal(run.ResultPayload, &result); err == nil {
			out.Result = &result
		}
	}
	return out
}

func scanResultFromIngest(result *libraryingest.Result) *evt.ScanRunResult {
	if result == nil {
		return nil
	}
	resp := &evt.ScanRunResult{
		MatchedFiles:           result.MatchedFiles,
		RetriedItems:           result.RetriedItems,
		StillUnmatchedWarnings: result.StillUnmatchedWarnings,
	}
	if result.Skipped {
		resp.Skipped = 1
	}
	if result.ScanResult != nil {
		resp.New = result.ScanResult.New
		resp.Updated = result.ScanResult.Updated
		resp.Unchanged = result.ScanResult.Unchanged
		resp.Missing = result.ScanResult.Missing
		resp.MissingSkippedProtected = result.ScanResult.MissingSkippedProtected
		resp.FilesDeleted = result.ScanResult.FilesDeleted
		resp.MembershipsRemoved = result.ScanResult.MembershipsRemoved
		resp.ItemsDeleted = result.ScanResult.ItemsDeleted
		resp.Errors = result.ScanResult.Errors
	}
	return resp
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
