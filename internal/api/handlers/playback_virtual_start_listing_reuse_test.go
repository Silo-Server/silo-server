package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// A detail read pays for one provider listing. The play that follows must
// reuse it instead of paying for the same candidates twice: the discovery
// service hands the listing it just persisted to the playback handler, whose
// start consults it before deciding to list. Before this, discovery listed
// once and the first start listed again (per candidate, and again for the
// neutral placeholder row).
func TestVirtualStartReusesTheDiscoveryListingInsteadOfListingAgain(t *testing.T) {
	const contentID = "movie-listing-reuse"
	neutral := "virtual://movie/listing-reuse"
	candidateURI := neutral + "?result=c1"

	var lists atomic.Int32
	lister := VirtualPlaybackStreamListerFunc(func(_ context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
		lists.Add(1)
		return []VirtualPlaybackStream{{
			ID: "c1", URI: candidateURI, ProviderURL: "https://provider.example/listing-reuse",
			OwnerInstallationID: 5, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv",
		}}, nil
	})
	source := &models.MediaFile{
		ID: 900, ContentID: contentID, FilePath: neutral, Container: "virtual",
		MediaFolderID: 3, VirtualOwnerInstallationID: 5,
	}
	// The row ReplaceVirtualCandidates writes: declared video metadata, the
	// virtual container sentinel, and no probe stamp.
	persisted := &models.MediaFile{
		ID: 901, ContentID: contentID, FilePath: candidateURI, Container: "virtual", MediaFolderID: 3,
		VirtualOwnerInstallationID: 5, ProbeSource: "virtual", Resolution: "1080p",
		CodecVideo: "h264", CodecAudio: "aac", ResolvedURL: "https://provider.example/listing-reuse",
		UpdatedAt: time.Now(), FileSize: 100,
	}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		BestResultCache:             NewVirtualBestResultCache(time.Minute, 8),
		VirtualPlaybackStreamLister: lister,
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
			return "http://127.0.0.1:8080/stream?path=" + path, nil
		}),
		VirtualFileLookup:           func(context.Context, string) (*models.MediaFile, error) { return persisted, nil },
		VirtualPlaybackSourceProber: prober.probe,
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			return 1, nil
		},
	}
	detail := &catalog.WatchDetail{
		Type: itemTypeMovie,
		Versions: []catalog.FileVersion{{
			FileID: 901, FilePath: candidateURI, Resolution: "1080p",
			CodecVideo: "h264", CodecAudio: "aac", Container: "virtual",
		}},
	}
	refresh := &VirtualCandidatesRefreshService{
		Detail:       &fakeRefreshDetail{detail: detail},
		ListCached:   lister,
		ListFresh:    lister,
		Persist:      func(context.Context, *models.MediaFile, []VirtualPlaybackStream) error { return nil },
		ContentFiles: func(context.Context, string) ([]*models.MediaFile, error) { return []*models.MediaFile{source}, nil },
		SeedListing:  h.SeedVirtualListing,
	}
	if _, err := refresh.DiscoverVirtualCandidates(context.Background(), 1, "profile-1", contentID, catalog.AccessFilter{}, VirtualDiscoveryDetail); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if got := lists.Load(); got != 1 {
		t.Fatalf("discovery listed %d time(s), want 1", got)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	// The persisted concrete candidate the picker's default row plays.
	if _, err := h.resolveVirtualPlaybackSource(req, persisted, "profile-1", true, nil, "", "", 0, false); err != nil {
		t.Fatalf("concrete start resolve: %v", err)
	}
	if got := lists.Load(); got != 1 {
		t.Fatalf("listing count after the concrete start = %d, want 1 (discovery's listing reused)", got)
	}

	// The provider-neutral placeholder row (play_content_id) takes the same
	// re-listing gate and must reuse the same entry.
	neutralRow := *source
	if _, err := h.resolveVirtualPlaybackSource(req, &neutralRow, "profile-1", true, nil, "", "", 0, false); err != nil {
		t.Fatalf("neutral start resolve: %v", err)
	}
	if got := lists.Load(); got != 1 {
		t.Fatalf("listing count after the neutral start = %d, want 1 (discovery's listing reused)", got)
	}

	// A second play of the same item performs no provider listing either.
	if _, err := h.resolveVirtualPlaybackSource(req, persisted, "profile-1", true, nil, "", "", 0, false); err != nil {
		t.Fatalf("second concrete start resolve: %v", err)
	}
	if got := lists.Load(); got != 1 {
		t.Fatalf("listing count after a second start = %d, want 1", got)
	}
}

