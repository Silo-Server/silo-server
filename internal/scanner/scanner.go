package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// videoExtensions is the set of file extensions recognized as media files.
var videoExtensions = map[string]bool{
	".mkv": true,
	".mp4": true,
	".avi": true,
	".m4v": true,
	".ts":  true,
	".wmv": true,
}

// SupportsVideoFile reports whether the given path uses a recognized media extension.
func SupportsVideoFile(filePath string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(filePath))]
}

// ignoredDirNames is the set of directory names skipped during scanning.
var ignoredDirNames = map[string]bool{
	".recyclebin":  true,
	"@recycle":     true,
	"@eadir":       true,
	".trash":       true,
	"#recycle":     true,
	"$recycle.bin": true,
	".deleted":     true,
	".inbound":     true,
	".downloads":   true,
}

var ignoredMovieSupplementalDirNames = map[string]bool{
	"sample":            true,
	"samples":           true,
	"extra":             true,
	"extras":            true,
	"featurette":        true,
	"featurettes":       true,
	"behind the scenes": true,
	"deleted scenes":    true,
	"trailer":           true,
	"trailers":          true,
	"subs":              true,
	"subtitles":         true,
}

func normalizeScannerDirLabel(name string) string {
	surface := strings.ToLower(strings.TrimSpace(name))
	surface = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(surface)
	return strings.Join(strings.Fields(surface), " ")
}

func shouldSkipMovieSupplementalDir(path string) bool {
	label := normalizeScannerDirLabel(filepath.Base(path))
	if label == "" {
		return false
	}
	return ignoredMovieSupplementalDirNames[label]
}

func shouldSkipMovieSupplementalFile(path string) bool {
	baseNoExt := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	surface := normalizeScannerDirLabel(baseNoExt)
	if surface == "" {
		return false
	}
	if surface != "sample" && !strings.HasPrefix(surface, "sample ") && !strings.HasSuffix(surface, " sample") {
		return false
	}

	parentTitle, parentYear, trusted := naming.ParseInferFolderTitleYear(filepath.Base(filepath.Dir(path)))
	if !trusted {
		return true
	}
	stem := naming.ParseInferMovieStem(baseNoExt, parentTitle, parentYear)
	return stem.Title == "" || !naming.InferTitlesCoherent(parentTitle, stem.Title)
}

// Scanner discovers and indexes media files in media folders.
// scannerImageCacher is the slice of imagecache.Cacher the audiobook
// branch uses. Wired via SetImageCacher; nil-safe when absent.
type scannerImageCacher = audiobookCoverCacher

type Scanner struct {
	fileRepo            *FileRepository
	rootSnapshotRepo    *ScannedRootRepository
	groupSnapshotRepo   *ScannedGroupRepository
	rootOverrideRepo    *MediaRootOverrideRepository
	groupOverrideRepo   *MediaGroupOverrideRepository
	locationRepo        *ObservedLocationRepository
	groupLocationRepo   *GroupLocationRepository
	folderRepo          *catalog.FolderRepository
	libraryRepo         *catalog.LibraryItemRepository
	episodeLibraryRepo  *catalog.EpisodeLibraryRepository
	itemRepo            *catalog.ItemRepository
	personRepo          *catalog.PersonRepository
	episodeRepo         *catalog.EpisodeRepository
	ffprobePath         string
	s3Client            *s3client.Client // public assets bucket (may be nil)
	imageCacher         scannerImageCacher
	workers             int
	emptyTrashAfterScan bool
	markerFetcher       func(context.Context, string) *IntroCreditsMarkers
	metadataQueue       MetadataQueueProducer
	movieQueueSyncer    MovieQueueSyncer
	seriesQueueSyncer   SeriesQueueSyncer
}

// SetImageCacher installs the imagecache.Cacher used by the audiobook
// branch to push embedded M4B cover art into the public assets bucket.
// Optional; if unset, audiobook covers are not extracted.
func (s *Scanner) SetImageCacher(cacher scannerImageCacher) {
	if s == nil {
		return
	}
	s.imageCacher = cacher
}

const scanProgressLogInterval = 10 * time.Second

// MetadataQueueProducer enqueues durable initial metadata work rows after a
// successful scan upsert.
type MetadataQueueProducer interface {
	EnqueueMovieFile(ctx context.Context, fileID int) error
	EnqueueSeriesRoot(ctx context.Context, folderID int, observedRootPath string) error
}

// MovieQueueSyncer synchronizes pending movie-file match queue state from the
// scanner's persisted file rows.
type MovieQueueSyncer interface {
	SyncForFolder(ctx context.Context, folderID int) error
	SyncInScope(ctx context.Context, folderID int, scopePath string) error
}

// SeriesQueueSyncer synchronizes pending series-root match queue state from
// the scanner's persisted snapshot tables.
type SeriesQueueSyncer interface {
	SyncForFolder(ctx context.Context, folderID int) error
	SyncInScope(ctx context.Context, folderID int, scopePath string) error
}

// NewScanner creates a new Scanner with the given dependencies.
func NewScanner(fileRepo *FileRepository, ffprobePath string, s3Client *s3client.Client, workers int, emptyTrashAfterScan bool) *Scanner {
	if workers < 1 {
		workers = 8
	}
	return &Scanner{
		fileRepo:            fileRepo,
		rootSnapshotRepo:    NewScannedRootRepository(fileRepo.Pool()),
		groupSnapshotRepo:   NewScannedGroupRepository(fileRepo.Pool()),
		rootOverrideRepo:    NewMediaRootOverrideRepository(fileRepo.Pool()),
		groupOverrideRepo:   NewMediaGroupOverrideRepository(fileRepo.Pool()),
		locationRepo:        NewObservedLocationRepository(fileRepo.Pool()),
		groupLocationRepo:   NewGroupLocationRepository(fileRepo.Pool()),
		folderRepo:          catalog.NewFolderRepository(fileRepo.Pool()),
		libraryRepo:         catalog.NewLibraryItemRepository(fileRepo.Pool()),
		episodeLibraryRepo:  catalog.NewEpisodeLibraryRepository(fileRepo.Pool()),
		itemRepo:            catalog.NewItemRepository(fileRepo.Pool()),
		personRepo:          catalog.NewPersonRepository(fileRepo.Pool()),
		episodeRepo:         catalog.NewEpisodeRepository(fileRepo.Pool()),
		ffprobePath:         ffprobePath,
		s3Client:            s3Client,
		workers:             workers,
		emptyTrashAfterScan: emptyTrashAfterScan,
		markerFetcher:       nil,
	}
}

// SetSeriesQueueSyncer installs the optional pending-series root queue synchronizer.
func (s *Scanner) SetSeriesQueueSyncer(syncer SeriesQueueSyncer) {
	if s == nil {
		return
	}
	s.seriesQueueSyncer = syncer
}

// SetMovieQueueSyncer installs the optional pending movie-file match queue synchronizer.
func (s *Scanner) SetMovieQueueSyncer(syncer MovieQueueSyncer) {
	if s == nil {
		return
	}
	s.movieQueueSyncer = syncer
}

// SetMetadataQueueProducer installs the optional inline metadata queue producer.
func (s *Scanner) SetMetadataQueueProducer(producer MetadataQueueProducer) {
	if s == nil {
		return
	}
	s.metadataQueue = producer
}

// ScanFolder walks a media folder's directory tree, discovers media files,
// probes them for technical data, and upserts them into the database.
// Files previously in the DB that no longer exist on disk are marked as missing.
//
// Audiobook libraries are handled by ScanAudiobookFolder and podcast
// libraries by ScanPodcastFolder; both bypass the per-file movie/TV
// pipeline entirely.
func (s *Scanner) ScanFolder(ctx context.Context, folder *models.MediaFolder) (*ScanResult, error) {
	watchCtx, stopWatch := s.watchFolderContext(ctx, folder.ID)
	defer stopWatch()

	if isAudiobookLibraryType(folder.Type) {
		if err := s.ScanAudiobookFolder(watchCtx, folder); err != nil {
			return nil, err
		}
		if err := s.syncFolderScopedAudioLibraryState(watchCtx, folder.ID); err != nil {
			return nil, err
		}
		return &ScanResult{}, nil
	}

	if isPodcastLibraryType(folder.Type) {
		if err := s.ScanPodcastFolder(watchCtx, folder); err != nil {
			return nil, err
		}
		if err := s.syncFolderScopedAudioLibraryState(watchCtx, folder.ID); err != nil {
			return nil, err
		}
		return &ScanResult{}, nil
	}

	return s.scanPaths(watchCtx, folder, folder.Paths, folder.Paths, true)
}

// ScanSubtree walks a single subtree within a media folder and reconciles only
// files that live beneath that subtree.
func (s *Scanner) ScanSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*ScanResult, error) {
	cleanSubtree := filepath.Clean(subtreePath)
	watchCtx, stopWatch := s.watchFolderContext(ctx, folder.ID)
	defer stopWatch()
	return s.scanPaths(watchCtx, folder, []string{cleanSubtree}, []string{cleanSubtree}, false)
}

func isMovieLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "movie", "movies":
		return true
	default:
		return false
	}
}
func isAudiobookLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "audiobook", "audiobooks":
		return true
	default:
		return false
	}
}
func isPodcastLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "podcast", "podcasts":
		return true
	default:
		return false
	}
}

func isEbookLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "ebook", "ebooks":
		return true
	default:
		return false
	}
}

// walkMode tells walkLogicalTree which file extensions to surface and
// which library-specific filename heuristics (sample/extra skipping)
// to apply.
type walkMode int

const (
	walkModeVideo     walkMode = iota // bare video walk: video extensions, no movie skipping
	walkModeMovie                     // movie library: video extensions + sample/extra skipping
	walkModeAudiobook                 // audiobook library: audio extensions, no skipping
	walkModePodcast                   // podcast library: audio extensions, no skipping
	walkModeEbook                     // ebook library: ebook extensions, no skipping
)

// walkModeFor derives a walkMode from a media_folders.type string.
// Unknown types default to walkModeVideo so existing call sites that
// pass arbitrary types preserve their prior behavior.
func walkModeFor(folderType string) walkMode {
	switch {
	case isMovieLibraryType(folderType):
		return walkModeMovie
	case isAudiobookLibraryType(folderType):
		return walkModeAudiobook
	case isPodcastLibraryType(folderType):
		return walkModePodcast
	case isEbookLibraryType(folderType):
		return walkModeEbook
	default:
		return walkModeVideo
	}
}

// acceptsExt reports whether the given lowercased extension belongs to
// the file types this walk mode is looking for.
func (m walkMode) acceptsExt(ext string) bool {
	switch m {
	case walkModeAudiobook, walkModePodcast:
		return audioExtensions[ext]
	case walkModeEbook:
		return ebookExtensions[ext]
	default:
		return videoExtensions[ext]
	}
}

func canonicalWalkPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(resolved)
	if err == nil {
		resolved = abs
	}
	return filepath.Clean(resolved), nil
}

func isIgnoredDirectoryPath(path string) bool {
	return ignoredDirNames[strings.ToLower(filepath.Base(path))]
}

