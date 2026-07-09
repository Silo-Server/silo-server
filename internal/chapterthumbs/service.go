package chapterthumbs

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

const (
	defaultWorkerCount         = 1
	defaultPriorityWorkerCount = 1
	defaultQueueSize           = 128
	defaultBatchLimit          = 25
	defaultPriorityBatchSize   = 3
	defaultNormalBatchSize     = 8
	hwExtractTimeoutSDR        = 8 * time.Second
	hwExtractTimeoutHDR        = 20 * time.Second
	cpuExtractTimeoutSDR       = 10 * time.Second
	cpuExtractTimeoutHDR       = 25 * time.Second

	chapterThumbnailHDRPolicySetting    = "playback.chapter_thumbnail_hdr_policy"
	chapterThumbnailHDRPolicyDefault    = "best_effort"
	chapterThumbnailHDRPolicyDisabled   = "disabled"
	chapterThumbnailHDRPolicyBestEffort = "best_effort"
)

var chapterThumbnailRetrySchedule = []time.Duration{
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

type FileRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
	ListMissingChapterThumbnails(ctx context.Context, limit int) ([]*models.MediaFile, error)
	UpdateChapterThumbnailState(
		ctx context.Context,
		fileID int,
		chapters []models.MediaChapter,
		fileFailure *scanner.ChapterThumbnailFailureState,
	) (*models.MediaFile, error)
	SetChapterThumbnailFailure(
		ctx context.Context,
		fileID int,
		retryAfter time.Time,
		failureCount int,
		lastError string,
	) error
}

type FolderRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}

type ProbeEnsurer interface {
	Ensure(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error)
}

type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type ObjectStore interface {
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	Bucket() string
}

type ThumbnailNotifier interface {
	ChapterThumbnailReady(ctx context.Context, fileID int, chapterIndex int, thumbnailPath string, thumbnailThumbhash string)
}

type ChapterThumbnailRequest struct {
	FileID        int
	TargetSeconds *float64
}

type Service struct {
	fileRepo         FileRepository
	folderRepo       FolderRepository
	probeEnsurer     ProbeEnsurer
	settings         SettingsReader
	store            ObjectStore
	notifier         ThumbnailNotifier
	ffmpegPath       string
	hwAccel          string
	hwDevice         string
	hwResolveOnce    sync.Once
	resolvedHWAccel  string
	resolvedHWDevice string

	notifyNormal        chan struct{}
	notifyPriority      chan struct{}
	workerCount         int
	priorityWorkerCount int
	priorityBatchSize   int
	normalBatchSize     int

	mu             sync.Mutex
	priorityQueue  []int
	normalQueue    []int
	queuedPriority map[int]ChapterThumbnailRequest
	queuedNormal   map[int]ChapterThumbnailRequest
	inProgress     map[int]struct{}

	transcodePool      *nodepool.TranscodePool
	remoteMu           sync.Mutex
	remoteReservations map[string]int
	remoteExtractor    remoteFrameExtractor

	extractFrameFunc           func(ctx context.Context, file *models.MediaFile, seekSeconds float64, hdrPolicy string) ([]byte, string, error)
	uploadChapterThumbnailFunc func(ctx context.Context, fileID, chapterIndex int, frame []byte) (string, string, error)
	runFFmpegFrameExtractFunc  func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error)
	clock                      func() time.Time
}

type chapterCandidate struct {
	offset   int
	chapter  models.MediaChapter
	distance float64
}

type generatedChapter struct {
	offset    int
	fileID    int
	chapterID int
	path      string
	thumbhash string
}

func (s *Service) SetNotifier(notifier ThumbnailNotifier) {
	if s == nil {
		return
	}
	s.notifier = notifier
}

func NewService(
	fileRepo FileRepository,
	folderRepo FolderRepository,
	probeEnsurer ProbeEnsurer,
	settings SettingsReader,
	store ObjectStore,
	notifier ThumbnailNotifier,
	transcodePool *nodepool.TranscodePool,
	ffmpegPath string,
	hwAccel string,
	hwDevice string,
	workerCount int,
) *Service {
	if fileRepo == nil || folderRepo == nil || store == nil {
		return nil
	}

	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if workerCount <= 0 {
		workerCount = defaultWorkerCount
	}

	return &Service{
		fileRepo:            fileRepo,
		folderRepo:          folderRepo,
		probeEnsurer:        probeEnsurer,
		settings:            settings,
		store:               store,
		notifier:            notifier,
		ffmpegPath:          ffmpegPath,
		hwAccel:             hwAccel,
		hwDevice:            hwDevice,
		notifyNormal:        make(chan struct{}, defaultQueueSize),
		notifyPriority:      make(chan struct{}, defaultQueueSize),
		workerCount:         workerCount,
		priorityWorkerCount: defaultPriorityWorkerCount,
		priorityBatchSize:   defaultPriorityBatchSize,
		normalBatchSize:     defaultNormalBatchSize,
		queuedPriority:      make(map[int]ChapterThumbnailRequest),
		queuedNormal:        make(map[int]ChapterThumbnailRequest),
		inProgress:          make(map[int]struct{}),
		transcodePool:       transcodePool,
		remoteReservations:  make(map[string]int),
		remoteExtractor:     &httpRemoteFrameExtractor{},
		clock:               time.Now,
	}
}

