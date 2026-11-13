package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A movie, series or mixed library picks its files by extension, so it also
// walks files that hold no video: a soundtrack saved as .mkv, an audio-only
// transport stream. Such a file is not cataloged. The playback planner has no
// route for a movie row without video, and the probe repair checks would ask
// for a new probe of it on every scan and every playback decision.
//
// The scanner decides from a probe that succeeded, never from one that failed,
// timed out or was rejected, and records the verdict in scanner_no_video_files
// against the file's path, size and modification time. While those still
// match, later scans leave the file out without hashing or probing it, so a
// probe that fails later cannot bring it back. Changed bytes are judged again.
// A row already written for the file is marked missing and leaves the library
// the way a deleted file's does. The file on disk is never touched.

// noVideoFile is one recorded verdict: the file at FilePath held no video
// stream when it had this size and modification time.
type noVideoFile struct {
	FilePath       string
	FileSize       int64
	FileModifiedAt time.Time
	FileHash       string
}

// describes reports whether the verdict was recorded for these bytes.
func (f noVideoFile) describes(fileSize int64, fileModifiedAt time.Time) bool {
	return f.FileSize == fileSize && sameFileModifiedAt(&f.FileModifiedAt, fileModifiedAt)
}

// catalogsOnlyVideo reports whether a library of this type catalogs video
// files only. ScanFile also sends a podcast library's video-extension files
// through processFile, and a podcast episode may be audio.
func catalogsOnlyVideo(folderType string) bool {
	mode := walkModeFor(folderType)
	return mode == walkModeMovie || mode == walkModeVideo
}

// deepProbeLimits is the probe that confirms a file has no video. MPEG
// transport and program streams and FLV announce a stream when its first
// packet arrives, so video that starts late is missing from the default
// probe, which stops after 5 seconds or 5 MB. This one reads up to 120
// seconds or 100 MB.
var deepProbeLimits = func() probeLimits {
	limits := defaultProbeLimits
	limits.inputOptions = []string{"-analyzeduration", "120000000", "-probesize", "100000000"}
	return limits
}()

// probeFoundNoVideo reports whether a successful probe listed no video
// stream. Embedded cover art is not one (convertProbeData).
func probeFoundNoVideo(probe *ProbeData) bool {
	return probe != nil && len(probe.VideoTracks) == 0 && strings.TrimSpace(probe.CodecVideo) == ""
}

// confirmNoVideo decides whether a file in a video library whose probe listed
// no video stream really has none, by probing it again with deepProbeLimits.
// It returns the probe to catalog the file from and whether the file has no
// video. A deeper probe that finds video replaces the first one, so the row
// describes the video. When the deeper probe fails the file stays undecided
// and is cataloged from the first probe, as before.
func (s *Scanner) confirmNoVideo(ctx context.Context, folder *models.MediaFolder, filePath string, probe *ProbeData) (*ProbeData, bool) {
	if !catalogsOnlyVideo(folder.Type) || !probeFoundNoVideo(probe) {
		return probe, false
	}
	deep, err := probeFileWithin(ctx, s.ffprobePath, filePath, deepProbeLimits)
	if err != nil {
		slog.WarnContext(ctx, "scanner: could not confirm that a file has no video; leaving it cataloged", "component", "scanner",
			"path", filePath,
			"error", err,
		)
		return probe, false
	}
	return deep, probeFoundNoVideo(deep)
}

// excludeNoVideoFile leaves out a file whose probe found no video: it records
// the verdict, retires the row the scan read at the path, if any, and logs the
// file once. The verdict is written first, so a scan cut short between the
// two writes leaves a present row that the next scan retires
// (dropNoVideoPaths), never a missing row a failed probe could restore.
func (s *Scanner) excludeNoVideoFile(
	ctx context.Context,
	folder *models.MediaFolder,
	existing *scanStateFile,
	verdict noVideoFile,
) (fileAction, []string, error) {
	if err := s.fileRepo.RecordNoVideoFile(ctx, folder.ID, verdict); err != nil {
		return 0, nil, fmt.Errorf("recording file without video %s: %w", verdict.FilePath, err)
	}
	action := actionNoVideo
	if existing != nil {
		retired, err := s.fileRepo.RetireNoVideoFile(ctx, existing, time.Now().UTC())
		if err != nil {
			return 0, nil, fmt.Errorf("retiring file without video %s: %w", verdict.FilePath, err)
		}
		if retired {
			action = actionNoVideoRetired
		}
	}
	slog.WarnContext(ctx, "scanner: file has no video stream; leaving it out of the library", "component", "scanner",
		"folder_id", folder.ID,
		"path", verdict.FilePath,
		"was_cataloged", action == actionNoVideoRetired,
	)
	return action, nil, nil
}