func walkLogicalTree(
	ctx context.Context,
	logicalPath string,
	physicalPath string,
	mode walkMode,
	visitedPhysicalDirs map[string]struct{},
	filePaths *[]string,
) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	info, err := os.Lstat(physicalPath)
	if err != nil {
		slog.Warn("scanner: walk lstat failed", "path", logicalPath, "physical_path", physicalPath, "error", err)
		return nil
	}

	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(physicalPath)
		if err != nil {
			slog.Warn("scanner: symlink resolve failed", "path", logicalPath, "physical_path", physicalPath, "error", err)
			return nil
		}
		targetInfo, err := os.Stat(resolved)
		if err != nil {
			slog.Warn("scanner: symlink stat failed", "path", logicalPath, "resolved_path", resolved, "error", err)
			return nil
		}
		if targetInfo.IsDir() {
			return walkLogicalTree(ctx, logicalPath, resolved, mode, visitedPhysicalDirs, filePaths)
		}
		if mode == walkModeMovie && shouldSkipMovieSupplementalFile(logicalPath) {
			return nil
		}
		if mode.acceptsExt(strings.ToLower(filepath.Ext(logicalPath))) {
			*filePaths = append(*filePaths, logicalPath)
		}
		return nil
	}

	if !info.IsDir() {
		if mode == walkModeMovie && shouldSkipMovieSupplementalFile(logicalPath) {
			return nil
		}
		if mode.acceptsExt(strings.ToLower(filepath.Ext(logicalPath))) {
			*filePaths = append(*filePaths, logicalPath)
		}
		return nil
	}

	canonicalDir, err := canonicalWalkPath(physicalPath)
	if err != nil {
		slog.Warn("scanner: canonical path resolution failed", "path", logicalPath, "physical_path", physicalPath, "error", err)
		return nil
	}
	if _, seen := visitedPhysicalDirs[canonicalDir]; seen {
		return nil
	}
	visitedPhysicalDirs[canonicalDir] = struct{}{}

	if isIgnoredDirectoryPath(logicalPath) {
		return nil
	}
	if mode == walkModeMovie && shouldSkipMovieSupplementalDir(logicalPath) {
		return nil
	}

	entries, err := os.ReadDir(physicalPath)
	if err != nil {
		slog.Warn("scanner: directory read failed", "path", logicalPath, "physical_path", physicalPath, "error", err)
		return nil
	}
	for _, entry := range entries {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		logicalChild := filepath.Join(logicalPath, entry.Name())
		physicalChild := filepath.Join(physicalPath, entry.Name())

		if entry.Type()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(physicalChild)
			if err != nil {
				slog.Warn("scanner: symlink resolve failed", "path", logicalChild, "physical_path", physicalChild, "error", err)
				continue
			}
			targetInfo, err := os.Stat(resolved)
			if err != nil {
				slog.Warn("scanner: symlink stat failed", "path", logicalChild, "resolved_path", resolved, "error", err)
				continue
			}
			if targetInfo.IsDir() {
				if err := walkLogicalTree(ctx, logicalChild, resolved, mode, visitedPhysicalDirs, filePaths); err != nil {
					return err
				}
				continue
			}
			if mode == walkModeMovie && shouldSkipMovieSupplementalFile(logicalChild) {
				continue
			}
			if mode.acceptsExt(strings.ToLower(filepath.Ext(entry.Name()))) {
				*filePaths = append(*filePaths, logicalChild)
			}
			continue
		}

		if entry.IsDir() {
			if err := walkLogicalTree(ctx, logicalChild, physicalChild, mode, visitedPhysicalDirs, filePaths); err != nil {
				return err
			}
			continue
		}

		if mode == walkModeMovie && shouldSkipMovieSupplementalFile(logicalChild) {
			continue
		}
		if mode.acceptsExt(strings.ToLower(filepath.Ext(entry.Name()))) {
			*filePaths = append(*filePaths, logicalChild)
		}
	}

	return nil
}

func collectLogicalFilePaths(ctx context.Context, walkRoots []string, libraryType string) ([]string, error) {
	filePaths := make([]string, 0)
	visitedPhysicalDirs := make(map[string]struct{})
	mode := walkModeFor(libraryType)

	for _, rootPath := range walkRoots {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		cleanRoot := filepath.Clean(rootPath)
		if cleanRoot == "" || cleanRoot == "." {
			continue
		}
		if err := walkLogicalTree(ctx, cleanRoot, cleanRoot, mode, visitedPhysicalDirs, &filePaths); err != nil {
			return nil, err
		}
	}

	return filePaths, nil
}

func (s *Scanner) scanPaths(
	ctx context.Context,
	folder *models.MediaFolder,
	walkRoots []string,
	reconcileRoots []string,
	allowEmptyRootGuard bool,
) (*ScanResult, error) {
	if err := s.ensureFolderEnabled(ctx, folder.ID); err != nil {
		return nil, err
	}
	if allowEmptyRootGuard {
		return s.scanFolderByRoots(ctx, folder, walkRoots, reconcileRoots)
	}

	result := &ScanResult{}

	// Get existing files in this scan scope from the DB.
	var (
		existingFiles []*scanStateFile
		err           error
	)
	if allowEmptyRootGuard {
		existingFiles, err = s.fileRepo.GetScanStateByFolder(ctx, folder.ID)
		if err != nil {
			return nil, fmt.Errorf("getting existing files for folder %d: %w", folder.ID, err)
		}
	} else {
		existingFiles, err = s.fileRepo.GetScanStateByFolderAndPathPrefix(ctx, folder.ID, reconcileRoots[0])
		if err != nil {
			return nil, fmt.Errorf("getting existing files for folder %d path %q: %w", folder.ID, reconcileRoots[0], err)
		}
	}

	// Build a set of existing file paths for quick lookup.
	existingByPath := make(map[string]*scanStateFile, len(existingFiles))
	for _, f := range existingFiles {
		existingByPath[f.FilePath] = f
	}
	existingContentStatuses, err := s.itemRepo.GetStatusByIDs(ctx, collectScanStateContentIDs(existingFiles))
	if err != nil {
		return nil, fmt.Errorf("loading item statuses for folder %d: %w", folder.ID, err)
	}

	// Phase 1: Collect all media file paths.
	reportProgress(ctx, ProgressUpdate{
		Phase:        "walking",
		Message:      "Discovering media files",
		CurrentScope: firstScope(reconcileRoots),
	})
	filePaths, walkErr := collectLogicalFilePaths(ctx, walkRoots, folder.Type)
	if walkErr != nil {
		return nil, fmt.Errorf("walking media roots: %w", walkErr)
	}
	slog.Info("scanner: discovered files",
		"folder_id", folder.ID,
		"scope", firstScope(reconcileRoots),
		"files", len(filePaths),
	)
	reportProgress(ctx, ProgressUpdate{
		Phase:           "processing",
		Message:         "Processing discovered files",
		CurrentScope:    firstScope(reconcileRoots),
		TotalFiles:      len(filePaths),
		FilesDiscovered: len(filePaths),
	})

	// Track which paths we see on disk so we can detect missing files.
	seenPaths := make(map[string]bool, len(filePaths))
	for _, p := range filePaths {
		seenPaths[p] = true
	}
	rootOverrides, err := s.loadRootOverrides(ctx, folder.ID, reconcileRoots)
	if err != nil {
		return nil, fmt.Errorf("loading root overrides: %w", err)
	}
	rootInference := inferRootAssignments(filePaths, folder.Type, folder.ID, rootOverrides)
	groupInference := inferGroupAssignments(filePaths, folder.Type, folder.ID, rootInference.Assignments)
	groupOverrides, err := s.loadGroupOverrides(ctx, folder.ID)
	if err != nil {
		return nil, fmt.Errorf("loading group overrides: %w", err)
	}
	applyGroupOverrides(&groupInference, groupOverrides)
	result.RootObservations = rootInference.Observations
	s.logRootInferenceDisagreements(rootInference.Assignments)
	if err := s.reconcileScannedRoots(
		ctx,
		folder.ID,
		reconcileRoots,
		rootInference.Snapshots,
	); err != nil {
		return nil, fmt.Errorf("reconciling scanned roots: %w", err)
	}
	if _, err := s.clearLegacyLinksForUnmatchableRoots(ctx, folder.ID, result.RootObservations); err != nil {
		return nil, fmt.Errorf("clearing legacy links for unmatchable roots: %w", err)
	}

	allowEmptyCleanup := false
	if allowEmptyRootGuard && len(filePaths) == 0 && len(existingFiles) > 0 {
		var err error
		allowEmptyCleanup, err = s.folderRepo.ConsumeEmptyCleanupAllowance(ctx, folder.ID)
		if err != nil {
			return nil, fmt.Errorf("checking empty cleanup confirmation for folder %d: %w", folder.ID, err)
		}
		if !allowEmptyCleanup {
			result.EmptyRootGuarded = true
			if err := s.folderRepo.SetScanWarning(ctx, folder.ID,
				"empty_root",
				"Scan found 0 media files; cleanup was skipped until deletion is confirmed.",
				time.Now().UTC(),
			); err != nil {
				return nil, fmt.Errorf("recording empty-root warning for folder %d: %w", folder.ID, err)
			}
			return result, nil
		}
	}

	// Phase 2: Process files concurrently with a worker pool.
	var wg sync.WaitGroup
	pathCh := make(chan string, s.workers)
	var newCount, updatedCount, unchangedCount, errorCount, processedCount atomic.Int64
	subtitleCache := newExternalSubtitleDirCache()

	for range s.workers {
		wg.Go(func() {
			for path := range pathCh {
				if ctx.Err() != nil {
					continue // drain channel
				}
				action, updateReasons, processErr := s.processFile(ctx, path, folder, existingByPath, existingContentStatuses, rootInference.Assignments[path], groupInference.Assignments[path], subtitleCache)
				if processErr != nil {
					slog.Error("scanner: file processing failed", "path", path, "error", processErr)
					errorCount.Add(1)
					continue
				}
				switch action {
				case actionNew:
					newCount.Add(1)
					slog.Debug("scanner: new file added", "path", path)
				case actionUpdated:
					updatedCount.Add(1)
					slog.Debug("scanner: file updated", "path", path, "reasons", updateReasons)
				case actionUnchanged:
					unchangedCount.Add(1)
				}
				processedCount.Add(1)
			}
		})
	}

	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(scanProgressLogInterval)
		defer ticker.Stop()
		defer close(progressDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopProgress:
				return
			case <-ticker.C:
				processed := int(processedCount.Load())
				total := len(filePaths)
				slog.Info("scanner: processing progress",
					"folder_id", folder.ID,
					"scope", firstScope(reconcileRoots),
					"processed", processed,
					"total", total,
					"new", newCount.Load(),
					"updated", updatedCount.Load(),
					"unchanged", unchangedCount.Load(),
					"errors", errorCount.Load(),
				)
				reportProgress(ctx, ProgressUpdate{
					Phase:           "processing",
					Message:         "Processing discovered files",
					CurrentScope:    firstScope(reconcileRoots),
					TotalFiles:      total,
					FilesDiscovered: total,
					FilesProcessed:  processed,
					New:             int(newCount.Load()),
					Updated:         int(updatedCount.Load()),
					Unchanged:       int(unchangedCount.Load()),
					Errors:          int(errorCount.Load()),
				})
			}
		}
	}()

	for _, p := range filePaths {
		pathCh <- p
	}
	close(pathCh)
	wg.Wait()
	close(stopProgress)
	<-progressDone

	result.New = int(newCount.Load())
	result.Updated = int(updatedCount.Load())
	result.Unchanged = int(unchangedCount.Load())
	result.Errors = int(errorCount.Load())
	reportProgress(ctx, ProgressUpdate{
		Phase:           "reconciling",
		Message:         "Reconciling scan state",
		CurrentScope:    firstScope(reconcileRoots),
		TotalFiles:      len(filePaths),
		FilesDiscovered: len(filePaths),
		FilesProcessed:  int(processedCount.Load()),
		New:             result.New,
		Updated:         result.Updated,
		Unchanged:       result.Unchanged,
		Errors:          result.Errors,
	})
	if walkErr != nil {
		return nil, walkErr
	}

	// If the scan was cancelled, return partial results without marking
	// files as missing or deleting records — that would corrupt state.
	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	if err := s.syncPresentLibraryState(ctx, folder.ID); err != nil {
		return nil, fmt.Errorf("syncing present library state for folder %d: %w", folder.ID, err)
	}

	if err := s.reconcileScannedGroups(ctx, folder.ID, allowEmptyRootGuard, reconcileRoots, !allowEmptyRootGuard, groupInference); err != nil {
		return nil, fmt.Errorf("reconciling scanned groups: %w", err)
	}

	// Mark files that were in the DB but not found on disk as missing.
	now := time.Now().UTC()
	for _, existing := range existingFiles {
		if seenPaths[existing.FilePath] {
			continue
		}
		// Only mark as missing if not already marked.
		if existing.MissingSince == nil {
			if err := s.fileRepo.MarkMissing(ctx, existing.ID, now); err != nil {
				slog.Error("scanner: failed to mark file missing",
					"path", existing.FilePath,
					"error", err,
				)
				result.Errors++
				continue
			}
		}
		result.Missing++
	}

	// Empty trash: delete all files marked as missing for this folder.
	// Safe because the empty-root guard (above) returns early when 0 files
	// are found on disk, so we only reach here when the root is populated.
	if s.emptyTrashAfterScan {
		trashed, err := s.fileRepo.DeleteMissingByFolder(ctx, folder.ID)
		if err != nil {
			return nil, fmt.Errorf("emptying trash for folder %d: %w", folder.ID, err)
		}
		if trashed > 0 {
			slog.Info("scanner: emptied trash", "folder_id", folder.ID, "deleted", trashed)
		}
		result.FilesDeleted += trashed
	}

	staleFileIDs := collectStaleRemovedPathFileIDs(existingFiles, seenPaths, reconcileRoots)
	if len(filePaths) == 0 && allowEmptyCleanup {
		staleFileIDs = make([]int, 0, len(existingFiles))
		for _, existing := range existingFiles {
			staleFileIDs = append(staleFileIDs, existing.ID)
		}
	}
	deletedFiles, err := s.fileRepo.DeleteByIDs(ctx, staleFileIDs)
	if err != nil {
		return nil, fmt.Errorf("deleting stale files for folder %d: %w", folder.ID, err)
	}
	result.FilesDeleted += deletedFiles

	removedMemberships, deletedItems, orphanedImageDirs, err := s.reconcileLibraryMemberships(ctx, folder.ID)
	if err != nil {
		return nil, fmt.Errorf("reconciling library membership for folder %d: %w", folder.ID, err)
	}
	result.MembershipsRemoved = removedMemberships
	result.ItemsDeleted = deletedItems

	if s.seriesQueueSyncer != nil {
		if allowEmptyRootGuard {
			if err := s.seriesQueueSyncer.SyncForFolder(ctx, folder.ID); err != nil {
				return nil, fmt.Errorf("syncing series match queue for folder %d: %w", folder.ID, err)
			}
			slog.Info("metadata: series root queue sync",
				"folder_id", folder.ID,
				"scope", "folder",
			)
		} else if len(reconcileRoots) > 0 {
			for _, scopePath := range reconcileRoots {
				if err := s.seriesQueueSyncer.SyncInScope(ctx, folder.ID, scopePath); err != nil {
					return nil, fmt.Errorf("syncing series match queue for scope %q: %w", scopePath, err)
				}
				slog.Info("metadata: series root queue sync",
					"folder_id", folder.ID,
					"scope", scopePath,
				)
			}
		}
	}
	if s.movieQueueSyncer != nil {
		if allowEmptyRootGuard {
			if err := s.movieQueueSyncer.SyncForFolder(ctx, folder.ID); err != nil {
				return nil, fmt.Errorf("syncing movie match queue for folder %d: %w", folder.ID, err)
			}
			slog.Info("metadata: movie file queue sync",
				"folder_id", folder.ID,
				"scope", "folder",
			)
		} else if len(reconcileRoots) > 0 {
			for _, scopePath := range reconcileRoots {
				if err := s.movieQueueSyncer.SyncInScope(ctx, folder.ID, scopePath); err != nil {
					return nil, fmt.Errorf("syncing movie match queue for scope %q: %w", scopePath, err)
				}
				slog.Info("metadata: movie file queue sync",
					"folder_id", folder.ID,
					"scope", scopePath,
				)
			}
		}
	}

	// Best-effort S3 image cleanup for orphaned items.
	if s.s3Client != nil && len(orphanedImageDirs) > 0 {
		bucket := s.s3Client.Bucket()
		for _, dir := range orphanedImageDirs {
			_, _ = s.s3Client.DeletePrefix(ctx, bucket, dir)
		}
	}

	if allowEmptyRootGuard && (len(filePaths) > 0 || allowEmptyCleanup) {
		if err := s.folderRepo.ClearScanWarning(ctx, folder.ID); err != nil {
			return nil, fmt.Errorf("clearing scan warning for folder %d: %w", folder.ID, err)
		}
	}

	return result, nil
}

