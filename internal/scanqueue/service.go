package scanqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	runningCancels         map[string]runningCancel
}

type runningCancel struct {
	libraryID int
	cancel    context.CancelFunc
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
		runningCancels:         make(map[string]runningCancel),
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
	if s == nil || s.repo == nil {
		return fmt.Errorf("scan queue is not configured")
	}
	inputs := make([]CreateInput, 0, len(targets))
	for _, target := range targets {
		if target.Folder == nil {
			return fmt.Errorf("scan queue: target is missing folder")
		}
		inputs = append(inputs, CreateInput{
			LibraryID: target.Folder.ID,
			Mode:      target.Mode,
			Path:      target.Path,
			Trigger:   target.Trigger,
		})
	}
	runs, created, err := s.repo.CreateBatch(ctx, inputs)
	if err != nil {
		return err
	}
	for i, run := range runs {
		if i < len(created) && created[i] {
			s.publish(ctx, "scan.accepted", run)
		}
	}
	return nil
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

	cancelled, err := s.CancelAcceptedByLibrary(ctx, libraryID)
	if err != nil {
		return 0, err
	}

	s.runningMu.Lock()
	for _, running := range s.runningCancels {
		if running.libraryID != libraryID || running.cancel == nil {
			continue
		}
		running.cancel()
	}
	s.runningMu.Unlock()

	activeRuns, err := s.repo.ListActive(ctx)
	if err != nil {
		return cancelled, err
	}
	for _, run := range activeRuns {
		if run == nil || run.MediaFolderID != libraryID {
			continue
		}
		_, changed, err := s.repo.MarkCancelled(ctx, run.ID)
		if err != nil {
			return cancelled, err
		}
		if changed {
			cancelled++
		}
	}
	return cancelled, nil
}

func (s *Service) ListActive(ctx context.Context) ([]evt.ScanRun, error) {
	if s == nil || s.repo == nil {
		return []evt.ScanRun{}, nil
	}
	rows, err := s.repo.ListActive(ctx)
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
	if requeued > 0 {
		slog.Info("scan queue: requeued stale runs", "count", requeued)
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

		s.publish(context.Background(), "scan.started", run)
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

	ctx, cancel := context.WithCancel(s.appCtx)
	defer cancel()
	s.trackRunning(run.ID, run.MediaFolderID, cancel)
	defer s.untrackRunning(run.ID)
	progressReporter := newScanProgressReporter(s, run)
	ctx = libraryingest.WithProgressReporter(ctx, progressReporter.Report)

	heartbeatStop := make(chan struct{})
	go s.heartbeatLoop(ctx, run.ID, heartbeatStop)
	defer close(heartbeatStop)

	folder, err := s.folders.GetByID(ctx, run.MediaFolderID)
	switch {
	case errors.Is(err, catalog.ErrFolderNotFound):
		s.cancelRun(run.ID)
		return
	case err != nil:
		s.failRun(run.ID, fmt.Errorf("load library for scan: %w", err))
		return
	case folder == nil || !folder.Enabled:
		s.cancelRun(run.ID)
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
	switch {
	case errors.Is(err, context.Canceled):
		s.cancelRun(run.ID)
	case err != nil:
		s.failRun(run.ID, err)
	default:
		s.completeRun(run.ID, result)
	}
}

type scanProgressReporter struct {
	service *Service
	run     *models.ScanRun
	mu      sync.Mutex
	last    evt.ScanRunResult
}

func newScanProgressReporter(service *Service, run *models.ScanRun) *scanProgressReporter {
	return &scanProgressReporter{
		service: service,
		run:     run,
	}
}

func (r *scanProgressReporter) Report(update libraryingest.ProgressUpdate) {
	if r == nil || r.service == nil || r.service.repo == nil || r.run == nil {
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
	run, err := r.service.repo.UpdateProgress(ctx, r.run.ID, &current)
	cancel()
	if err != nil && !errors.Is(err, ErrScanRunNotFound) {
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

func (s *Service) trackRunning(runID string, libraryID int, cancel context.CancelFunc) {
	if s == nil || cancel == nil {
		return
	}
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	s.runningCancels[runID] = runningCancel{
		libraryID: libraryID,
		cancel:    cancel,
	}
}

func (s *Service) untrackRunning(runID string) {
	if s == nil {
		return
	}
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	delete(s.runningCancels, runID)
}

func (s *Service) heartbeatLoop(ctx context.Context, runID string, stop <-chan struct{}) {
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			touchCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := s.repo.TouchHeartbeat(touchCtx, runID)
			cancel()
			if err != nil && !errors.Is(err, ErrScanRunNotFound) {
				slog.Warn("scan queue: failed to touch heartbeat", "scan_id", runID, "error", err)
			}
		}
	}
}

func (s *Service) cancelRun(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run, changed, err := s.repo.MarkCancelled(ctx, runID)
	if err != nil {
		slog.Warn("scan queue: failed to mark cancelled", "scan_id", runID, "error", err)
		return
	}
	if changed {
		s.publish(context.Background(), "scan.cancelled", run)
	}
}

func (s *Service) failRun(runID string, runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run, err := s.repo.Fail(ctx, runID, errString(runErr))
	if err != nil {
		slog.Warn("scan queue: failed to mark failed", "scan_id", runID, "error", err)
		return
	}
	s.publish(context.Background(), "scan.failed", run)
}

func (s *Service) completeRun(runID string, result *libraryingest.Result) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run, err := s.repo.Complete(ctx, runID, scanResultFromIngest(result))
	if err != nil {
		slog.Warn("scan queue: failed to mark completed", "scan_id", runID, "error", err)
		return
	}
	s.publish(context.Background(), "scan.completed", run)
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