func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}

	resolvedAccel, resolvedDevice := s.resolveHWConfig()
	slog.InfoContext(ctx,
		"chapter thumbnail service started", "component", "chapterthumbs",
		"workers",
		s.workerCount,
		"priority_workers",
		s.priorityWorkerCount,
		"hw_accel",
		resolvedAccel,
		"hw_device",
		resolvedDevice,
	)

	for i := 0; i < s.workerCount; i++ {
		go s.worker(ctx, false)
	}
	for i := 0; i < s.priorityWorkerCount; i++ {
		go s.worker(ctx, true)
	}
}

func (s *Service) QueueFileIDs(_ context.Context, fileIDs []int) {
	if s == nil {
		return
	}
	for _, fileID := range fileIDs {
		if s.enqueue(ChapterThumbnailRequest{FileID: fileID}, false) {
			s.notifyNormalWorker()
		}
	}
}

func (s *Service) QueuePriorityFileIDs(_ context.Context, fileIDs []int) {
	if s == nil {
		return
	}
	for _, fileID := range fileIDs {
		if s.enqueue(ChapterThumbnailRequest{FileID: fileID}, true) {
			s.notifyPriorityWorker()
			s.notifyNormalWorker()
		}
	}
}

func (s *Service) QueuePriorityFileAtPosition(_ context.Context, fileID int, targetSeconds float64) {
	if s == nil {
		return
	}
	target := targetSeconds
	if s.enqueue(ChapterThumbnailRequest{FileID: fileID, TargetSeconds: &target}, true) {
		s.notifyPriorityWorker()
		s.notifyNormalWorker()
	}
}

func (s *Service) BackfillMissing(ctx context.Context, limit int) (int, error) {
	if s == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = defaultBatchLimit
	}

	files, err := s.fileRepo.ListMissingChapterThumbnails(ctx, limit)
	if err != nil {
		return 0, err
	}

	processed := 0
	for _, file := range files {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		if file == nil {
			continue
		}
		if _, err := s.processRequest(ctx, ChapterThumbnailRequest{FileID: file.ID}, false); err != nil {
			slog.WarnContext(ctx, "chapter thumbnail backfill failed", "component", "chapterthumbs", "file_id", file.ID, "error", err)
			continue
		}
		processed++
	}
	return processed, nil
}

func (s *Service) worker(ctx context.Context, priorityOnly bool) {
	for {
		req, ok := s.nextRequest(ctx, priorityOnly)
		if !ok {
			return
		}
		requeueNormal, err := s.processRequest(ctx, req, priorityOnly)
		if err != nil {
			slog.WarnContext(ctx, "chapter thumbnail generation failed", "component", "chapterthumbs", "file_id", req.FileID, "error", err)
		}
		notifyPriority, notifyNormal := s.finishProcessing(req.FileID)
		if notifyPriority {
			s.notifyPriorityWorker()
		}
		if notifyNormal {
			s.notifyNormalWorker()
		}
		if requeueNormal && s.enqueue(ChapterThumbnailRequest{FileID: req.FileID}, false) {
			s.notifyNormalWorker()
		}
	}
}