type scopedScan struct {
	walkRoots      []string
	reconcileRoots []string
	existingFiles  []*scanStateFile
	filePaths      []string
	seenPaths      map[string]bool
	rootInference  rootInferenceResult
	groupInference groupInferenceResult
	result         *ScanResult
}

func (s *Scanner) scanFolderByRoots(
	ctx context.Context,
	folder *models.MediaFolder,
	walkRoots []string,
	reconcileRoots []string,
) (*ScanResult, error) {
	result := &ScanResult{}
	roots := compactScanRoots(reconcileRoots)
	if len(roots) == 0 {
		roots = compactScanRoots(walkRoots)
	}

	pendingEmptyScopes := make([]*scopedScan, 0)
	totalExisting := 0
	seenAnyFiles := false
	allowEmptyCleanup := false

	for _, root := range roots {
		reportProgress(ctx, ProgressUpdate{
			Phase:        "walking",
			Message:      "Scanning library root",
			CurrentScope: root,
		})
		scope, err := s.scanScope(ctx, folder, []string{root}, []string{root})
		if err != nil {
			return nil, err
		}
		totalExisting += len(scope.existingFiles)
		mergeScanResult(result, scope.result)

		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		if len(scope.filePaths) > 0 {
			seenAnyFiles = true
			for _, pending := range pendingEmptyScopes {
				beforeErrors := pending.result.Errors
				if err := s.applyScopedScan(ctx, folder, pending, false); err != nil {
					return nil, err
				}
				mergeCleanupResult(result, pending.result, beforeErrors)
			}
			pendingEmptyScopes = pendingEmptyScopes[:0]

			beforeErrors := scope.result.Errors
			if err := s.applyScopedScan(ctx, folder, scope, false); err != nil {
				return nil, err
			}
			mergeCleanupResult(result, scope.result, beforeErrors)
			continue
		}

		if seenAnyFiles {
			beforeErrors := scope.result.Errors
			if err := s.applyScopedScan(ctx, folder, scope, false); err != nil {
				return nil, err
			}
			mergeCleanupResult(result, scope.result, beforeErrors)
			continue
		}

		pendingEmptyScopes = append(pendingEmptyScopes, scope)
	}

	if !seenAnyFiles && totalExisting > 0 {
		var err error
		allowEmptyCleanup, err = s.folderRepo.ConsumeEmptyCleanupAllowance(ctx, folder.ID)
		if err != nil {
			return nil, fmt.Errorf("checking empty cleanup confirmation for folder %d: %w", folder.ID, err)
		}
		if !allowEmptyCleanup {
			result.EmptyRootGuarded = true
			if err := s.folderRepo.SetScanWarning(ctx, folder.ID,
				"empty_root",
				"Scan found 0 media files; cleanup was skipped until deletion is confirmed.",
				time.Now().UTC(),
			); err != nil {
				return nil, fmt.Errorf("recording empty-root warning for folder %d: %w", folder.ID, err)
			}
			return result, nil
		}
	}

	for _, pending := range pendingEmptyScopes {
		beforeErrors := pending.result.Errors
		if err := s.applyScopedScan(ctx, folder, pending, allowEmptyCleanup); err != nil {
			return nil, err
		}
		mergeCleanupResult(result, pending.result, beforeErrors)
	}

	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	reportProgress(ctx, ProgressUpdate{
		Phase:        "reconciling",
		Message:      "Reconciling library state",
		CurrentScope: "folder",
	})
	if err := s.syncPresentLibraryState(ctx, folder.ID); err != nil {
		return nil, fmt.Errorf("syncing present library state for folder %d: %w", folder.ID, err)
	}

	if s.emptyTrashAfterScan {
		trashed, err := s.fileRepo.DeleteMissingByFolder(ctx, folder.ID)
		if err != nil {
			return nil, fmt.Errorf("emptying trash for folder %d: %w", folder.ID, err)
		}
		if trashed > 0 {
			slog.Info("scanner: emptied trash", "folder_id", folder.ID, "deleted", trashed)
		}
		result.FilesDeleted += trashed
	}

	staleOutsideRoots, err := s.fileRepo.ListIDsOutsideRoots(ctx, folder.ID, roots)
	if err != nil {
		return nil, fmt.Errorf("listing stale files outside configured roots for folder %d: %w", folder.ID, err)
	}
	deletedOutsideRoots, err := s.fileRepo.DeleteByIDs(ctx, staleOutsideRoots)
	if err != nil {
		return nil, fmt.Errorf("deleting stale files outside configured roots for folder %d: %w", folder.ID, err)
	}
	result.FilesDeleted += deletedOutsideRoots

	removedMemberships, deletedItems, orphanedImageDirs, err := s.reconcileLibraryMemberships(ctx, folder.ID)
	if err != nil {
		return nil, fmt.Errorf("reconciling library membership for folder %d: %w", folder.ID, err)
	}
	result.MembershipsRemoved = removedMemberships
	result.ItemsDeleted = deletedItems

	if s.seriesQueueSyncer != nil {
		reportProgress(ctx, ProgressUpdate{
			Phase:        "queue_sync",
			Message:      "Syncing series match queue",
			CurrentScope: "folder",
		})
		if err := s.seriesQueueSyncer.SyncForFolder(ctx, folder.ID); err != nil {
			return nil, fmt.Errorf("syncing series match queue for folder %d: %w", folder.ID, err)
		}
		slog.Info("metadata: series root queue sync",
			"folder_id", folder.ID,
			"scope", "folder",
		)
	}
	if s.movieQueueSyncer != nil {
		reportProgress(ctx, ProgressUpdate{
			Phase:        "queue_sync",
			Message:      "Syncing movie match queue",
			CurrentScope: "folder",
		})
		if err := s.movieQueueSyncer.SyncForFolder(ctx, folder.ID); err != nil {
			return nil, fmt.Errorf("syncing movie match queue for folder %d: %w", folder.ID, err)
		}
		slog.Info("metadata: movie file queue sync",
			"folder_id", folder.ID,
			"scope", "folder",
		)
	}

	if s.s3Client != nil && len(orphanedImageDirs) > 0 {
		bucket := s.s3Client.Bucket()
		for _, dir := range orphanedImageDirs {
			_, _ = s.s3Client.DeletePrefix(ctx, bucket, dir)
		}
	}

	if seenAnyFiles || allowEmptyCleanup {
		if err := s.folderRepo.ClearScanWarning(ctx, folder.ID); err != nil {
			return nil, fmt.Errorf("clearing scan warning for folder %d: %w", folder.ID, err)
		}
	}

	return result, nil
}

