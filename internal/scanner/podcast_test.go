package scanner

import (
	"context"
	"errors"
	"os"

	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestScanPodcastFolderReturnsErrorWhenEveryReconcileFails(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "bad-show")
	if err := os.Mkdir(showDir, 0o755); err != nil {
		t.Fatalf("mkdir show dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "episode.mp3"), []byte("not real audio"), 0o644); err != nil {
		t.Fatalf("write fake audio: %v", err)
	}

	s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
	err := s.ScanPodcastFolder(context.Background(), &models.MediaFolder{ID: 43, Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanPodcastFolder returned nil, want aggregate failure")
	}
	if !strings.Contains(err.Error(), "folder_id=43") {
		t.Fatalf("error = %q, want folder id", err)
	}
}

// podcastProbeFixture writes a one-episode show whose file holds the ffprobe
// output for a probed duration, and returns the show folder and episode path.
func podcastProbeFixture(t *testing.T, dir, duration string) (string, string) {
	t.Helper()
	showDir := filepath.Join(dir, "Show")
	if err := os.Mkdir(showDir, 0o755); err != nil {
		t.Fatalf("mkdir show: %v", err)
	}
	path := filepath.Join(showDir, "01.mp3")
	if err := os.WriteFile(path, []byte(podcastProbeJSON(duration)), 0o644); err != nil {
		t.Fatalf("write episode: %v", err)
	}
	return showDir, path
}

func podcastProbeJSON(duration string) string {
	return `{"format":{"format_name":"mp3","duration":"` + duration + `","tags":{"album":"Show","title":"One","track":"1"}},` +
		`"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3"}]}`
}

func TestParsePodcastShowReprobesAFileReplacedDuringTheProbe(t *testing.T) {
	dir := t.TempDir()
	ffprobePath := filepath.Join(dir, "ffprobe")
	// Prints the probe stored in the file. The first time, it then renames a
	// waiting replacement over the file with the same size and mtime, as an
	// atomic replacement written while ffprobe reads the file would.
	writeFakeTool(t, ffprobePath, "#!/bin/sh\nfor last; do :; done\ncat \"$last\"\n"+
		"if [ -e \"$last.next\" ]; then touch -r \"$last\" \"$last.next\"; mv \"$last.next\" \"$last\"; fi\n")
	showDir, path := podcastProbeFixture(t, dir, "61")
	if err := os.WriteFile(path+".next", []byte(podcastProbeJSON("62")), 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	got, err := parsePodcastShow(context.Background(), ffprobePath, showDir, nil)
	if err != nil {
		t.Fatalf("parsePodcastShow: %v", err)
	}
	if len(got.Episodes) != 1 {
		t.Fatalf("got %d episodes, want 1", len(got.Episodes))
	}
	ep := got.Episodes[0]
	if ep.Probe == nil || ep.Probe.Duration != 62 {
		t.Fatalf("probe = %+v, want the replacement's 62-second probe", ep.Probe)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat episode: %v", err)
	}
	if ep.Size != after.Size() || !ep.ModTime.Equal(normalizeFileModifiedAt(after.ModTime())) {
		t.Fatalf("recorded size %d mtime %v, want the replacement's %d %v",
			ep.Size, ep.ModTime, after.Size(), normalizeFileModifiedAt(after.ModTime()))
	}
}

func TestParsePodcastShowSkipsAFileStillChanging(t *testing.T) {
	dir := t.TempDir()
	ffprobePath := filepath.Join(dir, "ffprobe")
	// Prints the probe stored in the file, then grows a "-grow" file, as a
	// download still being written would. A "-partial" file grows too, but
	// can't be probed yet, like an M4A whose index isn't written.
	writeFakeTool(t, ffprobePath, "#!/bin/sh\nfor last; do :; done\ncat \"$last\"\n"+
		"case \"$last\" in *-grow.mp3) printf ' ' >> \"$last\";; *-partial.mp3) printf ' ' >> \"$last\"; exit 1;; esac\n")
	showDir, stable := podcastProbeFixture(t, dir, "61")
	if err := os.WriteFile(filepath.Join(showDir, "02-grow.mp3"), []byte(podcastProbeJSON("62")), 0o644); err != nil {
		t.Fatalf("write growing episode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "03-partial.mp3"), []byte(podcastProbeJSON("63")), 0o644); err != nil {
		t.Fatalf("write partial episode: %v", err)
	}

	got, err := parsePodcastShow(context.Background(), ffprobePath, showDir, nil)
	if err != nil {
		t.Fatalf("parsePodcastShow: %v", err)
	}
	if len(got.Episodes) != 1 || got.Episodes[0].Path != stable {
		t.Fatalf("episodes = %+v, want only the stable %s", got.Episodes, stable)
	}

	// A show whose only episodes are still changing fails for this scan.
	if err := os.Remove(stable); err != nil {
		t.Fatalf("remove stable episode: %v", err)
	}
	if _, err := parsePodcastShow(context.Background(), ffprobePath, showDir, nil); !errors.Is(err, errPodcastFileChanging) {
		t.Fatalf("parsePodcastShow error = %v, want errPodcastFileChanging", err)
	}
}