func (s *Service) processRequest(ctx context.Context, req ChapterThumbnailRequest, priority bool) (bool, error) {
	file, err := s.fileRepo.GetByID(ctx, req.FileID)
	if err != nil || file == nil {
		if err == nil {
			slog.InfoContext(ctx, "chapter thumbnail request skipped", "component", "chapterthumbs", "file_id", req.FileID, "priority", priority, "reason", "file_not_found")
		}
		return false, err
	}

	folder, err := s.folderRepo.GetByID(ctx, file.MediaFolderID)
	if err != nil || folder == nil {
		if err == nil {
			slog.InfoContext(ctx, "chapter thumbnail request skipped", "component", "chapterthumbs", "file_id", req.FileID, "priority", priority, "reason", "folder_not_found")
		}
		return false, err
	}
	if !folder.Enabled || !folder.ChapterThumbnailsEnabled {
		slog.InfoContext(ctx,
			"chapter thumbnail request skipped", "component", "chapterthumbs",
			"file_id",
			req.FileID,
			"priority",
			priority,
			"reason",
			"folder_disabled",
			"folder_id",
			folder.ID,
		)
		return false, nil
	}

	now := s.now()
	if file.ChapterThumbnailRetryAfter != nil && file.ChapterThumbnailRetryAfter.After(now) {
		slog.InfoContext(ctx,
			"chapter thumbnail request skipped", "component", "chapterthumbs",
			"file_id",
			req.FileID,
			"priority",
			priority,
			"reason",
			"file_cooldown",
			"retry_after",
			file.ChapterThumbnailRetryAfter,
		)
		return false, nil
	}

	file, err = s.ensureChapters(ctx, file, now)
	if err != nil || file == nil {
		return false, err
	}
	if len(file.Chapters) == 0 {
		slog.InfoContext(ctx, "chapter thumbnail request skipped", "component", "chapterthumbs", "file_id", req.FileID, "priority", priority, "reason", "no_chapters")
		return false, nil
	}

	hdrPolicy := s.chapterThumbnailHDRPolicy(ctx)
	if needsTonemap(file) && hdrPolicy == chapterThumbnailHDRPolicyDisabled {
		slog.InfoContext(ctx,
			"chapter thumbnail request skipped", "component", "chapterthumbs",
			"file_id",
			req.FileID,
			"priority",
			priority,
			"reason",
			"hdr_policy_disabled",
		)
		return false, nil
	}

	selected := selectChapterCandidates(file.Chapters, req.TargetSeconds, priority, s.batchSize(priority), now)
	if len(selected) == 0 {
		slog.InfoContext(ctx,
			"chapter thumbnail request skipped", "component", "chapterthumbs",
			"file_id",
			req.FileID,
			"priority",
			priority,
			"reason",
			"no_eligible_chapters",
		)
		return false, nil
	}

	slog.InfoContext(ctx,
		"chapter thumbnail processing started", "component", "chapterthumbs",
		"file_id",
		req.FileID,
		"priority",
		priority,
		"target_seconds",
		requestTargetSeconds(req),
		"selected_count",
		len(selected),
		"chapter_count",
		len(file.Chapters),
		"hdr_policy",
		hdrPolicy,
	)

	updated := *file
	updated.Chapters = append([]models.MediaChapter(nil), file.Chapters...)
	generated := make([]generatedChapter, 0, len(selected))
	mutated := false
	failed := 0
	var hardFileFailure *scanner.ChapterThumbnailFailureState

	for _, candidate := range selected {
		chapter := updated.Chapters[candidate.offset]
		frame, reason, err := s.extractFrame(ctx, &updated, chapterCaptureTime(chapter), hdrPolicy)
		if err != nil {
			slog.WarnContext(ctx,
				"chapter thumbnail extract failed", "component", "chapterthumbs",
				"file_id",
				updated.ID,
				"chapter_index",
				chapter.Index,
				"reason",
				reason,
				"error",
				err,
			)
			recordChapterFailure(&updated.Chapters[candidate.offset], now, reason, err)
			mutated = true
			failed++
			if shouldApplyFileFailure(reason) {
				hardFileFailure = buildPersistentFileFailure(&updated, now, reason, err)
				slog.WarnContext(ctx,
					"chapter thumbnail file marked failed", "component", "chapterthumbs",
					"file_id",
					updated.ID,
					"reason",
					reason,
					"retry_after",
					hardFileFailure.RetryAfter,
				)
				break
			}
			continue
		}

		path, thumbhash, err := s.uploadChapterThumbnail(ctx, updated.ID, chapter.Index, frame)
		if err != nil {
			slog.WarnContext(ctx,
				"chapter thumbnail upload failed", "component", "chapterthumbs",
				"file_id",
				updated.ID,
				"chapter_index",
				chapter.Index,
				"reason",
				"chapter_extract_failed",
				"error",
				err,
			)
			recordChapterFailure(&updated.Chapters[candidate.offset], now, "chapter_extract_failed", err)
			mutated = true
			failed++
			continue
		}

		applyChapterSuccess(&updated.Chapters[candidate.offset], path, thumbhash)
		generated = append(generated, generatedChapter{
			offset:    candidate.offset,
			fileID:    updated.ID,
			chapterID: updated.Chapters[candidate.offset].Index,
			path:      path,
			thumbhash: thumbhash,
		})
		mutated = true
	}

	if mutated {
		persistFailure := hardFileFailure
		if persistFailure == nil {
			persistFailure = &scanner.ChapterThumbnailFailureState{
				Apply:        true,
				FailureCount: 0,
			}
		}
		persisted, err := s.fileRepo.UpdateChapterThumbnailState(
			ctx,
			updated.ID,
			updated.Chapters,
			persistFailure,
		)
		if err != nil {
			return false, err
		}
		if persisted != nil {
			updated = *persisted
			updated.Chapters = append([]models.MediaChapter(nil), persisted.Chapters...)
		}
		for _, ready := range generated {
			if s.notifier == nil || ready.offset < 0 || ready.offset >= len(updated.Chapters) {
				continue
			}
			chapter := updated.Chapters[ready.offset]
			s.notifier.ChapterThumbnailReady(
				ctx,
				updated.ID,
				chapter.Index,
				chapter.ThumbnailPath,
				chapter.ThumbnailThumbhash,
			)
		}
	}

	requeue := hardFileFailure == nil && hasEligibleMissingChapter(updated.Chapters, now)
	slog.InfoContext(ctx,
		"chapter thumbnail processing finished", "component", "chapterthumbs",
		"file_id",
		req.FileID,
		"priority",
		priority,
		"generated_count",
		len(generated),
		"failed_count",
		failed,
		"requeue",
		requeue,
	)

	return requeue, nil
}

