package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestScanStateCarriesProbeRejectionDB checks the scan state a library scan
// reads: a file ffprobe rejected keeps its mark there, so a rescan of the
// unchanged file skips the probe repair.
func TestScanStateCarriesProbeRejectionDB(t *testing.T) {
	ctx := context.Background()
	pool := newScannerTestPool(t)

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("probe-rejection-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	modifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	rejectedAt := modifiedAt.Add(time.Minute)
	repo := NewFileRepository(pool)
	if _, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID:  folderID,
		FilePath:       fmt.Sprintf("/tmp/probe-rejection-%d.mkv", suffix),
		FileSize:       1_000,
		FileModifiedAt: &modifiedAt,
		ProbeFailedAt:  &rejectedAt,
	}); err != nil {
		t.Fatalf("seed rejected file: %v", err)
	}

	states, err := repo.GetScanStateByFolder(ctx, folderID)
	if err != nil {
		t.Fatalf("GetScanStateByFolder: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("scan state rows = %d, want 1", len(states))
	}
	state := states[0]
	if state.ProbeFailedAt == nil || state.ProbeUpdatedAt != nil {
		t.Fatalf("scan state probe_failed_at = %v, probe_updated_at = %v, want a rejection and no probe", state.ProbeFailedAt, state.ProbeUpdatedAt)
	}
	reasons := scanStateUpdateReasons(state, 1_000, modifiedAt, nil, false, fileRootAssignment{}, fileGroupAssignment{}, "movies", true)
	if testStringSliceContains(reasons, "probe_repair") {
		t.Fatalf("rescan reasons = %#v, want no probe_repair for the unchanged rejected file", reasons)
	}
}