// sameBytesHaveNoVideo reports whether the library recorded a no-video verdict
// for a file with these bytes at another path. It is only asked for a new file
// whose probe returned nothing, so a renamed excluded file waits for a probe
// that works instead of being cataloged unprobed. OSHash samples the head and
// tail only, so a match never stands in for a successful probe.
func (s *Scanner) sameBytesHaveNoVideo(ctx context.Context, folder *models.MediaFolder, fileHash string, fileSize int64, fileModifiedAt time.Time) bool {
	if fileHash == "" || !catalogsOnlyVideo(folder.Type) {
		return false
	}
	verdicts, err := s.fileRepo.NoVideoFilesByBytes(ctx, folder.ID, fileHash, fileSize)
	if err != nil {
		slog.WarnContext(ctx, "scanner: could not read no-video verdicts", "component", "scanner", "folder_id", folder.ID, "error", err)
		return false
	}
	for _, verdict := range verdicts {
		if verdict.describes(fileSize, fileModifiedAt) {
			return true
		}
	}
	return false
}

// dropNoVideoPaths removes from paths the files a recorded verdict still
// describes, before the scan infers roots and groups from them, and retires
// any row still present at one (a scan that wrote the row after another scan
// recorded the verdict). It returns the remaining paths in order, how many
// were dropped, and how many rows it retired.
func (s *Scanner) dropNoVideoPaths(ctx context.Context, folder *models.MediaFolder, paths []string) (kept []string, dropped, retired int, err error) {
	if len(paths) == 0 || !catalogsOnlyVideo(folder.Type) {
		return paths, 0, 0, nil
	}
	verdicts, err := s.fileRepo.NoVideoFilesByPaths(ctx, folder.ID, paths)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(verdicts) == 0 {
		return paths, 0, 0, nil
	}
	excluded := make([]string, 0, len(verdicts))
	kept = make([]string, 0, len(paths))
	for _, path := range paths {
		verdict, ok := verdicts[path]
		if ok {
			// A stat failure leaves the path to processFile, which reports it.
			if info, statErr := fspath.Stat(path); statErr == nil && verdict.describes(info.Size(), normalizeFileModifiedAt(info.ModTime())) {
				excluded = append(excluded, path)
				observeFile(actionNoVideo, nil)
				continue
			}
		}
		kept = append(kept, path)
	}
	if len(excluded) == 0 {
		return paths, 0, 0, nil
	}
	retired, err = s.fileRepo.RetirePresentFilesAtPaths(ctx, folder.ID, excluded, time.Now().UTC())
	if err != nil {
		return nil, 0, 0, err
	}
	return kept, len(excluded), retired, nil
}

// candidateHasNoVideo reports whether a moved-file candidate's own probe
// found no video. Such a row must not be relinked to a new path: the file is
// probed there like a new one and left out.
func candidateHasNoVideo(candidate *scanStateFile) bool {
	if candidate.ProbeUpdatedAt == nil {
		return false
	}
	return strings.TrimSpace(candidate.CodecVideo) == "" && !candidate.HasVideoTracks
}

// dropNoVideoCandidates removes the moved-file candidates that describe a file
// without video: by their stored probe, or by a verdict recorded for their
// path and bytes (a retired row that never had a successful probe stored).
func (s *Scanner) dropNoVideoCandidates(ctx context.Context, folder *models.MediaFolder, candidates []*scanStateFile) ([]*scanStateFile, error) {
	if len(candidates) == 0 || !catalogsOnlyVideo(folder.Type) {
		return candidates, nil
	}
	kept := candidates[:0:0]
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidateHasNoVideo(candidate) {
			kept = append(kept, candidate)
			paths = append(paths, candidate.FilePath)
		}
	}
	if len(kept) == 0 {
		return kept, nil
	}
	verdicts, err := s.fileRepo.NoVideoFilesByPaths(ctx, folder.ID, paths)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(kept, func(candidate *scanStateFile) bool {
		verdict, ok := verdicts[candidate.FilePath]
		return ok && candidate.FileModifiedAt != nil && verdict.describes(candidate.FileSize, *candidate.FileModifiedAt)
	}), nil
}