func (s *Service) ensureChapters(ctx context.Context, file *models.MediaFile, now time.Time) (*models.MediaFile, error) {
	if file == nil || file.Chapters != nil || s.probeEnsurer == nil {
		return file, nil
	}

	ensured, err := s.probeEnsurer.Ensure(ctx, file)
	if err == nil && ensured != nil {
		return ensured, nil
	}

	reason := classifyProbeError(err)
	slog.WarnContext(ctx, "chapter thumbnail probe failed", "component", "chapterthumbs", "file_id", file.ID, "reason", reason, "error", err)
	if applyErr := s.applyFileFailure(ctx, file, now, reason, err); applyErr != nil {
		return file, applyErr
	}
	return file, err
}

func (s *Service) applyFileFailure(
	ctx context.Context,
	file *models.MediaFile,
	now time.Time,
	reason string,
	err error,
) error {
	if s == nil || s.fileRepo == nil || file == nil {
		return err
	}
	nextCount := file.ChapterThumbnailFailureCount + 1
	retryAfter := now.Add(retryDurationForCount(nextCount))
	if updateErr := s.fileRepo.SetChapterThumbnailFailure(
		ctx,
		file.ID,
		retryAfter,
		nextCount,
		failureDetail(reason, err),
	); updateErr != nil {
		return updateErr
	}
	return err
}

func (s *Service) extractFrame(
	ctx context.Context,
	file *models.MediaFile,
	seekSeconds float64,
	hdrPolicy string,
) ([]byte, string, error) {
	if s.extractFrameFunc != nil {
		return s.extractFrameFunc(ctx, file, seekSeconds, hdrPolicy)
	}
	toneMap := needsTonemap(file) && hdrPolicy == chapterThumbnailHDRPolicyBestEffort
	mode := s.chapterThumbnailExecutionMode(ctx)
	if mode == chapterThumbnailExecutionLocal {
		return s.extractFrameLocal(ctx, file.FilePath, seekSeconds, toneMap)
	}

	node, release, nodeReason := s.reserveRemoteNode(ctx)
	if node == nil {
		if mode == chapterThumbnailExecutionPreferTranscode {
			slog.InfoContext(ctx,
				"chapter thumbnail remote execution unavailable; falling back to local", "component", "chapterthumbs",
				"file_path",
				file.FilePath,
				"reason",
				nodeReason,
			)
			return s.extractFrameLocal(ctx, file.FilePath, seekSeconds, toneMap)
		}
		return nil, nodeReason, wrapReason(nodeReason, fmt.Errorf("no transcode node available for chapter thumbnail extraction"))
	}
	defer release()

	jwtSecret := s.chapterThumbnailJWTSecret(ctx)
	data, reason, err := s.remoteExtractor.ExtractFrame(ctx, node, jwtSecret, RemoteExtractRequest{
		InputPath:   file.FilePath,
		SeekSeconds: seekSeconds,
		ToneMap:     toneMap,
	})
	if err == nil {
		return data, "", nil
	}

	if mode == chapterThumbnailExecutionPreferTranscode && isInfrastructureRemoteFailure(reason) {
		slog.WarnContext(ctx,
			"chapter thumbnail remote extraction failed; falling back to local", "component", "chapterthumbs",
			"file_path",
			file.FilePath,
			"node",
			node.URL,
			"reason",
			reason,
			"error",
			err,
		)
		return s.extractFrameLocal(ctx, file.FilePath, seekSeconds, toneMap)
	}

	return nil, reason, err
}

