package chapterthumbs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

const (
	defaultWorkerCount         = 1
	defaultPriorityWorkerCount = 1
	defaultQueueSize           = 128
	defaultBatchLimit          = 25
	defaultPriorityBatchSize   = 3
	defaultNormalBatchSize     = 8
	chapterLockRetryDelay      = time.Second
	hwExtractTimeoutSDR        = 8 * time.Second
	hwExtractTimeoutHDR        = 20 * time.Second
	cpuExtractTimeoutSDR       = 10 * time.Second
	cpuExtractTimeoutHDR       = 25 * time.Second

	chapterThumbnailHDRPolicySetting    = "playback.chapter_thumbnail_hdr_policy"
	chapterThumbnailHDRPolicyDefault    = "best_effort"
	chapterThumbnailHDRPolicyDisabled   = "disabled"
	chapterThumbnailHDRPolicyBestEffort = "best_effort"

	// Hardware acceleration is read per extraction from these keys, not frozen
	// at startup, so an admin change applies to the next chapter thumbnail.
	// playbackHWAccelDefault mirrors the config loader's default for an unset
	// row (internal/config/admin_settings.go).
	playbackHWAccelSetting  = "playback.hw_accel"
	playbackHWDeviceSetting = "playback.hw_device"
	playbackHWAccelDefault  = "auto"
)