func (s *Scanner) scanScope(
	ctx context.Context,
	folder *models.MediaFolder,
	walkRoots []string,
	reconcileRoots []string,
) (*scopedScan, error) {
	result := &ScanResult{}
	existingFiles, err := s.fileRepo.GetScanStateByFolderAndPathPrefix(ctx, folder.ID, reconcileRoots[0])
	if err != nil {
		return nil, fmt.Errorf("getting existing files for folder %d path %q: %w", folder.ID, reconcileRoots[0], err)
	}

	existingByPath := make(map[string]*scanStateFile, len(existingFiles))
	for _, f := range existingFiles {
		existingByPath[f.FilePath] = f
	}
	existingContentStatuses, err := s.itemRepo.GetStatusByIDs(ctx, collectScanStateContentIDs(existingFiles))
	if err != nil {
		return nil, fmt.Errorf("loading item statuses for folder %d path %q: %w", folder.ID, reconcileRoots[0], err)
	}

	filePaths, walkErr := collectLogicalFilePaths(ctx, walkRoots, folder.Type)
	if walkErr != nil {
		return nil, fmt.Errorf("walking media roots for %q: %w", reconcileRoots[0], walkErr)
	}
	slog.Info("scanner: discovered files",
		"folder_id", folder.ID,
		"scope", firstScope(reconcileRoots),
		"files", len(filePaths),
	)
	reportProgress(ctx, ProgressUpdate{
		Phase:           "processing",
		Message:         "Processing discovered files",
		CurrentScope:    firstScope(reconcileRoots),
		TotalFiles:      len(filePaths),
		FilesDiscovered: len(filePaths),
	})

	seenPaths := make(map[string]bool, len(filePaths))
	for _, p := range filePaths {
		seenPaths[p] = true
	}
	rootOverrides, err := s.loadRootOverrides(ctx, folder.ID, reconcileRoots)
	if err != nil {
		return nil, fmt.Errorf("loading root overrides: %w", err)
	}
	rootInference := inferRootAssignments(filePaths, folder.Type, folder.ID, rootOverrides)
	groupInference := inferGroupAssignments(filePaths, folder.Type, folder.ID, rootInference.Assignments)
	groupOverrides, err := s.loadGroupOverrides(ctx, folder.ID)
	if err != nil {
		return nil, fmt.Errorf("loading group overrides: %w", err)
	}
	applyGroupOverrides(&groupInference, groupOverrides)
	result.RootObservations = append(result.RootObservations, rootInference.Observations...)
	s.logRootInferenceDisagreements(rootInference.Assignments)
	if _, err := s.clearLegacyLinksForUnmatchableRoots(ctx, folder.ID, result.RootObservations); err != nil {
		return nil, fmt.Errorf("clearing legacy links for unmatchable roots: %w", err)
	}

	var wg sync.WaitGroup
	pathCh := make(chan string, s.workers)
	var newCount, updatedCount, unchangedCount, errorCount, processedCount atomic.Int64
	subtitleCache := newExternalSubtitleDirCache()

	for range s.workers {
		wg.Go(func() {
			for path := range pathCh {
				if ctx.Err() != nil {
					continue
				}
				action, updateReasons, processErr := s.processFile(ctx, path, folder, existingByPath, existingContentStatuses, rootInference.Assignments[path], groupInference.Assignments[path], subtitleCache)
				if processErr != nil {
					slog.Error("scanner: file processing failed", "path", path, "error", processErr)
					errorCount.Add(1)
					continue
				}
				switch action {
				case actionNew:
					newCount.Add(1)
					slog.Debug("scanner: new file added", "path", path)
				case actionUpdated:
					updatedCount.Add(1)
					slog.Debug("scanner: file updated", "path", path, "reasons", updateReasons)
				case actionUnchanged:
					unchangedCount.Add(1)
				}
				processedCount.Add(1)
			}
		})
	}

	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(scanProgressLogInterval)
		defer ticker.Stop()
		defer close(progressDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopProgress:
				return
			case <-ticker.C:
				processed := int(processedCount.Load())
				total := len(filePaths)
				slog.Info("scanner: processing progress",
					"folder_id", folder.ID,
					"scope", firstScope(reconcileRoots),
					"processed", processed,
					"total", total,
					"new", newCount.Load(),
					"updated", updatedCount.Load(),
					"unchanged", unchangedCount.Load(),
					"errors", errorCount.Load(),
				)
				reportProgress(ctx, ProgressUpdate{
					Phase:           "processing",
					Message:         "Processing discovered files",
					CurrentScope:    firstScope(reconcileRoots),
					TotalFiles:      total,
					FilesDiscovered: total,
					FilesProcessed:  processed,
					New:             int(newCount.Load()),
					Updated:         int(updatedCount.Load()),
					Unchanged:       int(unchangedCount.Load()),
					Errors:          int(errorCount.Load()),
				})
			}
		}
	}()

	for _, p := range filePaths {
		pathCh <- p
	}
	close(pathCh)
	wg.Wait()
	close(stopProgress)
	<-progressDone

	result.New = int(newCount.Load())
	result.Updated = int(updatedCount.Load())
	result.Unchanged = int(unchangedCount.Load())
	result.Errors = int(errorCount.Load())
	reportProgress(ctx, ProgressUpdate{
		Phase:           "reconciling",
		Message:         "Reconciling scan state",
		CurrentScope:    firstScope(reconcileRoots),
		TotalFiles:      len(filePaths),
		FilesDiscovered: len(filePaths),
		FilesProcessed:  int(processedCount.Load()),
		New:             result.New,
		Updated:         result.Updated,
		Unchanged:       result.Unchanged,
		Errors:          result.Errors,
	})

	return &scopedScan{
		walkRoots:      append([]string(nil), walkRoots...),
		reconcileRoots: append([]string(nil), reconcileRoots...),
		existingFiles:  existingFiles,
		filePaths:      filePaths,
		seenPaths:      seenPaths,
		rootInference:  rootInference,
		groupInference: groupInference,
		result:         result,
	}, nil
}

func (s *Scanner) applyScopedScan(
	ctx context.Context,
	folder *models.MediaFolder,
	scope *scopedScan,
	forceDeleteAll bool,
) error {
	if scope == nil || scope.result == nil {
		return nil
	}

	if err := s.reconcileScannedRoots(
		ctx,
		folder.ID,
		scope.reconcileRoots,
		scope.rootInference.Snapshots,
	); err != nil {
		return fmt.Errorf("reconciling scanned roots for scope %q: %w", scope.reconcileRoots[0], err)
	}
	if err := s.reconcileScannedGroups(ctx, folder.ID, false, scope.reconcileRoots, true, scope.groupInference); err != nil {
		return fmt.Errorf("reconciling scanned groups for scope %q: %w", scope.reconcileRoots[0], err)
	}

	now := time.Now().UTC()
	for _, existing := range scope.existingFiles {
		if scope.seenPaths[existing.FilePath] {
			continue
		}
		if existing.MissingSince == nil {
			if err := s.fileRepo.MarkMissing(ctx, existing.ID, now); err != nil {
				slog.Error("scanner: failed to mark file missing",
					"path", existing.FilePath,
					"error", err,
				)
				scope.result.Errors++
				continue
			}
		}
		scope.result.Missing++
	}

	staleFileIDs := collectStaleRemovedPathFileIDs(scope.existingFiles, scope.seenPaths, scope.reconcileRoots)
	if forceDeleteAll && len(scope.filePaths) == 0 {
		staleFileIDs = make([]int, 0, len(scope.existingFiles))
		for _, existing := range scope.existingFiles {
			staleFileIDs = append(staleFileIDs, existing.ID)
		}
	}
	deletedFiles, err := s.fileRepo.DeleteByIDs(ctx, staleFileIDs)
	if err != nil {
		return fmt.Errorf("deleting stale files for scope %q: %w", scope.reconcileRoots[0], err)
	}
	scope.result.FilesDeleted += deletedFiles

	return nil
}

func mergeScanResult(dst *ScanResult, src *ScanResult) {
	if dst == nil || src == nil {
		return
	}
	dst.New += src.New
	dst.Updated += src.Updated
	dst.Unchanged += src.Unchanged
	dst.Errors += src.Errors
	dst.RootObservations = append(dst.RootObservations, src.RootObservations...)
	dst.EmptyRootGuarded = dst.EmptyRootGuarded || src.EmptyRootGuarded
}

func firstScope(scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	return scopes[0]
}

func mergeCleanupResult(dst *ScanResult, src *ScanResult, priorErrors int) {
	if dst == nil || src == nil {
		return
	}
	dst.Missing += src.Missing
	dst.FilesDeleted += src.FilesDeleted
	if src.Errors > priorErrors {
		dst.Errors += src.Errors - priorErrors
	}
}

func compactScanRoots(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, rawPath := range paths {
		if strings.TrimSpace(rawPath) == "" {
			continue
		}
		path := filepath.Clean(rawPath)
		if path == "" || path == "." {
			continue
		}
		if pathWithinAnyRoot(path, out) {
			continue
		}
		filtered := out[:0]
		for _, existing := range out {
			if pathWithinAnyRoot(existing, []string{path}) {
				continue
			}
			filtered = append(filtered, existing)
		}
		out = append(filtered, path)
	}
	return out
}

func (s *Scanner) syncPresentLibraryState(ctx context.Context, folderID int) error {
	if _, err := s.fileRepo.Pool().Exec(ctx, `
		UPDATE media_files mf
		SET content_id = NULL,
			updated_at = NOW()
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.content_id IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1
			FROM media_items mi
			WHERE mi.content_id = mf.content_id
		  )
	`, folderID); err != nil {
		return fmt.Errorf("clearing dangling content links: %w", err)
	}

	if _, err := s.fileRepo.Pool().Exec(ctx, `
		UPDATE media_files mf
		SET episode_id = NULL,
			updated_at = NOW()
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.episode_id IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1
			FROM episodes e
			WHERE e.content_id = mf.episode_id
		  )
	`, folderID); err != nil {
		return fmt.Errorf("clearing dangling episode links: %w", err)
	}

	if _, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		SELECT DISTINCT mf.content_id, mf.media_folder_id, NOW()
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.content_id IS NOT NULL
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, folderID); err != nil {
		return fmt.Errorf("restoring folder memberships: %w", err)
	}

	if _, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
		SELECT mf.episode_id, mf.media_folder_id, MIN(mf.created_at)
		FROM media_files mf
		JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.episode_id IS NOT NULL
		GROUP BY mf.episode_id, mf.media_folder_id
		ON CONFLICT (episode_id, media_folder_id) DO NOTHING
	`, folderID); err != nil {
		return fmt.Errorf("restoring episode folder memberships: %w", err)
	}

	return nil
}

func (s *Scanner) syncFolderScopedAudioLibraryState(ctx context.Context, folderID int) error {
	if err := s.syncPresentLibraryState(ctx, folderID); err != nil {
		return err
	}

	if _, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id)
		SELECT DISTINCT mf.media_folder_id, mf.canonical_root_path, mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.content_id IS NOT NULL
		  AND COALESCE(mf.canonical_root_path, '') <> ''
		  AND mi.type IN ('audiobook', 'podcast')
		ON CONFLICT (media_folder_id, canonical_root_path)
		DO UPDATE SET content_id = EXCLUDED.content_id,
			last_seen_at = NOW()
	`, folderID); err != nil {
		return fmt.Errorf("restoring folder-scoped audio roots: %w", err)
	}

	return nil
}