func (s *Service) extractFrameLocal(ctx context.Context, inputPath string, seekSeconds float64, toneMap bool) ([]byte, string, error) {
	resolvedAccel, resolvedDevice := s.resolveHWConfig()
	return ExtractFrame(ctx, FrameExtractOptions{
		InputPath:   inputPath,
		SeekSeconds: seekSeconds,
		FFmpegPath:  s.ffmpegPath,
		HWAccel:     resolvedAccel,
		HWDevice:    resolvedDevice,
		ToneMap:     toneMap,
		RunFunc:     s.runFFmpegFrameExtractFunc,
	})
}

func (s *Service) resolveHWConfig() (string, string) {
	s.hwResolveOnce.Do(func() {
		s.resolvedHWAccel = playback.ResolveHWAccelWithFFmpeg(s.hwAccel, s.ffmpegPath)
		s.resolvedHWDevice = s.hwDevice
		if s.resolvedHWDevice == "" && (s.resolvedHWAccel == "qsv" || s.resolvedHWAccel == "vaapi") {
			s.resolvedHWDevice = playback.PickRenderDevice("")
		}
	})
	return s.resolvedHWAccel, s.resolvedHWDevice
}

func (s *Service) chapterThumbnailExecutionMode(ctx context.Context) string {
	if s == nil || s.settings == nil {
		return chapterThumbnailExecutionLocal
	}
	value, err := s.settings.Get(ctx, chapterThumbnailExecutionSetting)
	if err != nil {
		return chapterThumbnailExecutionLocal
	}
	switch strings.TrimSpace(strings.ToLower(value)) {
	case chapterThumbnailExecutionPreferTranscode:
		return chapterThumbnailExecutionPreferTranscode
	case chapterThumbnailExecutionTranscodeOnly:
		return chapterThumbnailExecutionTranscodeOnly
	default:
		return chapterThumbnailExecutionLocal
	}
}

func (s *Service) chapterThumbnailNodeCapacity(ctx context.Context) int {
	if s == nil || s.settings == nil {
		return 1
	}
	value, err := s.settings.Get(ctx, chapterThumbnailNodeCapacitySetting)
	if err != nil {
		return 1
	}
	capacity, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || capacity <= 0 {
		return 1
	}
	return capacity
}

func (s *Service) chapterThumbnailJWTSecret(ctx context.Context) string {
	if s == nil || s.settings == nil {
		return ""
	}
	value, err := s.settings.Get(ctx, authJWTSecretSetting)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *Service) reserveRemoteNode(ctx context.Context) (*nodepool.Node, func(), string) {
	if s == nil || s.transcodePool == nil || s.remoteExtractor == nil {
		return nil, func() {}, chapterThumbnailNodeUnavailableReason
	}
	if s.chapterThumbnailJWTSecret(ctx) == "" {
		return nil, func() {}, chapterThumbnailNodeUnavailableReason
	}

	nodes := s.transcodePool.Nodes()
	capacity := s.chapterThumbnailNodeCapacity(ctx)

	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()

	var best *nodepool.Node
	bestLoad := 0
	hadHealthyNode := false
	for _, node := range nodes {
		if node == nil || !node.Enabled || !node.Healthy {
			continue
		}
		hadHealthyNode = true
		reserved := s.remoteReservations[node.URL]
		if reserved >= capacity {
			continue
		}
		effectiveLoad := node.ActiveJobs + reserved
		if best == nil || effectiveLoad < bestLoad {
			best = node
			bestLoad = effectiveLoad
		}
	}
	if best == nil {
		if hadHealthyNode {
			return nil, func() {}, chapterThumbnailNodeCapacityExhaustedReason
		}
		return nil, func() {}, chapterThumbnailNodeUnavailableReason
	}

	s.remoteReservations[best.URL]++
	return best, func() {
		s.remoteMu.Lock()
		defer s.remoteMu.Unlock()
		current := s.remoteReservations[best.URL]
		if current <= 1 {
			delete(s.remoteReservations, best.URL)
			return
		}
		s.remoteReservations[best.URL] = current - 1
	}, ""
}

