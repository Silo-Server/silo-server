package scanner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestVirtualProbeCacheSeparatesResolvedQueriesAndReturnsClones(t *testing.T) {
	cache := NewVirtualProbeCache(time.Minute, 4)
	file := &models.MediaFile{ID: 7, FilePath: "virtual://movie/tt7?profile=1080p"}
	var calls atomic.Int32
	probe := func(_ context.Context, _ string, input *models.MediaFile) (*models.MediaFile, error) {
		calls.Add(1)
		result := *input
		result.CodecAudio = "aac"
		result.AudioTracks = []models.AudioTrack{{Codec: "aac"}}
		return &result, nil
	}
	first, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=one", file, probe)
	if err != nil {
		t.Fatal(err)
	}
	first.CodecAudio = "mutated"
	second, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=two", file, probe)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || second.CodecAudio != "aac" {
		t.Fatalf("calls=%d cached codec=%q", calls.Load(), second.CodecAudio)
	}
}

func TestVirtualProbeCacheReusesRotatingCredentialsForExactResult(t *testing.T) {
	cache := NewVirtualProbeCache(time.Minute, 4)
	file := &models.MediaFile{ID: 7, FilePath: "virtual://movie/tt7?profile=1080p&result=stable-result", VirtualOwnerInstallationID: 9}
	var calls atomic.Int32
	probe := func(_ context.Context, _ string, input *models.MediaFile) (*models.MediaFile, error) {
		calls.Add(1)
		result := *input
		result.CodecAudio = "aac"
		return &result, nil
	}
	if _, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=one", file, probe); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=two", file, probe); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("probe calls=%d, want 1 for the same exact result", calls.Load())
	}
	if _, err := cache.Probe(context.Background(), "https://provider.example/different.mkv?token=three", file, probe); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("probe calls=%d, want 2 after provider path changed", calls.Load())
	}
}

func TestVirtualProbeCacheIsBounded(t *testing.T) {
	cache := NewVirtualProbeCache(time.Minute, 2)
	probe := func(_ context.Context, _ string, input *models.MediaFile) (*models.MediaFile, error) {
		return input, nil
	}
	for id := 1; id <= 3; id++ {
		file := &models.MediaFile{ID: id, FilePath: "virtual://movie/tt"}
		if _, err := cache.Probe(context.Background(), "https://provider.example/video", file, probe); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 2 {
		t.Fatalf("cache entries=%d, want 2", len(cache.entries))
	}
}