func (s *Scanner) reconcileLibraryMemberships(ctx context.Context, folderID int) (int, int, []string, error) {
	if s.episodeLibraryRepo != nil {
		if _, err := s.episodeLibraryRepo.ReconcileFolderMembership(ctx, folderID); err != nil {
			return 0, 0, nil, err
		}
	}
	return s.libraryRepo.ReconcileFolderMembership(ctx, folderID)
}

func collectStaleRemovedPathFileIDs(existingFiles []*scanStateFile, seenPaths map[string]bool, roots []string) []int {
	ids := make([]int, 0)
	for _, existing := range existingFiles {
		if seenPaths[existing.FilePath] {
			continue
		}
		if pathWithinAnyRoot(existing.FilePath, roots) {
			continue
		}
		ids = append(ids, existing.ID)
	}
	return ids
}

func collectScanStateContentIDs(files []*scanStateFile) []string {
	contentIDs := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file == nil || strings.TrimSpace(file.ContentID) == "" {
			continue
		}
		if _, ok := seen[file.ContentID]; ok {
			continue
		}
		seen[file.ContentID] = struct{}{}
		contentIDs = append(contentIDs, file.ContentID)
	}
	return contentIDs
}

func pathWithinAnyRoot(path string, roots []string) bool {
	cleanPath := filepath.Clean(path)
	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		rel, err := filepath.Rel(cleanRoot, cleanPath)
		if err != nil {
			continue
		}
		if rel == "." || rel == "" {
			return true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ScanFile scans a single file and upserts it into the database.
func (s *Scanner) ScanFile(ctx context.Context, filePath string, folder *models.MediaFolder) error {
	var stopWatch context.CancelFunc
	ctx, stopWatch = s.watchFolderContext(ctx, folder.ID)
	defer stopWatch()
	if err := s.ensureFolderEnabled(ctx, folder.ID); err != nil {
		return err
	}

	// Verify the file extension is recognized.
	ext := strings.ToLower(filepath.Ext(filePath))
	if !videoExtensions[ext] {
		return fmt.Errorf("unrecognized video extension: %s", ext)
	}

	// Look up only this specific file instead of loading the entire folder.
	existingByPath := make(map[string]*scanStateFile, 1)
	existing, err := s.fileRepo.GetByPath(ctx, filePath)
	if err == nil {
		existingByPath[filePath] = scanStateFromMediaFile(existing)
	}
	existingContentStatuses, err := s.itemRepo.GetStatusByIDs(ctx, collectScanStateContentIDs([]*scanStateFile{existingByPath[filePath]}))
	if err != nil {
		return fmt.Errorf("loading item statuses for file: %w", err)
	}
	observation, ok := ObserveRoot(filePath, folder.Type)
	if ok {
		cleared, clearErr := s.clearLegacyLinksForUnmatchableRoots(ctx, folder.ID, []RootObservation{observation})
		if clearErr != nil {
			return clearErr
		}
		if cleared > 0 {
			if _, _, _, reconcileErr := s.reconcileLibraryMemberships(ctx, folder.ID); reconcileErr != nil {
				return fmt.Errorf("reconciling folder membership after clearing legacy links: %w", reconcileErr)
			}
		}
	}

	rootOverrides, err := s.loadRootOverrides(ctx, folder.ID, []string{filepath.Dir(filePath)})
	if err != nil {
		return fmt.Errorf("loading root overrides for file: %w", err)
	}
	rootInference := inferRootAssignments([]string{filePath}, folder.Type, folder.ID, rootOverrides)
	s.logRootInferenceDisagreements(rootInference.Assignments)

	groupInference := inferGroupAssignments([]string{filePath}, folder.Type, folder.ID, rootInference.Assignments)
	groupOverrides, err := s.loadGroupOverrides(ctx, folder.ID)
	if err != nil {
		return fmt.Errorf("loading group overrides for file: %w", err)
	}
	applyGroupOverrides(&groupInference, groupOverrides)
	_, _, err = s.processFile(ctx, filePath, folder, existingByPath, existingContentStatuses, rootInference.Assignments[filepath.Clean(filePath)], groupInference.Assignments[filepath.Clean(filePath)], newExternalSubtitleDirCache())
	if err != nil {
		return err
	}
	if err := s.reconcileScannedRoots(
		ctx,
		folder.ID,
		nil,
		rootInference.Snapshots,
	); err != nil {
		return fmt.Errorf("reconciling scanned root for file: %w", err)
	}
	scopePath := filepath.Dir(filePath)
	if err := s.reconcileScannedGroups(ctx, folder.ID, false, []string{scopePath}, false, groupInference); err != nil {
		return fmt.Errorf("reconciling scanned groups for file: %w", err)
	}
	if err := s.syncPresentLibraryState(ctx, folder.ID); err != nil {
		return fmt.Errorf("syncing present library state for file: %w", err)
	}
	if s.seriesQueueSyncer != nil {
		if err := s.seriesQueueSyncer.SyncInScope(ctx, folder.ID, scopePath); err != nil {
			return fmt.Errorf("syncing series match queue for file: %w", err)
		}
		slog.Info("metadata: series root queue sync",
			"folder_id", folder.ID,
			"scope", scopePath,
		)
	}
	if s.movieQueueSyncer != nil {
		if err := s.movieQueueSyncer.SyncInScope(ctx, folder.ID, filepath.Clean(filePath)); err != nil {
			return fmt.Errorf("syncing movie match queue for file: %w", err)
		}
		slog.Info("metadata: movie file queue sync",
			"folder_id", folder.ID,
			"scope", filepath.Clean(filePath),
		)
	}
	return nil
}

func (s *Scanner) watchFolderContext(ctx context.Context, folderID int) (context.Context, context.CancelFunc) {
	cancel := func() {}
	if ctx == nil || folderID <= 0 || s == nil || s.folderRepo == nil {
		return ctx, cancel
	}

	watchCtx, cancel := context.WithCancel(ctx)
	enabled, err := s.folderEnabledState(watchCtx, folderID)
	if err != nil {
		slog.Warn("scanner: failed to load folder state", "folder_id", folderID, "error", err)
		cancel()
		return watchCtx, cancel
	}
	if !enabled {
		cancel()
		return watchCtx, cancel
	}

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				enabled, err := s.folderEnabledState(watchCtx, folderID)
				if err != nil {
					slog.Warn("scanner: failed to refresh folder state", "folder_id", folderID, "error", err)
					continue
				}
				if !enabled {
					cancel()
					return
				}
			}
		}
	}()

	return watchCtx, cancel
}

func (s *Scanner) ensureFolderEnabled(ctx context.Context, folderID int) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if s == nil || s.folderRepo == nil || folderID <= 0 {
		return nil
	}

	enabled, err := s.folderEnabledState(ctx, folderID)
	switch {
	case errors.Is(err, catalog.ErrFolderNotFound):
		return context.Canceled
	case err != nil:
		return fmt.Errorf("loading folder state: %w", err)
	case !enabled:
		return context.Canceled
	default:
		return nil
	}
}

func (s *Scanner) folderEnabledState(ctx context.Context, folderID int) (bool, error) {
	if s == nil || s.folderRepo == nil || folderID <= 0 {
		return true, nil
	}
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	switch {
	case errors.Is(err, catalog.ErrFolderNotFound):
		return false, err
	case err != nil:
		return false, err
	case folder == nil:
		return false, catalog.ErrFolderNotFound
	default:
		return folder.Enabled, nil
	}
}

// fileAction represents what happened when processing a file.
type fileAction int

const (
	actionNew fileAction = iota
	actionUpdated
	actionUnchanged
)