func (s *Service) uploadChapterThumbnail(ctx context.Context, fileID, chapterIndex int, frame []byte) (string, string, error) {
	if s.uploadChapterThumbnailFunc != nil {
		return s.uploadChapterThumbnailFunc(ctx, fileID, chapterIndex, frame)
	}

	result, err := imageutil.GenerateVariants(frame, []int{300})
	if err != nil {
		return "", "", fmt.Errorf("generate variants: %w", err)
	}

	bucket := s.store.Bucket()
	var originalKey string
	var w300Data []byte
	for _, variant := range result.Variants {
		key := filepath.ToSlash(fmt.Sprintf("chapter-images/%d/%d/%s%s", fileID, chapterIndex, variant.Key, result.Ext))
		if err := s.store.PutObject(ctx, bucket, key, variant.Data); err != nil {
			return "", "", fmt.Errorf("upload %s: %w", key, err)
		}
		if variant.Key == "original" {
			originalKey = key
		}
		if variant.Key == "w300" {
			w300Data = variant.Data
		}
	}

	thumbhashSource := w300Data
	if len(thumbhashSource) == 0 && len(result.Variants) > 0 {
		thumbhashSource = result.Variants[0].Data
	}
	thumbhash, err := imageutil.Thumbhash(thumbhashSource)
	if err != nil {
		return "", "", fmt.Errorf("thumbhash: %w", err)
	}
	return originalKey, thumbhash, nil
}

func (s *Service) enqueue(req ChapterThumbnailRequest, priority bool) bool {
	if req.FileID <= 0 {
		return false
	}

	s.mu.Lock()
	enqueued := false
	action := ""

	if priority {
		if existing, ok := s.queuedPriority[req.FileID]; ok {
			s.queuedPriority[req.FileID] = mergeRequest(existing, req)
			action = "updated_priority"
		} else if existing, ok := s.queuedNormal[req.FileID]; ok {
			delete(s.queuedNormal, req.FileID)
			req = mergeRequest(existing, req)
			s.queuedPriority[req.FileID] = req
			s.priorityQueue = append(s.priorityQueue, req.FileID)
			enqueued = true
			action = "promoted_to_priority"
		} else {
			s.queuedPriority[req.FileID] = req
			s.priorityQueue = append(s.priorityQueue, req.FileID)
			enqueued = true
			action = "queued_priority"
		}
	} else if _, ok := s.inProgress[req.FileID]; ok {
		action = "skipped_in_progress"
	} else if _, ok := s.queuedPriority[req.FileID]; ok {
		action = "skipped_priority_already_queued"
	} else if _, ok := s.queuedNormal[req.FileID]; ok {
		action = "skipped_normal_already_queued"
	} else {
		s.queuedNormal[req.FileID] = req
		s.normalQueue = append(s.normalQueue, req.FileID)
		enqueued = true
		action = "queued_normal"
	}

	priorityDepth := len(s.queuedPriority)
	normalDepth := len(s.queuedNormal)
	inProgress := len(s.inProgress)
	s.mu.Unlock()

	slog.Info(
		"chapter thumbnail queue event",
		"file_id",
		req.FileID,
		"priority",
		priority,
		"action",
		action,
		"target_seconds",
		requestTargetSeconds(req),
		"priority_depth",
		priorityDepth,
		"normal_depth",
		normalDepth,
		"in_progress",
		inProgress,
	)

	return enqueued
}

func mergeRequest(existing ChapterThumbnailRequest, incoming ChapterThumbnailRequest) ChapterThumbnailRequest {
	if incoming.TargetSeconds != nil {
		existing.TargetSeconds = incoming.TargetSeconds
	}
	return existing
}

func (s *Service) notifyNormalWorker() {
	select {
	case s.notifyNormal <- struct{}{}:
	default:
	}
}

func (s *Service) notifyPriorityWorker() {
	select {
	case s.notifyPriority <- struct{}{}:
	default:
	}
}