// A forced relist, an explicit exclusion, and an indicted requested row each
// need the provider's current answer; the seeded entry must not stand in for
// that list.
func TestVirtualStartStillListsWhenTheProviderAnswerMustBeFresh(t *testing.T) {
	const contentID = "movie-listing-fresh"
	neutral := "virtual://movie/listing-fresh"
	candidateURI := neutral + "?result=c1"
	var lists atomic.Int32
	lister := VirtualPlaybackStreamListerFunc(func(_ context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
		lists.Add(1)
		return []VirtualPlaybackStream{{
			ID: "c1", URI: candidateURI, OwnerInstallationID: 5,
			Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv",
		}}, nil
	})
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		BestResultCache:             NewVirtualBestResultCache(time.Minute, 8),
		VirtualPlaybackStreamLister: lister,
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
			return "http://127.0.0.1:8080/stream?path=" + path, nil
		}),
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) { return nil, nil },
	}
	row := &models.MediaFile{
		ID: 902, ContentID: contentID, FilePath: candidateURI, Container: "virtual",
		MediaFolderID: 3, VirtualOwnerInstallationID: 5, Resolution: "1080p",
		CodecVideo: "h264", CodecAudio: "aac",
	}
	h.SeedVirtualListing(&models.MediaFile{ID: 900, ContentID: contentID, FilePath: neutral, VirtualOwnerInstallationID: 5}, []VirtualPlaybackStream{{
		ID: "c1", URI: candidateURI, OwnerInstallationID: 5,
		Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv",
	}})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	if _, err := h.resolveVirtualPlaybackSource(req, row, "profile-1", true, nil, "", "", 0, true /* forceRelist */); err != nil {
		t.Fatalf("forced relist resolve: %v", err)
	}
	if got := lists.Load(); got != 1 {
		t.Fatalf("forced relist listed %d time(s), want 1 (the seeded entry must not answer a relist)", got)
	}

	failed := *row
	failed.FailedAt = timePtr(time.Now())
	if _, err := h.resolveVirtualPlaybackSource(req, &failed, "profile-1", true, nil, "", "", 0, false); err != nil {
		t.Logf("indicted-row resolve error (rotation path): %v", err)
	}
	if got := lists.Load(); got < 2 {
		t.Fatalf("listing count for an indicted row = %d, want at least 2 (rotation needs a fresh list)", got)
	}
}

// A start no longer cancels the probe a detail read scheduled. Every probe
// goes through the shared VirtualProbeCache, whose single-flight makes the
// start join the chain's in-flight enumeration: two callers, one ffprobe. The
// chain then lands its durable evidence instead of being thrown away.
func TestVirtualStartJoinsTheLivePrePlayProbe(t *testing.T) {
	row := probeChainRow(950, "virtual://movie/probe-join?result=c1", nil)
	row.ContentID = probeChainTestContent
	source := probeChainSourceRow()

	var cacheCalls atomic.Int32
	var underlying atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	probeFn := func(_ context.Context, sourceURL string, f *models.MediaFile) (*models.MediaFile, error) {
		underlying.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		probed := *f
		probed.FilePath = sourceURL
		probed.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
		probed.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2, Language: "eng"}}
		probed.CodecVideo, probed.CodecAudio, probed.Resolution, probed.Container = "h264", "aac", "1080p", "mkv"
		probed.Duration = 5400
		return &probed, nil
	}
	cache := scanner.NewVirtualProbeCache(time.Minute, 8)

	target := &blockedDefaultTarget{open: make(chan struct{}), row: row}
	target.releaseRow()
	h := &PlaybackHandler{
		ServiceContext:     context.Background(),
		DefaultProbeTarget: target,
		VirtualPlaybackSourceProberWithHeaders: func(ctx context.Context, sourceURL string, f *models.MediaFile, _ map[string]string) (*models.MediaFile, error) {
			cacheCalls.Add(1)
			return cache.Probe(ctx, sourceURL, f, probeFn)
		},
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(_ context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
			return probeChainStreams(row), nil
		}),
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(_ context.Context, _ string, _ int, _ string, _ int) (string, error) {
			// The same provider URL the chain's stored row/listing names, so
			// both callers ask the probe cache about the same bytes.
			return probeChainProviderURL(row), nil
		}),
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) { return row, nil },
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			return 1, nil
		},
	}

	h.ScheduleDefaultFileProbe(probeChainTestContent, source, probeChainStreams(row), catalog.AccessFilter{})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the scheduled probe chain never entered its enumeration")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	resolved := make(chan struct{})
	var resolveErr error
	go func() {
		defer close(resolved)
		_, resolveErr = h.resolveVirtualPlaybackSource(req, row, "profile-1", true, nil, "", "", 0, false)
	}()

	// Wait until the start has also reached the prober seam: from here both
	// callers are waiting on the same single-flight enumeration.
	probeChainWaitFor(t, 2, func() int { return int(cacheCalls.Load()) }, "probe callers")
	close(release)
	select {
	case <-resolved:
	case <-time.After(5 * time.Second):
		t.Fatal("the start never returned after the shared probe completed")
	}
	if resolveErr != nil {
		t.Fatalf("start resolve: %v", resolveErr)
	}
	h.probeChainWG.Wait()
	if got := underlying.Load(); got != 1 {
		t.Fatalf("underlying probes = %d, want 1 (the start must join the live chain's probe, not cancel and redo it)", got)
	}
}