// processFile handles a single file: checks if it changed, gathers hints,
// probes it, and upserts it.
func (s *Scanner) processFile(
	ctx context.Context,
	filePath string,
	folder *models.MediaFolder,
	existingByPath map[string]*scanStateFile,
	existingContentStatuses map[string]string,
	assignment fileRootAssignment,
	groupAssignment fileGroupAssignment,
	subtitleCache *externalSubtitleDirCache,
) (fileAction, []string, error) {
	// Stat the file.
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, nil, fmt.Errorf("stat %s: %w", filePath, err)
	}

	fileSize := info.Size()
	fileModifiedAt := normalizeFileModifiedAt(info.ModTime())
	var externalSubs []ExternalSubtitleInfo
	externalSubsLoaded := false
	externalSubsChecked := false
	loadExternalSubs := func() []ExternalSubtitleInfo {
		if externalSubsLoaded {
			return externalSubs
		}
		externalSubsLoaded = true
		var subErr error
		externalSubs, subErr = subtitleCache.Detect(filePath)
		if subErr != nil {
			slog.Warn("scanner: subtitle detection failed", "path", filePath, "error", subErr)
			externalSubs = nil
			externalSubsChecked = false
			return externalSubs
		}
		externalSubsChecked = true
		return externalSubs
	}

	// Check if unchanged (same size) only when playback-critical probe data
	// is already present or this scanner cannot repair it on this node. This
	// lets rescans repair legacy rows when local ffprobe metadata is available
	// without forcing endless rewrites on nodes that cannot probe.
	if existing, ok := existingByPath[filePath]; ok {
		currentExternalSubtitlePaths := externalSubtitleInfoPaths(loadExternalSubs())
		updateReasons := scanStateUpdateReasons(existing, fileSize, fileModifiedAt, currentExternalSubtitlePaths, externalSubsChecked, assignment, groupAssignment, folder.Type, s.ffprobePath != "")
		if shouldSkipStableConfirmedScanState(existing, existingContentStatuses[existing.ContentID], fileSize, fileModifiedAt, updateReasons, s.ffprobePath != "") {
			return actionUnchanged, nil, nil
		}
		if len(updateReasons) == 0 {
			return actionUnchanged, nil, nil
		}
		action := actionUpdated
		// Gather hints (OSHash only).
		hints := s.gatherHints(filePath)
		fileHash := hints.FileHash

		// Try to get probe data.
		probe, probeSource := s.probeFile(ctx, filePath)

		// Detect external subtitles.
		externalSubs = loadExternalSubs()

		// Check for intro/credits markers from S3.
		markerFetcher := s.markerFetcher
		if markerFetcher == nil {
			markerFetcher = s.fetchMarkers
		}
		markers := markerFetcher(ctx, fileHash)

		// Build the media file for upsert.
		mf := models.MediaFile{
			MediaFolderID:  folder.ID,
			FilePath:       filePath,
			FileSize:       fileSize,
			FileModifiedAt: &fileModifiedAt,
			FileHash:       fileHash,
		}
		if assignment.RootPath != "" {
			mf.CanonicalRootPath = filepath.Clean(assignment.RootPath)
		} else if root, ok := naming.DetectCanonicalRoot(filePath, folder.Type); ok {
			mf.CanonicalRootPath = filepath.Clean(root.RootPath)
		}
		mf.ObservedRootPath = filepath.Clean(groupAssignment.ObservedRootPath)
		mf.ContentGroupKey = groupAssignment.ContentGroupKey
		mf.GroupKeyVersion = groupAssignment.GroupKeyVersion
		mf.BaseTitle = groupAssignment.BaseTitle
		mf.BaseYear = groupAssignment.BaseYear
		mf.BaseType = groupAssignment.BaseType
		mf.IdentityConfidence = groupAssignment.Confidence
		mf.IdentityJSON = append([]byte(nil), groupAssignment.EvidenceJSON...)
		if filenameHints := naming.ParseFilename(filePath, folder.Type); filenameHints != nil &&
			filenameHints.Type == "series" && filenameHints.EpisodeNum > 0 {
			mf.SeasonNumber = filenameHints.SeasonNum
			mf.EpisodeNumber = filenameHints.EpisodeNum
		}
		variantHints := naming.ParseVariantHints(filePath, folder.Type)
		if existing != nil && existing.EditionSource == "import" && existing.EditionKey != "" {
			variantHints = &naming.VariantHints{
				EditionRaw:            existing.EditionRaw,
				EditionKey:            existing.EditionKey,
				EditionSource:         existing.EditionSource,
				EditionConfidence:     existing.EditionConfidence,
				PresentationKind:      existing.PresentationKind,
				PresentationGroupKey:  existing.PresentationGroupKey,
				PresentationPartIndex: existing.PresentationPartIndex,
				MultiEpisodeStart:     existing.MultiEpisodeStart,
				MultiEpisodeEnd:       existing.MultiEpisodeEnd,
			}
		}
		if variantHints != nil {
			mf.EditionRaw = variantHints.EditionRaw
			mf.EditionKey = variantHints.EditionKey
			mf.EditionConfidence = variantHints.EditionConfidence
			mf.EditionSource = variantHints.EditionSource
			mf.PresentationKind = variantHints.PresentationKind
			mf.PresentationGroupKey = variantHints.PresentationGroupKey
			mf.PresentationPartIndex = variantHints.PresentationPartIndex
			mf.MultiEpisodeStart = variantHints.MultiEpisodeStart
			mf.MultiEpisodeEnd = variantHints.MultiEpisodeEnd
		}

		// Apply probe data if available.
		if probe != nil {
			applyProbeData(&mf, probe, probeSource)
		}

		if mf.SubtitleTracks == nil {
			mf.SubtitleTracks = []models.SubtitleTrack{}
		}

		modelExternalSubs := make([]models.ExternalSubtitle, len(externalSubs))
		for i, es := range externalSubs {
			modelExternalSubs[i] = models.ExternalSubtitle{
				Path:     es.Path,
				Language: es.Language,
				Format:   es.Format,
				Title:    es.Title,
				Forced:   es.Forced,
			}
		}
		mf.ExternalSubtitles = modelExternalSubs
		if mf.ExternalSubtitles == nil {
			mf.ExternalSubtitles = []models.ExternalSubtitle{}
		}

		upserted, upsertErr := s.fileRepo.Upsert(ctx, mf)
		if upsertErr != nil {
			return 0, nil, fmt.Errorf("upserting file %s: %w", filePath, upsertErr)
		}
		if err := s.enqueueMetadataWork(ctx, folder, upserted); err != nil {
			return 0, nil, fmt.Errorf("enqueueing metadata work for file %s: %w", filePath, err)
		}
		if markers != nil {
			applied, markerErr := s.fileRepo.UpsertMarkers(ctx, upserted.ID, MarkerUpdate{
				IntroStart:    markers.IntroStart,
				IntroEnd:      markers.IntroEnd,
				CreditsStart:  markers.CreditsStart,
				CreditsEnd:    markers.CreditsEnd,
				MarkersSource: models.MarkerSourceS3,
			})
			if markerErr != nil {
				return 0, nil, fmt.Errorf("upserting markers for file %s: %w", filePath, markerErr)
			}
			if !applied {
				slog.Debug("scanner: skipped lower-priority s3 markers", "path", filePath, "hash", fileHash)
			}
		}

		return action, updateReasons, nil
	}

	action := actionNew

	// Gather hints (OSHash only).
	hints := s.gatherHints(filePath)
	fileHash := hints.FileHash

	// Try to get probe data.
	probe, probeSource := s.probeFile(ctx, filePath)

	// Detect external subtitles.
	externalSubs = loadExternalSubs()

	// Check for intro/credits markers from S3.
	markerFetcher := s.markerFetcher
	if markerFetcher == nil {
		markerFetcher = s.fetchMarkers
	}
	markers := markerFetcher(ctx, fileHash)

	// Build the media file for upsert.
	mf := models.MediaFile{
		MediaFolderID:  folder.ID,
		FilePath:       filePath,
		FileSize:       fileSize,
		FileModifiedAt: &fileModifiedAt,
		FileHash:       fileHash,
	}
	if assignment.RootPath != "" {
		mf.CanonicalRootPath = filepath.Clean(assignment.RootPath)
	} else if root, ok := naming.DetectCanonicalRoot(filePath, folder.Type); ok {
		mf.CanonicalRootPath = filepath.Clean(root.RootPath)
	}
	mf.ObservedRootPath = filepath.Clean(groupAssignment.ObservedRootPath)
	mf.ContentGroupKey = groupAssignment.ContentGroupKey
	mf.GroupKeyVersion = groupAssignment.GroupKeyVersion
	mf.BaseTitle = groupAssignment.BaseTitle
	mf.BaseYear = groupAssignment.BaseYear
	mf.BaseType = groupAssignment.BaseType
	mf.IdentityConfidence = groupAssignment.Confidence
	mf.IdentityJSON = append([]byte(nil), groupAssignment.EvidenceJSON...)
	if filenameHints := naming.ParseFilename(filePath, folder.Type); filenameHints != nil &&
		filenameHints.Type == "series" && filenameHints.EpisodeNum > 0 {
		mf.SeasonNumber = filenameHints.SeasonNum
		mf.EpisodeNumber = filenameHints.EpisodeNum
	}
	variantHints := naming.ParseVariantHints(filePath, folder.Type)
	if existing, ok := existingByPath[filePath]; ok && existing != nil && existing.EditionSource == "import" && existing.EditionKey != "" {
		variantHints = &naming.VariantHints{
			EditionRaw:            existing.EditionRaw,
			EditionKey:            existing.EditionKey,
			EditionSource:         existing.EditionSource,
			EditionConfidence:     existing.EditionConfidence,
			PresentationKind:      existing.PresentationKind,
			PresentationGroupKey:  existing.PresentationGroupKey,
			PresentationPartIndex: existing.PresentationPartIndex,
			MultiEpisodeStart:     existing.MultiEpisodeStart,
			MultiEpisodeEnd:       existing.MultiEpisodeEnd,
		}
	}
	if variantHints != nil {
		mf.EditionRaw = variantHints.EditionRaw
		mf.EditionKey = variantHints.EditionKey
		mf.EditionConfidence = variantHints.EditionConfidence
		mf.EditionSource = variantHints.EditionSource
		mf.PresentationKind = variantHints.PresentationKind
		mf.PresentationGroupKey = variantHints.PresentationGroupKey
		mf.PresentationPartIndex = variantHints.PresentationPartIndex
		mf.MultiEpisodeStart = variantHints.MultiEpisodeStart
		mf.MultiEpisodeEnd = variantHints.MultiEpisodeEnd
	}

	// Apply probe data if available.
	if probe != nil {
		applyProbeData(&mf, probe, probeSource)
	}

	if mf.SubtitleTracks == nil {
		mf.SubtitleTracks = []models.SubtitleTrack{}
	}

	// Convert external subtitles to model type.
	modelExternalSubs := make([]models.ExternalSubtitle, len(externalSubs))
	for i, es := range externalSubs {
		modelExternalSubs[i] = models.ExternalSubtitle{
			Path:     es.Path,
			Language: es.Language,
			Format:   es.Format,
			Title:    es.Title,
			Forced:   es.Forced,
		}
	}
	mf.ExternalSubtitles = modelExternalSubs

	if mf.ExternalSubtitles == nil {
		mf.ExternalSubtitles = []models.ExternalSubtitle{}
	}

	// Upsert into DB.
	upserted, upsertErr := s.fileRepo.Upsert(ctx, mf)
	if upsertErr != nil {
		return 0, nil, fmt.Errorf("upserting file %s: %w", filePath, upsertErr)
	}
	if err := s.enqueueMetadataWork(ctx, folder, upserted); err != nil {
		return 0, nil, fmt.Errorf("enqueueing metadata work for file %s: %w", filePath, err)
	}
	if markers != nil {
		applied, markerErr := s.fileRepo.UpsertMarkers(ctx, upserted.ID, MarkerUpdate{
			IntroStart:    markers.IntroStart,
			IntroEnd:      markers.IntroEnd,
			CreditsStart:  markers.CreditsStart,
			CreditsEnd:    markers.CreditsEnd,
			MarkersSource: models.MarkerSourceS3,
		})
		if markerErr != nil {
			return 0, nil, fmt.Errorf("upserting markers for file %s: %w", filePath, markerErr)
		}
		if !applied {
			slog.Debug("scanner: skipped lower-priority s3 markers", "path", filePath, "hash", fileHash)
		}
	}

	return action, nil, nil
}

func scanStateUpdateReasons(
	existing *scanStateFile,
	fileSize int64,
	fileModifiedAt time.Time,
	currentExternalSubtitlePaths []string,
	externalSubtitlesChecked bool,
	assignment fileRootAssignment,
	groupAssignment fileGroupAssignment,
	libraryType string,
	canRepairProbe bool,
) []string {
	if existing == nil {
		return nil
	}

	reasons := make([]string, 0, 7)
	if existing.FileSize != fileSize {
		reasons = append(reasons, "size_changed")
	}
	if !sameFileModifiedAt(existing.FileModifiedAt, fileModifiedAt) {
		reasons = append(reasons, "mtime_changed")
	}
	if existing.MissingSince != nil {
		reasons = append(reasons, "was_missing")
	}
	if canRepairProbe && needsCriticalProbeRepairScanState(existing) {
		reasons = append(reasons, "probe_repair")
	}
	if externalSubtitlesChecked {
		if !sameStringSet(existing.ExternalSubtitlePaths, currentExternalSubtitlePaths) {
			reasons = append(reasons, "external_subtitle_changed")
		}
	} else if hasMissingExternalSubtitlePath(existing.ExternalSubtitlePaths) {
		reasons = append(reasons, "external_subtitle_missing")
	}
	if scanStateRootAssignmentChanged(existing, assignment, libraryType) {
		reasons = append(reasons, "root_assignment_changed")
	}
	if scanStateGroupAssignmentChanged(existing, groupAssignment) {
		reasons = append(reasons, "group_assignment_changed")
	}
	return reasons
}

func externalSubtitleInfoPaths(subtitles []ExternalSubtitleInfo) []string {
	paths := make([]string, 0, len(subtitles))
	for _, subtitle := range subtitles {
		path := strings.TrimSpace(subtitle.Path)
		if path == "" {
			continue
		}
		paths = append(paths, filepath.Clean(path))
	}
	return paths
}