func (s *Service) nextRequest(ctx context.Context, priorityOnly bool) (ChapterThumbnailRequest, bool) {
	for {
		s.mu.Lock()
		if req, ok := s.popQueuedLocked(true); ok {
			priorityDepth := len(s.queuedPriority)
			normalDepth := len(s.queuedNormal)
			inProgress := len(s.inProgress)
			s.mu.Unlock()
			slog.InfoContext(ctx,
				"chapter thumbnail dequeued", "component", "chapterthumbs",
				"file_id",
				req.FileID,
				"priority",
				true,
				"target_seconds",
				requestTargetSeconds(req),
				"priority_only_worker",
				priorityOnly,
				"priority_depth",
				priorityDepth,
				"normal_depth",
				normalDepth,
				"in_progress",
				inProgress,
			)
			return req, true
		}
		if !priorityOnly {
			if req, ok := s.popQueuedLocked(false); ok {
				priorityDepth := len(s.queuedPriority)
				normalDepth := len(s.queuedNormal)
				inProgress := len(s.inProgress)
				s.mu.Unlock()
				slog.InfoContext(ctx,
					"chapter thumbnail dequeued", "component", "chapterthumbs",
					"file_id",
					req.FileID,
					"priority",
					false,
					"target_seconds",
					requestTargetSeconds(req),
					"priority_only_worker",
					priorityOnly,
					"priority_depth",
					priorityDepth,
					"normal_depth",
					normalDepth,
					"in_progress",
					inProgress,
				)
				return req, true
			}
		}
		s.mu.Unlock()

		if priorityOnly {
			select {
			case <-ctx.Done():
				return ChapterThumbnailRequest{}, false
			case <-s.notifyPriority:
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ChapterThumbnailRequest{}, false
		case <-s.notifyPriority:
		case <-s.notifyNormal:
		}
	}
}

func (s *Service) finishProcessing(fileID int) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inProgress, fileID)
	_, notifyPriority := s.queuedPriority[fileID]
	_, notifyNormal := s.queuedNormal[fileID]
	return notifyPriority, notifyNormal
}

func (s *Service) popQueuedLocked(priority bool) (ChapterThumbnailRequest, bool) {
	queue := &s.normalQueue
	queued := s.queuedNormal
	if priority {
		queue = &s.priorityQueue
		queued = s.queuedPriority
	}

	for i, fileID := range *queue {
		req, ok := queued[fileID]
		if !ok {
			continue
		}
		if _, busy := s.inProgress[fileID]; busy {
			continue
		}
		delete(queued, fileID)
		s.inProgress[fileID] = struct{}{}
		*queue = append((*queue)[:i], (*queue)[i+1:]...)
		return req, true
	}

	*queue = compactQueue(*queue, queued)
	return ChapterThumbnailRequest{}, false
}

func compactQueue(queue []int, queued map[int]ChapterThumbnailRequest) []int {
	if len(queue) == 0 {
		return queue
	}
	compacted := make([]int, 0, len(queue))
	for _, fileID := range queue {
		if _, ok := queued[fileID]; ok {
			compacted = append(compacted, fileID)
		}
	}
	return compacted
}

func requestTargetSeconds(req ChapterThumbnailRequest) any {
	if req.TargetSeconds == nil {
		return nil
	}
	return *req.TargetSeconds
}

func (s *Service) batchSize(priority bool) int {
	if priority {
		if s.priorityBatchSize > 0 {
			return s.priorityBatchSize
		}
		return defaultPriorityBatchSize
	}
	if s.normalBatchSize > 0 {
		return s.normalBatchSize
	}
	return defaultNormalBatchSize
}

func (s *Service) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) chapterThumbnailHDRPolicy(ctx context.Context) string {
	if s == nil || s.settings == nil {
		return chapterThumbnailHDRPolicyDefault
	}
	value, err := s.settings.Get(ctx, chapterThumbnailHDRPolicySetting)
	if err != nil {
		return chapterThumbnailHDRPolicyDefault
	}
	switch strings.TrimSpace(strings.ToLower(value)) {
	case chapterThumbnailHDRPolicyDisabled:
		return chapterThumbnailHDRPolicyDisabled
	case "", chapterThumbnailHDRPolicyBestEffort:
		return chapterThumbnailHDRPolicyBestEffort
	default:
		return chapterThumbnailHDRPolicyDefault
	}
}

func hasEligibleMissingChapter(chapters []models.MediaChapter, now time.Time) bool {
	for _, chapter := range chapters {
		if isChapterEligible(chapter, now) {
			return true
		}
	}
	return false
}

func selectChapterCandidates(
	chapters []models.MediaChapter,
	targetSeconds *float64,
	priority bool,
	limit int,
	now time.Time,
) []chapterCandidate {
	if limit <= 0 {
		return nil
	}

	candidates := make([]chapterCandidate, 0, len(chapters))
	for offset, chapter := range chapters {
		if !isChapterEligible(chapter, now) {
			continue
		}
		candidate := chapterCandidate{offset: offset, chapter: chapter}
		if targetSeconds != nil {
			candidate.distance = math.Abs(chapterCaptureTime(chapter) - *targetSeconds)
		}
		candidates = append(candidates, candidate)
	}

	if len(candidates) == 0 {
		return nil
	}
	if !priority {
		if len(candidates) > limit {
			return candidates[:limit]
		}
		return candidates
	}

	if targetSeconds == nil {
		if len(candidates) > limit {
			return candidates[:limit]
		}
		return candidates
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].distance == candidates[j].distance {
			return candidates[i].chapter.Index < candidates[j].chapter.Index
		}
		return candidates[i].distance < candidates[j].distance
	})
	if len(candidates) > limit {
		return candidates[:limit]
	}
	return candidates
}