// TestRescanKeepsProbeFactsOfARejectedUnchangedFileDB covers a row probed
// before rejections were tracked: it keeps old probe facts without
// probe_updated_at, and ffprobe now rejects it. A rescan for a new subtitle
// sidecar probes it again and fails; that must record the subtitle without
// overwriting the probe facts the row still has.
func TestRescanKeepsProbeFactsOfARejectedUnchangedFileDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("rejected-legacy-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	moviePath := filepath.Join(root, "Legacy Movie (2020).mkv")
	if err := os.WriteFile(moviePath, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(moviePath)
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt := info.ModTime().UTC()
	rejectedAt := time.Now().UTC()
	repo := NewFileRepository(pool)
	if _, err := repo.Upsert(ctx, models.MediaFile{
		MediaFolderID:     folderID,
		FilePath:          moviePath,
		FileSize:          info.Size(),
		FileModifiedAt:    &modifiedAt,
		CodecVideo:        "h264",
		Container:         "matroska",
		Duration:          5400,
		ProbeFailedAt:     &rejectedAt,
		SubtitleTracks:    []models.SubtitleTrack{},
		ExternalSubtitles: []models.ExternalSubtitle{},
	}); err != nil {
		t.Fatalf("seed rejected legacy file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Legacy Movie (2020).en.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(repo, writeRejectingFFprobe(t), nil, 1, false, 0)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	stored, err := repo.GetByPath(ctx, moviePath)
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if stored.CodecVideo != "h264" || stored.Container != "matroska" || stored.Duration != 5400 {
		t.Fatalf("probe facts after rescan = %q %q %d, want h264 matroska 5400", stored.CodecVideo, stored.Container, stored.Duration)
	}
	if len(stored.ExternalSubtitles) != 1 {
		t.Fatalf("external subtitles after rescan = %+v, want the new sidecar", stored.ExternalSubtitles)
	}
	if stored.ProbeFailedAt == nil || stored.ProbeUpdatedAt != nil {
		t.Fatalf("probe state after rescan: failed=%v updated=%v, want the rejection kept", stored.ProbeFailedAt, stored.ProbeUpdatedAt)
	}
}

// TestScanSkipsVideoLibraryFilesWithoutVideoDB covers a file in a movie
// library that ffprobe reads as audio only, such as a soundtrack saved as
// .mkv. It is not cataloged, and a row written while the file still held video
// leaves through the missing-file path, so its title is hidden at once and
// deleted after the removal grace, and a later scan whose probe fails or
// rejects the file does not bring it back. Only a container whose header lists every stream is
// judged: an MPEG program stream can hold video past the part ffprobe reads.
func TestScanSkipsVideoLibraryFilesWithoutVideoDB(t *testing.T) {
	pool := newScannerTestPool(t)
	repo := NewFileRepository(pool)
	ffprobe := writeAudioOnlyFFprobe(t)

	for _, tc := range []struct {
		name string
		scan func(ctx context.Context, s *Scanner, folder *models.MediaFolder, paths []string) error
	}{
		{"folder scan", func(ctx context.Context, s *Scanner, folder *models.MediaFolder, _ []string) error {
			_, err := s.ScanFolder(ctx, folder)
			return err
		}},
		{"file scan", func(ctx context.Context, s *Scanner, folder *models.MediaFolder, paths []string) error {
			for _, path := range paths {
				if err := s.ScanFile(ctx, path, folder); err != nil {
					return err
				}
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("audio-only-%d", time.Now().UnixNano()))
			folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
			newPath := filepath.Join(root, "Soundtrack (2020).mkv")
			replacedPath := filepath.Join(root, "Replaced (2020).mkv")
			legacyPath := filepath.Join(root, "Score (2021).mkv")
			programStreamPath := filepath.Join(root, "Program Stream (2019).mpg")
			paths := []string{newPath, replacedPath, legacyPath, programStreamPath}
			for _, path := range paths {
				if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// One row a scan wrote while the file was still a video, and one
			// written for the audio-only file before such files were skipped.
			oldModifiedAt := time.Now().UTC().Add(-time.Hour)
			probedAt := time.Now().UTC()
			for _, row := range []models.MediaFile{
				{FilePath: replacedPath, FileSize: 1 << 30, FileModifiedAt: &oldModifiedAt, CodecVideo: "h264", Container: "matroska", Duration: 5400},
				{FilePath: legacyPath, FileSize: 5, FileModifiedAt: &oldModifiedAt, CodecAudio: "aac", Container: containerMKV, Duration: 6,
					AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}}, ProbeSource: probeSourceLocal, ProbeUpdatedAt: &probedAt},
			} {
				row.MediaFolderID, row.SubtitleTracks, row.ExternalSubtitles = folderID, []models.SubtitleTrack{}, []models.ExternalSubtitle{}
				if _, err := repo.Upsert(ctx, row); err != nil {
					t.Fatalf("seed %s: %v", filepath.Base(row.FilePath), err)
				}
			}

			if err := tc.scan(ctx, NewScanner(repo, ffprobe, nil, 1, false, time.Hour), folder, paths); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if _, err := repo.GetByPath(ctx, newPath); !errors.Is(err, ErrFileNotFound) {
				t.Fatalf("audio-only .mkv: GetByPath err = %v, want no row", err)
			}
			if stored, err := repo.GetByPath(ctx, replacedPath); err != nil || stored.MissingSince == nil || stored.CodecVideo != "h264" {
				t.Fatalf("replaced row: %s (err %v), want marked missing with its old facts", rowState(stored), err)
			}
			if stored, err := repo.GetByPath(ctx, programStreamPath); err != nil || stored.MissingSince != nil {
				t.Fatalf("audio-only .mpg: GetByPath = %v, %v; want a present row", stored, err)
			}

			for _, failing := range []string{filepath.Join(t.TempDir(), "ffprobe"), writeRejectingFFprobe(t)} {
				if err := tc.scan(ctx, NewScanner(repo, failing, nil, 1, false, time.Hour), folder, []string{legacyPath}); err != nil {
					t.Fatalf("scan with a failing ffprobe: %v", err)
				}
				if stored, err := repo.GetByPath(ctx, legacyPath); err != nil || stored.MissingSince == nil || stored.CodecAudio != "aac" {
					t.Fatalf("legacy audio-only row after a failed probe: %s (err %v), want still missing with its facts", rowState(stored), err)
				}
			}
		})
	}
}

// rowState describes the fields TestScanSkipsVideoLibraryFilesWithoutVideoDB
// checks.
func rowState(file *models.MediaFile) string {
	if file == nil {
		return "no row"
	}
	return fmt.Sprintf("missing_since=%v codec_video=%q codec_audio=%q", file.MissingSince, file.CodecVideo, file.CodecAudio)
}

// writeAudioOnlyFFprobe writes an ffprobe that reads every file as one AAC
// stream, in the container its extension names.
func writeAudioOnlyFFprobe(t *testing.T) string {
	t.Helper()
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, ffprobe, `#!/bin/sh
for arg; do file=$arg; done
case $file in
*.mpg) format=mpeg ;;
*.mp3) format=mp3 ;;
*) format=matroska,webm ;;
esac
printf '{"format":{"format_name":"%s","duration":"6.0","size":"5"},"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","channels":2}]}\n' "$format"
`)
	return ffprobe
}

// TestPodcastSubtreeScanKeepsAudioFilesDB covers podcast subtree scans, which
// share the video scan path with movie libraries: their audio files are
// cataloged, not skipped for having no video.
func TestPodcastSubtreeScanKeepsAudioFilesDB(t *testing.T) {
	pool := newScannerTestPool(t)
	repo := NewFileRepository(pool)
	ffprobe := writeAudioOnlyFFprobe(t)
	ctx := t.Context()
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "podcasts", fmt.Sprintf("podcast-audio-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "podcasts", Paths: []string{root}}
	show := filepath.Join(root, "Some Show")
	episode := filepath.Join(show, "Episode 1.mp3")
	if err := errors.Join(os.MkdirAll(show, 0o755), os.WriteFile(episode, []byte("audio"), 0o600)); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(repo, ffprobe, nil, 1, false, time.Hour)
	if _, err := s.ScanSubtree(ctx, folder, show); err != nil {
		t.Fatalf("ScanSubtree: %v", err)
	}
	if stored, err := repo.GetByPath(ctx, episode); err != nil || stored.MissingSince != nil {
		t.Fatalf("podcast episode: GetByPath = %v, %v; want a present row", stored, err)
	}
}