func sameStringSet(left, right []string) bool {
	seen := make(map[string]int, len(left))
	for _, value := range left {
		value = filepath.Clean(strings.TrimSpace(value))
		if value == "." || value == "" {
			continue
		}
		seen[value]++
	}
	for _, value := range right {
		value = filepath.Clean(strings.TrimSpace(value))
		if value == "." || value == "" {
			continue
		}
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func hasMissingExternalSubtitlePath(paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func shouldSkipStableConfirmedScanState(
	existing *scanStateFile,
	itemStatus string,
	fileSize int64,
	fileModifiedAt time.Time,
	updateReasons []string,
	canRepairProbe bool,
) bool {
	if existing == nil || existing.ContentID == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(itemStatus), "matched") {
		return false
	}
	if existing.FileSize != fileSize {
		return false
	}
	if !sameFileModifiedAt(existing.FileModifiedAt, fileModifiedAt) {
		return false
	}
	if existing.MissingSince != nil {
		return false
	}
	if canRepairProbe && needsCriticalProbeRepairScanState(existing) {
		return false
	}
	if len(updateReasons) > 0 {
		return false
	}
	return true
}

func scannerUpdateReasons(
	existing *models.MediaFile,
	fileSize int64,
	fileModifiedAt time.Time,
	assignment fileRootAssignment,
	groupAssignment fileGroupAssignment,
	libraryType string,
	canRepairProbe bool,
) []string {
	if existing == nil {
		return nil
	}

	reasons := make([]string, 0, 6)
	if existing.FileSize != fileSize {
		reasons = append(reasons, "size_changed")
	}
	if !sameFileModifiedAt(existing.FileModifiedAt, fileModifiedAt) {
		reasons = append(reasons, "mtime_changed")
	}
	if existing.MissingSince != nil {
		reasons = append(reasons, "was_missing")
	}
	if canRepairProbe && NeedsCriticalProbeRepair(existing) {
		reasons = append(reasons, "probe_repair")
	}
	if rootAssignmentChanged(existing, assignment, libraryType) {
		reasons = append(reasons, "root_assignment_changed")
	}
	if groupAssignmentChanged(existing, groupAssignment) {
		reasons = append(reasons, "group_assignment_changed")
	}
	return reasons
}

func shouldSkipStableConfirmedFile(
	existing *models.MediaFile,
	itemStatus string,
	fileSize int64,
	fileModifiedAt time.Time,
	updateReasons []string,
	canRepairProbe bool,
) bool {
	if existing == nil || existing.ContentID == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(itemStatus), "matched") {
		return false
	}
	if existing.FileSize != fileSize {
		return false
	}
	if !sameFileModifiedAt(existing.FileModifiedAt, fileModifiedAt) {
		return false
	}
	if existing.MissingSince != nil {
		return false
	}
	if canRepairProbe && NeedsCriticalProbeRepair(existing) {
		return false
	}
	if len(updateReasons) > 0 {
		return false
	}
	return true
}

func sameFileModifiedAt(existing *time.Time, current time.Time) bool {
	if existing == nil {
		return false
	}
	return normalizeFileModifiedAt(*existing).Equal(normalizeFileModifiedAt(current))
}

func normalizeFileModifiedAt(ts time.Time) time.Time {
	return ts.UTC().Truncate(time.Microsecond)
}

func needsCriticalProbeRepairScanState(file *scanStateFile) bool {
	if file == nil {
		return true
	}
	if strings.TrimSpace(file.ProbeSource) == "" || file.ProbeUpdatedAt == nil {
		return true
	}
	if file.Duration <= 0 {
		return true
	}
	if strings.TrimSpace(file.Container) == "" {
		return true
	}
	if strings.TrimSpace(file.CodecVideo) == "" {
		return true
	}
	if strings.TrimSpace(file.CodecAudio) == "" {
		return true
	}
	if strings.TrimSpace(file.Resolution) == "" {
		return true
	}
	if !file.HasVideoTracks {
		return true
	}
	if !file.HasAudioTracks {
		return true
	}
	if !file.HasChapters {
		return true
	}
	return false
}

func (s *Scanner) enqueueMetadataWork(ctx context.Context, folder *models.MediaFolder, file *models.MediaFile) error {
	if s == nil || s.metadataQueue == nil || folder == nil || file == nil {
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(folder.Type)) {
	case "movie", "movies":
		if err := s.metadataQueue.EnqueueMovieFile(ctx, file.ID); err != nil {
			return err
		}
		slog.Debug("metadata queue: movie file enqueued",
			"folder_id", folder.ID,
			"file_id", file.ID,
			"path", file.FilePath,
		)
	case "series", "tv", "show", "tvshows":
		if strings.TrimSpace(file.ObservedRootPath) == "" {
			return nil
		}
		if err := s.metadataQueue.EnqueueSeriesRoot(ctx, folder.ID, file.ObservedRootPath); err != nil {
			return err
		}
		slog.Debug("metadata queue: series root touched",
			"folder_id", folder.ID,
			"observed_root_path", file.ObservedRootPath,
			"file_id", file.ID,
		)
	}

	return nil
}

func (s *Scanner) reconcileScannedGroups(
	ctx context.Context,
	folderID int,
	fullFolderScan bool,
	scopeRoots []string,
	deleteMissingInScope bool,
	groups groupInferenceResult,
) error {
	if s == nil {
		return nil
	}
	if s.groupSnapshotRepo != nil {
		if fullFolderScan {
			if err := s.groupSnapshotRepo.ReplaceForFolder(ctx, folderID, groups.ScannedGroups); err != nil {
				return err
			}
		} else if deleteMissingInScope {
			for _, scope := range scopeRoots {
				if err := s.groupSnapshotRepo.ReplaceInScope(ctx, folderID, scope, filterScannedGroupsByScope(groups.ScannedGroups, scope)); err != nil {
					return err
				}
			}
		} else {
			if err := s.groupSnapshotRepo.UpsertMany(ctx, groups.ScannedGroups); err != nil {
				return err
			}
		}
	}
	if s.locationRepo != nil {
		if fullFolderScan {
			if err := s.locationRepo.ReplaceForFolder(ctx, folderID, groups.Locations); err != nil {
				return err
			}
		} else if deleteMissingInScope {
			for _, scope := range scopeRoots {
				if err := s.locationRepo.ReplaceInScope(ctx, folderID, scope, filterObservedLocationsByScope(groups.Locations, scope)); err != nil {
					return err
				}
			}
		} else {
			if err := s.locationRepo.UpsertMany(ctx, groups.Locations); err != nil {
				return err
			}
		}
	}
	if s.groupLocationRepo != nil {
		if fullFolderScan {
			if err := s.groupLocationRepo.ReplaceForFolder(ctx, folderID, groups.GroupLocations); err != nil {
				return err
			}
		} else if deleteMissingInScope {
			for _, scope := range scopeRoots {
				if err := s.groupLocationRepo.ReplaceInScope(ctx, folderID, scope, filterGroupLocationsByScope(groups.GroupLocations, scope)); err != nil {
					return err
				}
			}
		} else {
			if err := s.groupLocationRepo.UpsertMany(ctx, groups.GroupLocations); err != nil {
				return err
			}
		}
	}
	return nil
}

func filterScannedGroupsByScope(groups []models.ScannedMediaGroup, scopePath string) []models.ScannedMediaGroup {
	filtered := make([]models.ScannedMediaGroup, 0, len(groups))
	for _, group := range groups {
		if pathWithinAnyRoot(group.SampleObservedRootPath, []string{scopePath}) {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func filterObservedLocationsByScope(locations []models.ObservedMediaLocation, scopePath string) []models.ObservedMediaLocation {
	filtered := make([]models.ObservedMediaLocation, 0, len(locations))
	for _, location := range locations {
		if pathWithinAnyRoot(location.ObservedRootPath, []string{scopePath}) {
			filtered = append(filtered, location)
		}
	}
	return filtered
}

func filterGroupLocationsByScope(locations []models.MediaGroupLocation, scopePath string) []models.MediaGroupLocation {
	filtered := make([]models.MediaGroupLocation, 0, len(locations))
	for _, location := range locations {
		if pathWithinAnyRoot(location.ObservedRootPath, []string{scopePath}) {
			filtered = append(filtered, location)
		}
	}
	return filtered
}

func (s *Scanner) reconcileScannedRoots(
	ctx context.Context,
	folderID int,
	scopeRoots []string,
	roots []models.ScannedMediaRoot,
) error {
	if s == nil || s.rootSnapshotRepo == nil {
		return nil
	}

	seenByScope := make(map[string][]string, len(scopeRoots))
	for _, scope := range scopeRoots {
		seenByScope[scope] = []string{}
	}

	for _, root := range roots {
		for scope := range seenByScope {
			if pathWithinAnyRoot(root.RootPath, []string{scope}) {
				seenByScope[scope] = append(seenByScope[scope], root.RootPath)
			}
		}
	}
	if err := s.rootSnapshotRepo.UpsertMany(ctx, roots); err != nil {
		return err
	}

	for scope, seenRoots := range seenByScope {
		if err := s.rootSnapshotRepo.DeleteMissingInScope(ctx, folderID, scope, seenRoots); err != nil {
			return err
		}
	}

	return nil
}

func (s *Scanner) loadRootOverrides(
	ctx context.Context,
	folderID int,
	scopeRoots []string,
) (map[string]models.MediaRootOverride, error) {
	overridesByRoot := map[string]models.MediaRootOverride{}
	if s == nil || s.rootOverrideRepo == nil || folderID <= 0 {
		return overridesByRoot, nil
	}

	overrides, err := s.rootOverrideRepo.ListByFolder(ctx, folderID)
	if err != nil {
		return nil, err
	}

	for _, override := range overrides {
		if len(scopeRoots) > 0 && !pathWithinAnyRoot(override.RootPath, scopeRoots) {
			continue
		}
		overridesByRoot[filepath.Clean(override.RootPath)] = override
	}
	return overridesByRoot, nil
}

func (s *Scanner) loadGroupOverrides(
	ctx context.Context,
	folderID int,
) (map[string]models.MediaGroupOverride, error) {
	overridesByKey := map[string]models.MediaGroupOverride{}
	if s == nil || s.groupOverrideRepo == nil || folderID <= 0 {
		return overridesByKey, nil
	}

	overrides, err := s.groupOverrideRepo.ListByFolder(ctx, folderID)
	if err != nil {
		return nil, err
	}
	for _, override := range overrides {
		overridesByKey[groupOverrideKey(override.GroupKeyVersion, override.ContentGroupKey)] = override
	}
	return overridesByKey, nil
}

func (s *Scanner) logRootInferenceDisagreements(assignments map[string]fileRootAssignment) {
	for _, assignment := range assignments {
		if assignment.LegacyRootPath == "" {
			continue
		}
		if assignment.LegacyRootPath == assignment.RootPath && assignment.LegacyType == assignment.InferredType {
			continue
		}
		slog.Info("scanner: root inference disagreement",
			"file_path", assignment.FilePath,
			"legacy_root_path", assignment.LegacyRootPath,
			"inferred_root_path", assignment.RootPath,
			"legacy_type", assignment.LegacyType,
			"inferred_type", assignment.InferredType,
			"wrapper_collapsed", assignment.WrapperCollapsed,
			"promoted_ancestor", assignment.PromotedAncestor,
		)
	}
}

func scanStateRootAssignmentChanged(existing *scanStateFile, assignment fileRootAssignment, libraryType string) bool {
	if existing == nil {
		return true
	}
	expectedRoot := assignment.RootPath
	if expectedRoot == "" {
		if root, ok := naming.DetectCanonicalRoot(existing.FilePath, libraryType); ok {
			expectedRoot = filepath.Clean(root.RootPath)
		}
	}
	if filepath.Clean(existing.CanonicalRootPath) != filepath.Clean(expectedRoot) {
		return true
	}

	hints := naming.ParseVariantHints(existing.FilePath, libraryType)
	if existing.EditionSource == "import" && existing.EditionKey != "" {
		hints = &naming.VariantHints{
			EditionRaw:            existing.EditionRaw,
			EditionKey:            existing.EditionKey,
			EditionSource:         existing.EditionSource,
			EditionConfidence:     existing.EditionConfidence,
			PresentationKind:      existing.PresentationKind,
			PresentationGroupKey:  existing.PresentationGroupKey,
			PresentationPartIndex: existing.PresentationPartIndex,
			MultiEpisodeStart:     existing.MultiEpisodeStart,
			MultiEpisodeEnd:       existing.MultiEpisodeEnd,
		}
	}
	if hints == nil {
		hints = &naming.VariantHints{}
	}
	if existing.EditionRaw != hints.EditionRaw ||
		existing.EditionKey != hints.EditionKey ||
		existing.EditionSource != hints.EditionSource ||
		existing.PresentationKind != hints.PresentationKind ||
		existing.PresentationGroupKey != hints.PresentationGroupKey ||
		existing.PresentationPartIndex != hints.PresentationPartIndex ||
		existing.MultiEpisodeStart != hints.MultiEpisodeStart ||
		existing.MultiEpisodeEnd != hints.MultiEpisodeEnd {
		return true
	}
	switch {
	case existing.EditionConfidence == nil && hints.EditionConfidence == nil:
		return false
	case existing.EditionConfidence == nil || hints.EditionConfidence == nil:
		return true
	default:
		return *existing.EditionConfidence != *hints.EditionConfidence
	}
}

func scanStateGroupAssignmentChanged(existing *scanStateFile, assignment fileGroupAssignment) bool {
	if existing == nil {
		return true
	}
	if filepath.Clean(existing.ObservedRootPath) != filepath.Clean(assignment.ObservedRootPath) {
		return true
	}
	if existing.ContentGroupKey != assignment.ContentGroupKey ||
		existing.GroupKeyVersion != assignment.GroupKeyVersion ||
		existing.BaseTitle != assignment.BaseTitle ||
		existing.BaseYear != assignment.BaseYear ||
		existing.BaseType != assignment.BaseType ||
		existing.IdentityConfidence != assignment.Confidence {
		return true
	}
	return !identityEvidenceEqual(existing.IdentityJSON, assignment.EvidenceJSON)
}

func rootAssignmentChanged(existing *models.MediaFile, assignment fileRootAssignment, libraryType string) bool {
	if existing == nil {
		return true
	}
	expectedRoot := assignment.RootPath
	if expectedRoot == "" {
		if root, ok := naming.DetectCanonicalRoot(existing.FilePath, libraryType); ok {
			expectedRoot = filepath.Clean(root.RootPath)
		}
	}
	if filepath.Clean(existing.CanonicalRootPath) != filepath.Clean(expectedRoot) {
		return true
	}

	hints := naming.ParseVariantHints(existing.FilePath, libraryType)
	if existing.EditionSource == "import" && existing.EditionKey != "" {
		hints = &naming.VariantHints{
			EditionRaw:            existing.EditionRaw,
			EditionKey:            existing.EditionKey,
			EditionSource:         existing.EditionSource,
			EditionConfidence:     existing.EditionConfidence,
			PresentationKind:      existing.PresentationKind,
			PresentationGroupKey:  existing.PresentationGroupKey,
			PresentationPartIndex: existing.PresentationPartIndex,
			MultiEpisodeStart:     existing.MultiEpisodeStart,
			MultiEpisodeEnd:       existing.MultiEpisodeEnd,
		}
	}
	if hints == nil {
		hints = &naming.VariantHints{}
	}
	if existing.EditionRaw != hints.EditionRaw ||
		existing.EditionKey != hints.EditionKey ||
		existing.EditionSource != hints.EditionSource ||
		existing.PresentationKind != hints.PresentationKind ||
		existing.PresentationGroupKey != hints.PresentationGroupKey ||
		existing.PresentationPartIndex != hints.PresentationPartIndex ||
		existing.MultiEpisodeStart != hints.MultiEpisodeStart ||
		existing.MultiEpisodeEnd != hints.MultiEpisodeEnd {
		return true
	}
	switch {
	case existing.EditionConfidence == nil && hints.EditionConfidence == nil:
		return false
	case existing.EditionConfidence == nil || hints.EditionConfidence == nil:
		return true
	default:
		return *existing.EditionConfidence != *hints.EditionConfidence
	}
}

func groupAssignmentChanged(existing *models.MediaFile, assignment fileGroupAssignment) bool {
	if existing == nil {
		return true
	}
	if filepath.Clean(existing.ObservedRootPath) != filepath.Clean(assignment.ObservedRootPath) {
		return true
	}
	if existing.ContentGroupKey != assignment.ContentGroupKey ||
		existing.GroupKeyVersion != assignment.GroupKeyVersion ||
		existing.BaseTitle != assignment.BaseTitle ||
		existing.BaseYear != assignment.BaseYear ||
		existing.BaseType != assignment.BaseType ||
		existing.IdentityConfidence != assignment.Confidence {
		return true
	}
	return !identityEvidenceEqual(existing.IdentityJSON, assignment.EvidenceJSON)
}

func identityEvidenceEqual(existing, expected []byte) bool {
	if bytes.Equal(existing, expected) {
		return true
	}
	if len(bytes.TrimSpace(existing)) == 0 || len(bytes.TrimSpace(expected)) == 0 {
		return len(bytes.TrimSpace(existing)) == 0 && len(bytes.TrimSpace(expected)) == 0
	}

	var existingValue any
	if err := json.Unmarshal(existing, &existingValue); err != nil {
		return false
	}
	var expectedValue any
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		return false
	}

	return reflect.DeepEqual(existingValue, expectedValue)
}

func applyProbeData(mf *models.MediaFile, probe *ProbeData, probeSource string) {
	mf.CodecVideo = probe.CodecVideo
	mf.CodecAudio = probe.CodecAudio
	mf.Resolution = probe.Resolution
	mf.AudioChannels = probe.AudioChannels
	mf.HDR = probe.HDR
	mf.Container = probe.Container
	mf.Duration = probe.Duration
	mf.Bitrate = probe.Bitrate
	mf.ProbeSource = probeSource

	now := time.Now().UTC()
	mf.ProbeUpdatedAt = &now

	videoTracks := make([]models.VideoTrack, len(probe.VideoTracks))
	for i, vt := range probe.VideoTracks {
		videoTracks[i] = models.VideoTrack{
			Title:           vt.Title,
			Codec:           vt.Codec,
			DolbyVision:     vt.DolbyVision,
			Profile:         vt.Profile,
			Level:           vt.Level,
			Width:           vt.Width,
			Height:          vt.Height,
			AspectRatio:     vt.AspectRatio,
			Interlaced:      vt.Interlaced,
			FrameRate:       vt.FrameRate,
			Bitrate:         vt.Bitrate,
			VideoRange:      vt.VideoRange,
			ColorPrimaries:  vt.ColorPrimaries,
			ColorSpace:      vt.ColorSpace,
			ColorTransfer:   vt.ColorTransfer,
			BitDepth:        vt.BitDepth,
			PixelFormat:     vt.PixelFormat,
			ReferenceFrames: vt.ReferenceFrames,
		}
	}
	mf.VideoTracks = videoTracks

	audioTracks := make([]models.AudioTrack, len(probe.AudioTracks))
	for i, at := range probe.AudioTracks {
		audioTracks[i] = models.AudioTrack{
			Title:         at.Title,
			EmbeddedTitle: at.EmbeddedTitle,
			Language:      at.Language,
			Codec:         at.Codec,
			Layout:        at.Layout,
			Channels:      at.Channels,
			Bitrate:       at.Bitrate,
			SampleRate:    at.SampleRate,
			BitDepth:      at.BitDepth,
			Default:       at.Default,
		}
	}
	mf.AudioTracks = audioTracks

	subtitleTracks := make([]models.SubtitleTrack, len(probe.SubtitleTracks))
	for i, st := range probe.SubtitleTracks {
		subtitleTracks[i] = models.SubtitleTrack{
			Index:           st.Index,
			Language:        st.Language,
			Codec:           st.Codec,
			Title:           st.Title,
			EmbeddedTitle:   st.EmbeddedTitle,
			Resolution:      st.Resolution,
			Forced:          st.Forced,
			Default:         st.Default,
			HearingImpaired: st.HearingImpaired,
		}
	}
	mf.SubtitleTracks = subtitleTracks

	chapters := make([]models.MediaChapter, len(probe.Chapters))
	for i, chapter := range probe.Chapters {
		chapters[i] = models.MediaChapter{
			Index:        chapter.Index,
			Title:        chapter.Title,
			StartSeconds: chapter.StartSeconds,
			EndSeconds:   chapter.EndSeconds,
			Source:       chapter.Source,
		}
	}
	mf.Chapters = chapters
}

// clearLegacyLinksForUnmatchableRoots was previously used to clear content
// links for files under roots without embedded folder IDs. With the library
// matching redesign, roots without folder IDs are now valid and create
// pending items, so this function is a no-op.
// TODO: remove callers and delete this function
func (s *Scanner) clearLegacyLinksForUnmatchableRoots(_ context.Context, _ int, _ []RootObservation) (int, error) {
	return 0, nil
}

// gatherHints computes the OSHash for a media file.
func (s *Scanner) gatherHints(filePath string) FileHints {
	hints := FileHints{}
	hash, err := ComputeOSHash(filePath)
	if err != nil {
		slog.Warn("scanner: OSHash computation failed", "path", filePath, "error", err)
	} else {
		hints.FileHash = hash
	}
	return hints
}

// probeFile attempts to get probe data by running local ffprobe.
func (s *Scanner) probeFile(ctx context.Context, filePath string) (*ProbeData, string) {
	if s.ffprobePath != "" {
		probe, err := ProbeFile(ctx, s.ffprobePath, filePath)
		if err != nil {
			slog.Warn("scanner: ffprobe failed", "path", filePath, "error", err)
			return nil, "local"
		}
		return probe, "local"
	}

	return nil, "local"
}

// fetchMarkers checks S3 for intro/credits markers for the given file hash.
func (s *Scanner) fetchMarkers(ctx context.Context, fileHash string) *IntroCreditsMarkers {
	if fileHash == "" || s.s3Client == nil {
		return nil
	}

	key := fmt.Sprintf("markers/%s.json", fileHash)
	data, err := s.s3Client.GetObject(ctx, s.s3Client.Bucket(), key)
	if err != nil {
		// Not found is expected; don't log it.
		return nil
	}

	var markers IntroCreditsMarkers
	if err := json.Unmarshal(data, &markers); err != nil {
		slog.Warn("scanner: markers JSON parse failed",
			"hash", fileHash,
			"error", err,
		)
		return nil
	}

	return &markers
}