var chapterThumbnailRetrySchedule = []time.Duration{
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

var errChapterThumbnailLockBusy = errors.New("chapter thumbnail extraction is already running on another replica")

type FileRepository interface {
	// Take this guard before reading a file, and hold it until extraction
	// results are saved, so replicas cannot process stale chapters together.
	TryLockChapterThumbnails(context.Context, int) (lockCtx context.Context, release func(), acquired bool, err error)
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
	// ListMissingChapterThumbnails lists files with a chapter to make a
	// thumbnail for: none yet, or one whose path does not end in
	// currentSuffix, made at another width.
	ListMissingChapterThumbnails(ctx context.Context, limit int, currentSuffix string) ([]*models.MediaFile, error)
	ListChapterThumbnailsAtOtherWidths(ctx context.Context, limit int, currentSuffix string, afterID int, skipHDR bool) ([]*models.MediaFile, time.Time, error)
	// ChapterThumbnailLibraryKey changes when the set of enabled libraries
	// opted into chapter thumbnails changes, without reading media files.
	ChapterThumbnailLibraryKey(context.Context) (string, error)
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

// ProbeEnsurer repairs probe metadata. Only the repair half is needed here:
// chapter extraction reads Chapters, never the H.264 copy-safety verdict, so
// this deliberately does not ask for the bitstream scan.
type ProbeEnsurer interface {
	EnsureProbeOnly(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error)
}

type SettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type ObjectStore = blobstore.Store

// BlobQueue schedules the deletion of images a newer one replaced
// (*blobgc.Queue).
type BlobQueue interface {
	Schedule(ctx context.Context, prefixes []string, delay time.Duration) error
}

// displacedImageGrace is the minimum time a replaced image stays stored.
// The queue keeps any later expiry recorded by the chapter URL resolver.
const displacedImageGrace = 48 * time.Hour

type ThumbnailNotifier interface {
	ChapterThumbnailReady(ctx context.Context, fileID int, chapterIndex int, thumbnailPath string, thumbnailThumbhash string)
}

type ChapterThumbnailRequest struct {
	FileID        int
	TargetSeconds *float64
	// Set by dequeue so an ordinary worker preserves playback priority.
	priority bool
}

type Service struct {
	fileRepo     FileRepository
	folderRepo   FolderRepository
	probeEnsurer ProbeEnsurer
	settings     SettingsReader
	store        ObjectStore
	notifier     ThumbnailNotifier
	blobQueue    BlobQueue
	ffmpegPath   string
	// hwAccel and hwDevice hold the playback.hw_accel / playback.hw_device
	// values captured when the service was built. They are only the fallback:
	// resolveHWConfig re-reads both settings per extraction so an admin who
	// changes hardware acceleration does not have to restart the server for
	// chapter-thumbnail extraction to follow.
	hwAccel  string
	hwDevice string

	// hwMu guards the resolved-accelerator memo below. Resolving "auto" execs
	// an FFmpeg capability probe and logs the verdict, so the result is cached
	// against the configured values that produced it and recomputed only when
	// they actually change.
	//
	// Both values, not just the backend: the walk is over the configured device
	// set, so a device edit changes which backends have candidates to verify and
	// therefore what "auto" resolves to. Keying on the backend alone would hold
	// a verdict taken against the old device list.
	hwMu             sync.Mutex
	hwResolved       bool
	hwResolvedFrom   string
	hwResolvedDevice string
	resolvedHWAccel  string

	notifyNormal        chan struct{}
	notifyPriority      chan struct{}
	widthQueueSpace     chan struct{}
	workerCount         int
	priorityWorkerCount int
	priorityBatchSize   int
	normalBatchSize     int

	mu             sync.Mutex
	priorityQueue  []int
	normalQueue    []int
	queuedPriority map[int]ChapterThumbnailRequest
	queuedNormal   map[int]ChapterThumbnailRequest
	priorityRetry  map[int]time.Time
	inProgress     map[int]struct{}

	transcodePool      *nodepool.TranscodePool
	remoteReservations *nodepool.Reservations
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

// SetBlobQueue lets the service retire the images a width change replaces.
// Without one they stay stored until their file is deleted.
func (s *Service) SetBlobQueue(queue BlobQueue) {
	if s == nil {
		return
	}
	s.blobQueue = queue
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
		widthQueueSpace:     make(chan struct{}, 1),
		workerCount:         workerCount,
		priorityWorkerCount: defaultPriorityWorkerCount,
		priorityBatchSize:   defaultPriorityBatchSize,
		normalBatchSize:     defaultNormalBatchSize,
		queuedPriority:      make(map[int]ChapterThumbnailRequest),
		queuedNormal:        make(map[int]ChapterThumbnailRequest),
		inProgress:          make(map[int]struct{}),
		transcodePool:       transcodePool,
		remoteReservations:  &nodepool.Reservations{},
		remoteExtractor:     &httpRemoteFrameExtractor{},
		clock:               time.Now,
	}
}

func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}

	// Logged for the boot record only: both values are re-read per extraction,
	// so a later settings change is honored without a restart.
	resolvedAccel, resolvedDevice := s.resolveHWConfig(ctx)
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
	go s.followPreviewWidth(ctx)
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

	width, err := s.previewImageWidth(ctx)
	if err != nil {
		return 0, err
	}
	files, err := s.fileRepo.ListMissingChapterThumbnails(ctx, limit, chapterThumbnailSuffix(width))
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
		requeueNormal, err := s.processRequest(ctx, req, req.priority)
		if errors.Is(err, errChapterThumbnailLockBusy) {
			s.retryPriorityRequest(req)
		} else if err != nil {
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
	lockCtx, release, acquired, err := s.fileRepo.TryLockChapterThumbnails(ctx, req.FileID)
	if err != nil {
		return false, fmt.Errorf("lock chapter thumbnail extraction: %w", err)
	}
	if !acquired {
		if priority {
			return false, errChapterThumbnailLockBusy
		}
		return false, nil
	}
	defer release()
	ctx = lockCtx
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

	width, err := s.previewImageWidth(ctx)
	if err != nil {
		return false, err
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

	selected := selectChapterCandidates(file.Chapters, req.TargetSeconds, priority, s.batchSize(priority), now, width)
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
				hardFileFailure = buildFileFailure(&updated, now, reason, err)
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

		path, thumbhash, err := s.uploadChapterThumbnail(ctx, updated.ID, chapter.Index, frame, width)
		if err != nil {
			slog.WarnContext(ctx,
				"chapter thumbnail upload failed", "component", "chapterthumbs",
				"file_id",
				updated.ID,
				"chapter_index",
				chapter.Index,
				"reason",
				reasonChapterExtractFailed,
				"error",
				err,
			)
			recordChapterFailure(&updated.Chapters[candidate.offset], now, reasonChapterExtractFailed, err)
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
		s.retireDisplaced(ctx, file.Chapters, generated)
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

	// A first image may have been absent when the width coordinator scanned
	// during extraction. Check the live width after saving; the coordinator
	// also keeps missing images pending if this settings read fails.
	currentWidth, widthErr := s.previewImageWidth(ctx)
	if widthErr != nil {
		return false, widthErr
	}
	requeue := hardFileFailure == nil && hasEligibleMissingChapter(updated.Chapters, now, currentWidth)
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

	ensured, err := s.probeEnsurer.EnsureProbeOnly(ctx, file)
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
	allowSoftwareToneMap := toneMap && s.chapterThumbnailSoftwareToneMapEnabled(ctx)
	mode := s.chapterThumbnailExecutionMode(ctx)
	if mode == chapterThumbnailExecutionLocal {
		return s.extractFrameLocal(ctx, file.FilePath, seekSeconds, toneMap, allowSoftwareToneMap)
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
			return s.extractFrameLocal(ctx, file.FilePath, seekSeconds, toneMap, allowSoftwareToneMap)
		}
		return nil, nodeReason, wrapReason(nodeReason, fmt.Errorf("no transcode node available for chapter thumbnail extraction"))
	}
	defer release()

	jwtSecret := s.chapterThumbnailJWTSecret(ctx)
	data, reason, err := s.remoteExtractor.ExtractFrame(ctx, node, jwtSecret, RemoteExtractRequest{
		InputPath:            file.FilePath,
		SeekSeconds:          seekSeconds,
		ToneMap:              toneMap,
		AllowSoftwareToneMap: allowSoftwareToneMap,
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
		return s.extractFrameLocal(ctx, file.FilePath, seekSeconds, toneMap, allowSoftwareToneMap)
	}

	return nil, reason, err
}

func (s *Service) extractFrameLocal(
	ctx context.Context,
	inputPath string,
	seekSeconds float64,
	toneMap bool,
	allowSoftwareToneMap bool,
) ([]byte, string, error) {
	resolvedAccel, resolvedDevice := s.resolveHWConfig(ctx)
	return ExtractFrame(ctx, FrameExtractOptions{
		InputPath:            inputPath,
		SeekSeconds:          seekSeconds,
		FFmpegPath:           s.ffmpegPath,
		HWAccel:              resolvedAccel,
		HWDevice:             resolvedDevice,
		ToneMap:              toneMap,
		AllowSoftwareToneMap: allowSoftwareToneMap,
		RunFunc:              s.runFFmpegFrameExtractFunc,
	})
}

// resolveHWConfig returns the accelerator and device this extraction should
// use. Both come from the live settings repo rather than from a value frozen at
// startup, which is what lets playback.hw_accel / playback.hw_device take
// effect without a server restart.
func (s *Service) resolveHWConfig(ctx context.Context) (string, string) {
	configuredAccel, configuredDevice := s.configuredHWConfig(ctx)

	s.hwMu.Lock()
	defer s.hwMu.Unlock()
	if !s.hwResolved || s.hwResolvedFrom != configuredAccel || s.hwResolvedDevice != configuredDevice {
		// The device set is an input to the walk, not just to execution: it is
		// what decides which backends have candidates to probe.
		s.resolvedHWAccel = playback.ResolveHWAccelWithFFmpeg(configuredAccel, s.ffmpegPath, configuredDevice)
		s.hwResolvedFrom = configuredAccel
		s.hwResolvedDevice = configuredDevice
		s.hwResolved = true
	}
	// The configured device value passes through raw: ExtractFrame resolves it
	// (multi-device balancing, empty-value auto-detection) per extraction.
	return s.resolvedHWAccel, configuredDevice
}

// configuredHWConfig reads playback.hw_accel / playback.hw_device from the
// settings repo, mirroring how the config loader defaults them. A settings repo
// that is absent (test doubles) or failing falls back to the values captured at
// construction, so a database blip keeps the boot configuration instead of
// silently dropping extraction to software.
func (s *Service) configuredHWConfig(ctx context.Context) (string, string) {
	if s == nil {
		return "", ""
	}
	accel := s.hwAccel
	if value, ok := s.readSetting(ctx, playbackHWAccelSetting); ok {
		accel = value
		if accel == "" {
			accel = playbackHWAccelDefault
		}
	}
	device := s.hwDevice
	if value, ok := s.readSetting(ctx, playbackHWDeviceSetting); ok {
		// An empty device is a meaningful value ("auto-detect one"), so unlike
		// the accelerator it is not replaced by a default.
		device = value
	}
	return accel, device
}

// readSetting reports the trimmed setting value and whether the settings repo
// answered at all. The second result is what lets callers tell "configured
// empty" apart from "could not read".
func (s *Service) readSetting(ctx context.Context, key string) (string, bool) {
	if s == nil || s.settings == nil {
		return "", false
	}
	value, err := s.settings.Get(ctx, key)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(value), true
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

	node, release, outcome := s.remoteReservations.Reserve(s.transcodePool.Nodes(), s.chapterThumbnailNodeCapacity(ctx))
	switch outcome {
	case nodepool.Reserved:
		return node, release, ""
	case nodepool.CapacityExhausted:
		return nil, release, chapterThumbnailNodeCapacityExhaustedReason
	default:
		return nil, release, chapterThumbnailNodeUnavailableReason
	}
}

func (s *Service) uploadChapterThumbnail(ctx context.Context, fileID, chapterIndex int, frame []byte, width int) (string, string, error) {
	if s.uploadChapterThumbnailFunc != nil {
		return s.uploadChapterThumbnailFunc(ctx, fileID, chapterIndex, frame)
	}

	data, err := imageutil.EncodeWebPWidth(frame, width)
	if err != nil {
		return "", "", fmt.Errorf("encode thumbnail: %w", err)
	}
	// Immutable output keys also fence storage: a worker whose lock session
	// dies may finish an outstanding upload, but different stale bytes cannot
	// overwrite the image a replacement worker published.
	digest := sha256.Sum256(data)
	key := fmt.Sprintf("%s%d/%d-%x/w%d.webp", chapterImagesPrefix, fileID, chapterIndex, digest, width)
	// Defer queued deletion before reusing a width, waiting for a collector
	// that already holds the row. Keep any longer issued URL expiry: removing
	// that protection would let file deletion collect the image too early.
	if s.blobQueue != nil {
		if err := s.blobQueue.Schedule(ctx, []string{key}, displacedImageGrace); err != nil {
			return "", "", err
		}
	}
	if err := s.store.Put(ctx, key, data); err != nil {
		return "", "", fmt.Errorf("upload %s: %w", key, err)
	}
	thumbhash, err := imageutil.Thumbhash(data)
	if err != nil {
		return "", "", fmt.Errorf("thumbhash: %w", err)
	}
	return key, thumbhash, nil
}

// previewImageWidth is the width of the one image stored per chapter: the
// preview image width setting, shared with seek previews. It is the size
// every client is served: the web seek-bar preview and chapters menu, and
// the thumbnail_url the native apps decode. No full-size original is kept, so
// a new width makes every chapter's thumbnail again, and the old image serves
// until its replacement exists. A setting that cannot be read is an error:
// going on with the default would remake every thumbnail made to another.
func (s *Service) previewImageWidth(ctx context.Context) (int, error) {
	if s.settings == nil {
		return config.DefaultPreviewImageWidth, nil
	}
	value, err := s.settings.Get(ctx, config.PreviewImageWidthSettingKey)
	if err != nil {
		return 0, fmt.Errorf("read the preview image width: %w", err)
	}
	return config.PreviewImageWidth(value), nil
}

// chapterThumbnailKey is the legacy numeric chapter-directory key. Existing
// references remain supported alongside new immutable image keys.
func chapterThumbnailKey(fileID, chapterIndex, width int) string {
	return fmt.Sprintf("%s%d/%d/w%d.webp", chapterImagesPrefix, fileID, chapterIndex, width)
}

// chapterThumbnailSuffix ends the key of every thumbnail made at width.
func chapterThumbnailSuffix(width int) string {
	return fmt.Sprintf("/w%d.webp", width)
}

// retireDisplaced queues the images that generated replaced for deletion,
// once the chapters naming their replacements are saved. A failure only
// leaves the old images stored until their file is deleted.
func (s *Service) retireDisplaced(ctx context.Context, before []models.MediaChapter, generated []generatedChapter) {
	if s.blobQueue == nil {
		return
	}
	var displaced []string
	for _, ready := range generated {
		if ready.offset < 0 || ready.offset >= len(before) {
			continue
		}
		old := before[ready.offset].ThumbnailPath
		if _, ok := imageKeyGroup(old); ok && old != ready.path {
			displaced = append(displaced, old)
		}
	}
	if err := s.blobQueue.Schedule(ctx, displaced, displacedImageGrace); err != nil {
		slog.WarnContext(ctx, "chapter thumbnail cleanup not queued", "component", "chapterthumbs", "images", len(displaced), "error", err)
	}
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

// retryPriorityRequest retains a contended playback request while workers can
// serve other files. A newer queued target takes precedence over this attempt.
func (s *Service) retryPriorityRequest(req ChapterThumbnailRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.queuedPriority[req.FileID]; ok {
		req = mergeRequest(req, existing)
	} else {
		delete(s.queuedNormal, req.FileID)
		s.priorityQueue = append(s.priorityQueue, req.FileID)
	}
	s.queuedPriority[req.FileID] = req
	if s.priorityRetry == nil {
		s.priorityRetry = make(map[int]time.Time)
	}
	s.priorityRetry[req.FileID] = time.Now().Add(chapterLockRetryDelay)
}

func (s *Service) nextPriorityRetryLocked() <-chan time.Time {
	var earliest time.Time
	for fileID, retryAfter := range s.priorityRetry {
		if _, busy := s.inProgress[fileID]; busy {
			continue
		}
		if _, queued := s.queuedPriority[fileID]; queued && (earliest.IsZero() || retryAfter.Before(earliest)) {
			earliest = retryAfter
		}
	}
	if earliest.IsZero() {
		return nil
	}
	return time.After(max(time.Until(earliest), 0))
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
		retry := s.nextPriorityRetryLocked()
		s.mu.Unlock()

		if priorityOnly {
			select {
			case <-ctx.Done():
				return ChapterThumbnailRequest{}, false
			case <-s.notifyPriority:
			case <-retry:
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ChapterThumbnailRequest{}, false
		case <-s.notifyPriority:
		case <-s.notifyNormal:
		case <-retry:
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
		if priority && time.Now().Before(s.priorityRetry[fileID]) {
			continue
		}
		req.priority = priority
		delete(s.priorityRetry, fileID)
		delete(queued, fileID)
		s.inProgress[fileID] = struct{}{}
		*queue = append((*queue)[:i], (*queue)[i+1:]...)
		select {
		case s.widthQueueSpace <- struct{}{}:
		default:
		}
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

func (s *Service) chapterThumbnailSoftwareToneMapEnabled(ctx context.Context) bool {
	if s == nil || s.settings == nil {
		return false
	}
	value, err := s.settings.Get(ctx, config.ChapterThumbnailSoftwareToneMapSettingKey)
	return err == nil && config.AdminSettingEnabled(config.ChapterThumbnailSoftwareToneMapSettingKey, value)
}

func hasEligibleMissingChapter(chapters []models.MediaChapter, now time.Time, width int) bool {
	for _, chapter := range chapters {
		if isChapterEligible(chapter, now, width) {
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
	width int,
) []chapterCandidate {
	if limit <= 0 {
		return nil
	}

	candidates := make([]chapterCandidate, 0, len(chapters))
	for offset, chapter := range chapters {
		if !isChapterEligible(chapter, now, width) {
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

// isChapterEligible reports whether a chapter needs a thumbnail made: it has
// none, or has one made at another width, and is not waiting out a failure.
func isChapterEligible(chapter models.MediaChapter, now time.Time, width int) bool {
	if chapter.ThumbnailPath != "" && strings.HasSuffix(chapter.ThumbnailPath, chapterThumbnailSuffix(width)) {
		return false
	}
	if chapter.ThumbnailRetryAfter != nil && chapter.ThumbnailRetryAfter.After(now) {
		return false
	}
	return true
}

// needsTonemap reports whether thumbnail extraction must convert HDR to SDR.
func needsTonemap(file *models.MediaFile) bool {
	return tonemap.NeedsToneMap(file)
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
	// A thumbnail made at another width keeps serving until one at this
	// width replaces it.
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
	case reasonDecodeInvalidData, reasonFFmpegProbeFailed, reasonToneMapUnsupported:
		return true
	default:
		return false
	}
}

func buildFileFailure(
	file *models.MediaFile,
	now time.Time,
	reason string,
	err error,
) *scanner.ChapterThumbnailFailureState {
	failureCount := 1
	if reason == reasonDecodeInvalidData {
		failureCount = len(chapterThumbnailRetrySchedule)
		if file != nil && file.ChapterThumbnailFailureCount >= failureCount {
			failureCount = file.ChapterThumbnailFailureCount + 1
		}
	} else if file != nil {
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
