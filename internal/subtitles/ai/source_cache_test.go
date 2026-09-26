package ai

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const cachedSourceSRT = `1
00:00:01,000 --> 00:00:02,000
Bonjour

2
00:00:03,000 --> 00:00:04,000
Au revoir
`

// TestLoadSourceReusesCachedEmbeddedSubtitle covers #1099: translating an
// embedded text track reads the node's complete subtitle extract instead of
// demuxing the source again. The service's ffmpeg path does not exist, so any
// extraction attempt fails the test.
func TestLoadSourceReusesCachedEmbeddedSubtitle(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(source, []byte("not a real container"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := playback.NewSubtitleCache(func() string { return filepath.Join(dir, "transcode") })
	if _, err := cache.ExtractText(context.Background(), source, 1, "srt", func(context.Context) ([]byte, error) {
		return []byte(cachedSourceSRT), nil
	}); err != nil {
		t.Fatal(err)
	}

	file := &models.MediaFile{ID: 7, FilePath: source, SubtitleTracks: []models.SubtitleTrack{
		{Language: "eng", Codec: "subrip"},
		{Language: "fre", Codec: "subrip"},
	}}
	svc := NewService(context.Background(), Config{}, nil, nil, nil, nil, nil,
		runTranscribeFileResolver{file: file}, nil, filepath.Join(dir, "missing-ffmpeg"), nil, nil)
	svc.SetSubtitleCache(cache)

	cues, language, err := svc.loadSource(context.Background(), &Job{MediaFileID: file.ID, SourceIndex: 1})
	if err != nil {
		t.Fatalf("loadSource extracted instead of reusing the cache: %v", err)
	}
	if language != "fre" || len(cues) != 2 || cues[0].Lines[0] != "Bonjour" {
		t.Fatalf("language=%q cues=%+v", language, cues)
	}

	// Without the cache the same request has to demux, which fails here.
	svc.SetSubtitleCache(nil)
	if _, _, err := svc.loadSource(context.Background(), &Job{MediaFileID: file.ID, SourceIndex: 1}); err == nil {
		t.Fatal("uncached load did not attempt extraction")
	} else if errors.Is(err, ErrSourceUnsupported) {
		t.Fatalf("unexpected unsupported-source error: %v", err)
	}
}

// TestLoadSourceFillsCacheOnMiss: the first translation of an uncached track
// extracts it once and publishes the extract, so a later translation, even
// from another service sharing the cache, reads it without extracting.
func TestLoadSourceFillsCacheOnMiss(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(source, []byte("not a real container"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := playback.NewSubtitleCache(func() string { return filepath.Join(dir, "transcode") })
	file := &models.MediaFile{ID: 7, FilePath: source, SubtitleTracks: []models.SubtitleTrack{
		{Language: "eng", Codec: "subrip"},
		{Language: "fre", Codec: "subrip"},
	}}
	newService := func(extract func(context.Context, string, int) ([]byte, error)) *Service {
		svc := NewService(context.Background(), Config{}, nil, nil, nil, nil, nil,
			runTranscribeFileResolver{file: file}, nil, "", nil, nil)
		svc.SetSubtitleCache(cache)
		svc.extractEmbedded = extract
		return svc
	}

	calls := 0
	first := newService(func(_ context.Context, path string, ordinal int) ([]byte, error) {
		calls++
		if path != source || ordinal != 1 {
			t.Fatalf("extracted %s track %d, want %s track 1", path, ordinal, source)
		}
		return []byte(cachedSourceSRT), nil
	})
	for range 2 {
		cues, language, err := first.loadSource(context.Background(), &Job{MediaFileID: file.ID, SourceIndex: 1})
		if err != nil || language != "fre" || len(cues) != 2 {
			t.Fatalf("language=%q cues=%d err=%v", language, len(cues), err)
		}
	}
	if calls != 1 {
		t.Fatalf("extracted %d times, want 1", calls)
	}

	second := newService(func(context.Context, string, int) ([]byte, error) {
		return nil, errors.New("extracted again despite a cached source")
	})
	if _, _, err := second.loadSource(context.Background(), &Job{MediaFileID: file.ID, SourceIndex: 1}); err != nil {
		t.Fatal(err)
	}
}
