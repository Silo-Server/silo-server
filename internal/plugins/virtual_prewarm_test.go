package plugins

import (
	"testing"
	"time"
)

func TestPrewarmCacheGetSet(t *testing.T) {
	cache := NewPrewarmCache(4, time.Hour)
	got := cache.Get("content-1", "profile-1")
	if got != nil {
		t.Fatalf("expected nil for missing entry, got %+v", got)
	}
	want := &PrewarmResult{ResolvedURL: "https://cdn.example/stream", StreamURI: "virtual://movie/1?result=abc", ResolvedAt: time.Now()}
	cache.Set("content-1", "profile-1", want)
	got = cache.Get("content-1", "profile-1")
	if got == nil || got.StreamURI != want.StreamURI {
		t.Fatalf("expected cached result, got %+v", got)
	}
	if got.ExpiresAt.IsZero() {
		t.Fatal("expected ExpiresAt to be set by Set")
	}
}

func TestPrewarmCacheEvictsOldestWhenFull(t *testing.T) {
	cache := NewPrewarmCache(2, time.Hour)
	now := time.Now()
	cache.Set("c1", "p1", &PrewarmResult{ResolvedAt: now})
	cache.Set("c2", "p1", &PrewarmResult{ResolvedAt: now.Add(time.Second)})
	cache.Set("c3", "p1", &PrewarmResult{ResolvedAt: now.Add(2 * time.Second)})
	if cache.Get("c1", "p1") != nil {
		t.Fatal("expected oldest entry to be evicted when the cache is full")
	}
	if cache.Get("c2", "p1") == nil || cache.Get("c3", "p1") == nil {
		t.Fatal("expected newer entries to survive eviction")
	}
}

func TestPrewarmServiceGetNilSafe(t *testing.T) {
	var svc *PrewarmService
	if got := svc.Get("c1", "p1"); got != nil {
		t.Fatalf("expected nil from nil service, got %+v", got)
	}
	svc = NewPrewarmService(nil, NewPrewarmCache(2, time.Hour), nil, func() bool { return true })
	if got := svc.Get("c1", "p1"); got != nil {
		t.Fatalf("expected nil from empty cache, got %+v", got)
	}
}

func TestSelectBestCandidateForDevice(t *testing.T) {
	candidates := []VirtualPlaybackStream{
		{URI: "virtual://m/1", Resolution: "2160p", CodecVideo: "h264", HDR: "hdr10", Container: "mkv"},
		{URI: "virtual://m/2", Resolution: "1080p", CodecVideo: "hevc", Container: "mkv"},
		{URI: "virtual://m/3", Resolution: "720p", CodecVideo: "hevc", Container: "mkv"},
	}
	device := DeviceCapabilities{
		CodecsVideo:   []string{"hevc"},
		Containers:    []string{"mkv"},
		MaxResolution: "1080p",
	}
	best := selectBestCandidateForDevice(candidates, device)
	if best != 1 {
		t.Fatalf("expected 1080p hevc candidate, got index %d (%+v)", best, candidates[best])
	}

	// No device profile: fall back to the first candidate.
	if got := selectBestCandidateForDevice(candidates, DeviceCapabilities{}); got != 0 {
		t.Fatalf("expected first candidate without device info, got %d", got)
	}
}

func TestPrewarmServiceGetExpiredReturnsNil(t *testing.T) {
	cache := NewPrewarmCache(2, 5*time.Millisecond)
	svc := NewPrewarmService(nil, cache, nil, func() bool { return true })
	cache.Set("c1", "p1", &PrewarmResult{StreamURI: "virtual://m/1", ResolvedAt: time.Now()})
	if got := svc.Get("c1", "p1"); got == nil {
		t.Fatal("expected entry before expiry")
	}
	time.Sleep(10 * time.Millisecond)
	if got := svc.Get("c1", "p1"); got != nil {
		t.Fatalf("expected expired entry to be dropped, got %+v", got)
	}
}