// RecordNoVideoFile stores the verdict for a path, replacing an earlier one.
func (r *FileRepository) RecordNoVideoFile(ctx context.Context, folderID int, verdict noVideoFile) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO scanner_no_video_files (file_path, media_folder_id, file_size, file_modified_at, file_hash)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (file_path) DO UPDATE SET
			media_folder_id = EXCLUDED.media_folder_id,
			file_size = EXCLUDED.file_size,
			file_modified_at = EXCLUDED.file_modified_at,
			file_hash = EXCLUDED.file_hash`,
		verdict.FilePath, folderID, verdict.FileSize, verdict.FileModifiedAt, verdict.FileHash,
	)
	if err != nil {
		return fmt.Errorf("recording no-video verdict: %w", err)
	}
	return nil
}

// NoVideoFilesByPaths returns the folder's verdicts recorded at the given
// paths, keyed by path.
func (r *FileRepository) NoVideoFilesByPaths(ctx context.Context, folderID int, paths []string) (map[string]noVideoFile, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT file_path, file_size, file_modified_at, file_hash
		FROM scanner_no_video_files
		WHERE media_folder_id = $1 AND file_path = ANY($2::text[])`, folderID, paths)
	if err != nil {
		return nil, fmt.Errorf("querying no-video verdicts by path: %w", err)
	}
	verdicts, err := scanNoVideoFiles(rows)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]noVideoFile, len(verdicts))
	for _, verdict := range verdicts {
		byPath[verdict.FilePath] = verdict
	}
	return byPath, nil
}

// NoVideoFilesByBytes returns the folder's verdicts recorded for a file of
// this OSHash and size, at any path.
func (r *FileRepository) NoVideoFilesByBytes(ctx context.Context, folderID int, fileHash string, fileSize int64) ([]noVideoFile, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT file_path, file_size, file_modified_at, file_hash
		FROM scanner_no_video_files
		WHERE media_folder_id = $1 AND file_hash = $2 AND file_size = $3 AND file_hash <> ''`, folderID, fileHash, fileSize)
	if err != nil {
		return nil, fmt.Errorf("querying no-video verdicts by bytes: %w", err)
	}
	return scanNoVideoFiles(rows)
}

func scanNoVideoFiles(rows pgx.Rows) ([]noVideoFile, error) {
	defer rows.Close()
	var verdicts []noVideoFile
	for rows.Next() {
		var verdict noVideoFile
		if err := rows.Scan(&verdict.FilePath, &verdict.FileSize, &verdict.FileModifiedAt, &verdict.FileHash); err != nil {
			return nil, fmt.Errorf("scanning no-video verdict: %w", err)
		}
		verdicts = append(verdicts, verdict)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating no-video verdicts: %w", err)
	}
	return verdicts, nil
}

// RetireNoVideoFile marks the row a scan read as missing, while it is still
// the row the scan read: same path, size and modification time. A row another
// scan rewrote meanwhile (the file was replaced) is left alone. It reports
// whether a present row was marked; a row already missing keeps its
// missing_since, so its removal grace does not restart.
func (r *FileRepository) RetireNoVideoFile(ctx context.Context, read *scanStateFile, since time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET missing_since = $1, updated_at = NOW()
		WHERE id = $2 AND file_path = $3 AND file_size = $4 AND missing_since IS NULL
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM date_trunc('microseconds', $5::timestamptz)`,
		since, read.ID, read.FilePath, read.FileSize, read.FileModifiedAt,
	)
	if err != nil {
		return false, fmt.Errorf("marking file without video missing: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RetirePresentFilesAtPaths marks the folder's present rows at the given paths
// missing and returns how many it marked.
func (r *FileRepository) RetirePresentFilesAtPaths(ctx context.Context, folderID int, paths []string, since time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET missing_since = $1, updated_at = NOW()
		WHERE media_folder_id = $2 AND file_path = ANY($3::text[]) AND missing_since IS NULL`,
		since, folderID, paths,
	)
	if err != nil {
		return 0, fmt.Errorf("marking files without video missing: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