func isChapterEligible(chapter models.MediaChapter, now time.Time) bool {
	if chapter.ThumbnailPath != "" {
		return false
	}
	if chapter.ThumbnailRetryAfter != nil && chapter.ThumbnailRetryAfter.After(now) {
		return false
	}
	return true
}

func needsTonemap(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if file.HDR {
		return true
	}
	for _, track := range file.VideoTracks {
		if strings.TrimSpace(track.DolbyVision) != "" {
			return true
		}
	}
	return false
}

func applyChapterSuccess(chapter *models.MediaChapter, thumbnailPath string, thumbnailThumbhash string) {
	if chapter == nil {
		return
	}
	chapter.ThumbnailPath = thumbnailPath
	chapter.ThumbnailThumbhash = thumbnailThumbhash
	chapter.ThumbnailRetryAfter = nil
	chapter.ThumbnailFailedAt = nil
	chapter.ThumbnailLastError = ""
}

func recordChapterFailure(chapter *models.MediaChapter, now time.Time, reason string, err error) {
	if chapter == nil {
		return
	}
	failedAt := now
	retryAfter := now.Add(nextChapterRetryDuration(*chapter))
	chapter.ThumbnailPath = ""
	chapter.ThumbnailThumbhash = ""
	chapter.ThumbnailFailedAt = &failedAt
	chapter.ThumbnailRetryAfter = &retryAfter
	chapter.ThumbnailLastError = failureDetail(reason, err)
}

func nextChapterRetryDuration(chapter models.MediaChapter) time.Duration {
	if chapter.ThumbnailFailedAt == nil || chapter.ThumbnailRetryAfter == nil {
		return chapterThumbnailRetrySchedule[0]
	}
	previous := chapter.ThumbnailRetryAfter.Sub(*chapter.ThumbnailFailedAt)
	for _, candidate := range chapterThumbnailRetrySchedule {
		if previous < candidate {
			return candidate
		}
	}
	return chapterThumbnailRetrySchedule[len(chapterThumbnailRetrySchedule)-1]
}

func retryDurationForCount(failureCount int) time.Duration {
	if failureCount <= 1 {
		return chapterThumbnailRetrySchedule[0]
	}
	index := failureCount - 1
	if index >= len(chapterThumbnailRetrySchedule) {
		index = len(chapterThumbnailRetrySchedule) - 1
	}
	return chapterThumbnailRetrySchedule[index]
}

func shouldApplyFileFailure(reason string) bool {
	switch reason {
	case "decode_invalid_data":
		return true
	default:
		return false
	}
}

func buildPersistentFileFailure(
	file *models.MediaFile,
	now time.Time,
	reason string,
	err error,
) *scanner.ChapterThumbnailFailureState {
	failureCount := len(chapterThumbnailRetrySchedule)
	if file != nil && file.ChapterThumbnailFailureCount >= failureCount {
		failureCount = file.ChapterThumbnailFailureCount + 1
	}
	retryAfter := now.Add(retryDurationForCount(failureCount))
	return &scanner.ChapterThumbnailFailureState{
		Apply:        true,
		RetryAfter:   &retryAfter,
		FailureCount: failureCount,
		LastError:    failureDetail(reason, err),
	}
}

func failureDetail(reason string, err error) string {
	if err == nil {
		return reason
	}
	if reason == "" {
		return err.Error()
	}
	return fmt.Sprintf("%s: %v", reason, err)
}

func wrapReason(reason string, err error) error {
	if err == nil || reason == "" {
		return err
	}
	return fmt.Errorf("%s: %w", reason, err)
}

func classifyProbeError(err error) string {
	if isDeadlineError(err) {
		return "probe_timeout"
	}
	return "probe_failed"
}

func chapterCaptureTime(chapter models.MediaChapter) float64 {
	duration := chapter.EndSeconds - chapter.StartSeconds
	offset := 5.0
	if quarter := duration * 0.25; quarter < offset {
		offset = quarter
	}
	seek := chapter.StartSeconds + offset
	if seek >= chapter.EndSeconds {
		seek = chapter.EndSeconds - 0.1
	}
	if seek < chapter.StartSeconds {
		seek = chapter.StartSeconds
	}
	if seek < 0 {
		return 0
	}
	return seek
}
