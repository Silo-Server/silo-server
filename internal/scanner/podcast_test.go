package scanner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestParsePodcastShow(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parsePodcastShow(ctx, ffprobePath, "testdata/podcast_fixtures/show_a")
	if err != nil {
		t.Fatalf("parsePodcastShow: %v", err)
	}
	if got.Title != "Show A" {
		t.Errorf("Title = %q, want %q", got.Title, "Show A")
	}
	if got.Author != "Show A Host" {
		t.Errorf("Author = %q, want %q", got.Author, "Show A Host")
	}
	if got.Year != 2024 {
		t.Errorf("Year = %d, want 2024", got.Year)
	}
	if len(got.Episodes) != 3 {
		t.Fatalf("got %d episodes, want 3", len(got.Episodes))
	}
	for i, ep := range got.Episodes {
		wantTrack := i + 1
		if ep.Track != wantTrack {
			t.Errorf("episode %d Track = %d, want %d", i, ep.Track, wantTrack)
		}
	}
}

func TestPodcastIdentityConfidenceReflectsMetadataCompleteness(t *testing.T) {
	show := &parsedPodcastShow{Title: "Tagged Show", Author: "Host", Year: 2024}
	episode := parsedPodcastEpisode{Title: "Episode", Track: 3}
	if got := podcastIdentityConfidence(show, episode); got != "high" {
		t.Fatalf("complete metadata confidence = %q, want high", got)
	}

	show = &parsedPodcastShow{Title: "Tagged Show"}
	episode = parsedPodcastEpisode{Title: "Episode"}
	if got := podcastIdentityConfidence(show, episode); got != "medium" {
		t.Fatalf("partial metadata confidence = %q, want medium", got)
	}

	show = &parsedPodcastShow{}
	episode = parsedPodcastEpisode{}
	if got := podcastIdentityConfidence(show, episode); got != "low" {
		t.Fatalf("empty metadata confidence = %q, want low", got)
	}
}

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

func TestResolvePodcastMediaItemReusesRootScopedContentID(t *testing.T) {
	finder := &fakeRootContentFinder{contentID: "podcast-root-id"}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolvePodcastMediaItem(
		context.Background(),
		finder,
		writer,
		8,
		"/library/Same Show",
		&parsedPodcastShow{Title: "Same Show", Year: 0, Author: "Host A"},
	)
	if err != nil {
		t.Fatalf("resolvePodcastMediaItem: %v", err)
	}
	if got != "podcast-root-id" {
		t.Fatalf("contentID = %q, want root-scoped id", got)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("unexpected item upsert for existing root: %d", len(writer.upserts))
	}
}

func TestResolvePodcastMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolvePodcastMediaItem(
		context.Background(),
		finder,
		writer,
		8,
		"/library/Another Same Show",
		&parsedPodcastShow{Title: "Same Show", Year: 0, Author: "Host B"},
	)
	if err != nil {
		t.Fatalf("resolvePodcastMediaItem: %v", err)
	}
	if got == "" {
		t.Fatal("contentID is empty")
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(writer.upserts))
	}
	if writer.upserts[0].ContentID != got || writer.upserts[0].Type != "podcast" {
		t.Fatalf("upserted item = %+v, contentID %q", writer.upserts[0], got)
	}
}

func TestResolvePodcastMediaItemPropagatesRootLookupError(t *testing.T) {
	wantErr := errors.New("root lookup failed")
	finder := &fakeRootContentFinder{err: wantErr}
	writer := &fakeFilesystemItemWriter{}

	_, err := resolvePodcastMediaItem(
		context.Background(),
		finder,
		writer,
		8,
		"/library/Show",
		&parsedPodcastShow{Title: "Show"},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(writer.upserts))
	}
}
