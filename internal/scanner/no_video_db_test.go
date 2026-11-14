package scanner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// noVideoTestProbe is a fake ffprobe that answers by file name: "Score" and
// "Radio" files are audio only, a "Late" file shows its video only to the
// deeper confirming probe, and anything else has video and audio. It logs each
// run, and fails every run while the file at failFlag exists.
type noVideoTestProbe struct {
	path, log, failFlag string
}

func writeNoVideoTestProbe(t *testing.T) noVideoTestProbe {
	t.Helper()
	dir := t.TempDir()
	probe := noVideoTestProbe{
		path:     filepath.Join(dir, "ffprobe"),
		log:      filepath.Join(dir, "runs.log"),
		failFlag: filepath.Join(dir, "fail"),
	}
	const (
		video = `{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"color_range":"tv","r_frame_rate":"24/1"},`
		audio = `{"codec_type":"audio","codec_name":"aac","channels":2}`
	)
	writeFakeTool(t, probe.path, `#!/bin/sh
for arg; do file=$arg; done
echo "$file" >> '`+probe.log+`'
[ -e '`+probe.failFlag+`' ] && exit 1
video='`+video+`'
case "$file" in
*Score*|*Radio*) video= ;;
*Late*) case "$*" in *-analyzeduration*) ;; *) video= ;; esac ;;
esac
echo '{"format":{"format_name":"matroska,webm","duration":"6.0","bit_rate":"800000"},"streams":['"$video"'`+audio+`]}'
`)
	return probe
}

// runs counts the probe's runs on a file.
func (p noVideoTestProbe) runs(t *testing.T, file string) int {
	t.Helper()
	log, err := os.ReadFile(p.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(log), file+"\n")
}

// writeNoVideoTestFile writes a file large enough to have an OSHash, with
// bytes that differ per name.
func writeNoVideoTestFile(t *testing.T, root, title, name string) string {
	t.Helper()
	path := filepath.Join(root, title, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(name), 200_000/len(name)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedAudioOnlyMovieRow catalogs path the way a scan before the rule did: a
// present movie row whose probe found audio and no video.
func seedAudioOnlyMovieRow(t *testing.T, repo *FileRepository, folderID int, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := ComputeOSHash(path)
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt, probedAt := info.ModTime().UTC(), time.Now().UTC()
	if _, err := repo.Upsert(t.Context(), models.MediaFile{
		MediaFolderID:  folderID,
		FilePath:       path,
		BaseType:       "movie",
		FileSize:       info.Size(),
		FileModifiedAt: &modifiedAt,
		FileHash:       hash,
		CodecAudio:     "aac",
		Container:      "mkv",
		Duration:       6,
		AudioTracks:    []models.AudioTrack{{Codec: "aac", Channels: 2}},
		SubtitleTracks: []models.SubtitleTrack{},
		Chapters:       []models.MediaChapter{},
		ProbeSource:    probeSourceLocal,
		ProbeUpdatedAt: &probedAt,
	}); err != nil {
		t.Fatalf("seed audio-only movie row: %v", err)
	}
}

// presentFileNames returns the base names of the folder's present files.
func presentFileNames(t *testing.T, pool *pgxpool.Pool, folderID int) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT file_path FROM media_files WHERE media_folder_id = $1 AND missing_since IS NULL`, folderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatal(err)
		}
		names = append(names, filepath.Base(path))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

// TestScanLeavesOutFilesWithoutVideoDB: a movie library does not catalog a
// file whose probe finds no video, in any container, and retires a row written
// for one earlier. It stops probing the file, and neither a failed probe nor
// a rename brings it back. A file whose video starts past the default probe
// window is kept.
func TestScanLeavesOutFilesWithoutVideoDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("no-video-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	repo := NewFileRepository(pool)

	writeNoVideoTestFile(t, root, "Control Film (2018)", "Control Film (2018).mkv")
	late := writeNoVideoTestFile(t, root, "Late Start (2012)", "Late Start (2012).ts")
	writeNoVideoTestFile(t, root, "Radio Play (2019)", "Radio Play (2019).ts")
	score := writeNoVideoTestFile(t, root, "Score Album (2020)", "Score Album (2020).mkv")
	seedAudioOnlyMovieRow(t, repo, folderID, score)

	probe := writeNoVideoTestProbe(t)
	s := NewScanner(repo, probe.path, nil, 1, false, 0)
	want := []string{"Control Film (2018).mkv", "Late Start (2012).ts"}
	scan := func(step string) {
		t.Helper()
		if _, err := s.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got := presentFileNames(t, pool, folderID); !slices.Equal(got, want) {
			t.Fatalf("%s: present files = %q, want %q", step, got, want)
		}
	}

	scan("first scan")
	// The late file's row comes from the deeper probe, so it describes the
	// video and is not probed again either.
	if stored, err := repo.GetByPath(ctx, late); err != nil || stored.CodecVideo != "h264" {
		t.Fatalf("late-video file: codec_video = %q, err = %v, want h264", stored.CodecVideo, err)
	}
	scoreRuns, lateRuns := probe.runs(t, score), probe.runs(t, late)
	scan("second scan")
	if got := probe.runs(t, score); got != scoreRuns {
		t.Fatalf("second scan probed the excluded file again: %d runs, want %d", got, scoreRuns)
	}
	if got := probe.runs(t, late); got != lateRuns {
		t.Fatalf("second scan probed the late-video file again: %d runs, want %d", got, lateRuns)
	}

	// Renamed, with ffprobe failing: the retired row must not move to the new
	// path, and the file must not be cataloged unprobed.
	if err := os.WriteFile(probe.failFlag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(filepath.Dir(score), "Score Album (2020) - 1080p.mkv")
	if err := os.Rename(score, renamed); err != nil {
		t.Fatal(err)
	}
	scan("scan after rename with a failing probe")
	if err := os.Remove(probe.failFlag); err != nil {
		t.Fatal(err)
	}
	scan("scan after rename with a working probe")
}

// TestScanFileRetiresFileWithoutVideoDB: a single-file scan retires the row of
// a file without video, and scanning it again, with a probe that now fails,
// neither restores it nor restarts its removal grace.
func TestScanFileRetiresFileWithoutVideoDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("no-video-file-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	repo := NewFileRepository(pool)
	score := writeNoVideoTestFile(t, root, "Score Album (2020)", "Score Album (2020).mkv")
	seedAudioOnlyMovieRow(t, repo, folderID, score)

	probe := writeNoVideoTestProbe(t)
	s := NewScanner(repo, probe.path, nil, 1, false, time.Hour)
	missingSince := func(step string) time.Time {
		t.Helper()
		if err := s.ScanFile(ctx, score, folder); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		stored, err := repo.GetByPath(ctx, score)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if stored.MissingSince == nil {
			t.Fatalf("%s: the audio-only file's row is still present", step)
		}
		return *stored.MissingSince
	}

	retiredAt := missingSince("first scan")
	if err := os.WriteFile(probe.failFlag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if again := missingSince("scan with a failing probe"); !again.Equal(retiredAt) {
		t.Fatalf("missing_since moved from %v to %v", retiredAt, again)
	}
}
