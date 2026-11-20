package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// staticDeviceCapabilitySource injects a fixed decode profile so the ranking
// path is exercised without a user store.
type staticDeviceCapabilitySource struct {
	caps plugins.DeviceCapabilities
}

func (s staticDeviceCapabilitySource) DeviceCapabilitiesFor(context.Context, string, string) (plugins.DeviceCapabilities, bool) {
	return s.caps, true
}

// A background probe that ends because the caller's budget fired must not
// release the active sticky pin: the verdict is unknown, and the inner probe
// may still complete under its own shared timeout.
func TestBackgroundProbeTransientTimeoutDoesNotUnpinActivePin(t *testing.T) {
	uri := "virtual://series/tt-bg-timeout/3/5?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	t.Cleanup(func() { virtualProbeFailures.clear(key) })

	persisted := false
	h := &PlaybackHandler{
		VirtualPlaybackSourceProber: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
			return nil, context.DeadlineExceeded
		},
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			persisted = true
			return 1, nil
		},
	}
	stickyKey := "sticky-transient-timeout"
	h.pinVirtualSticky(stickyKey, uri)
	file := &models.MediaFile{ID: 501, ContentID: "series-tt", EpisodeID: "ep-1", FilePath: uri, VirtualOwnerInstallationID: 5}
	cand := VirtualPlaybackStream{ID: "cand-1", URI: uri}

	h.probeVirtualSourceAndPersist(context.Background(), stickyKey, file, "http://provider.example/stream", *file, cand, noCandidateIdentity, 45, 5)

	if got := h.peekVirtualSticky(stickyKey); got != uri {
		t.Fatalf("transient timeout released the active pin: got %q, want %q", got, uri)
	}
	if got := virtualProbeFailures.count(key); got >= virtualProbeFailureRepeatThreshold {
		t.Fatalf("transient timeout counted %d failures, want fewer than %d", got, virtualProbeFailureRepeatThreshold)
	}
	if persisted {
		t.Fatal("a transient timeout must not persist evidence")
	}
}

// A context error is not a verdict about the candidate: the probe may have been
// canceled by playback start itself (the request went away, the attempt was
// superseded) and may still land in the cache under its own timeout. It must
// never arm the failure damper or consume the repeat budget, however often it
// repeats, and must not release the pin on its own.
func TestBackgroundProbeContextErrorDoesNotMarkDefinitiveFailure(t *testing.T) {
	uri := "virtual://series/tt-bg-context/3/5?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	t.Cleanup(func() { virtualProbeFailures.clear(key) })

	h := &PlaybackHandler{
		VirtualPlaybackSourceProber: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
			return nil, context.Canceled
		},
	}
	stickyKey := "sticky-context-cancel"
	h.pinVirtualSticky(stickyKey, uri)
	file := &models.MediaFile{ID: 502, ContentID: "series-tt", EpisodeID: "ep-1", FilePath: uri, VirtualOwnerInstallationID: 5}
	cand := VirtualPlaybackStream{ID: "cand-1", URI: uri}

	for attempt := 0; attempt < virtualProbeFailureRepeatThreshold+1; attempt++ {
		h.probeVirtualSourceAndPersist(context.Background(), stickyKey, file, "http://provider.example/stream", *file, cand, noCandidateIdentity, 0, 5)
		if got := h.peekVirtualSticky(stickyKey); got != uri {
			t.Fatalf("context error %d released the pin: got %q, want %q", attempt+1, got, uri)
		}
		if got := virtualProbeFailures.count(key); got != 0 {
			t.Fatalf("context error %d consumed %d failure marks, want 0", attempt+1, got)
		}
		if virtualProbeFailures.recent(key) {
			t.Fatalf("context error %d armed the failure damper", attempt+1)
		}
	}
}

// The probe-failure damper is advisory only for probing. It must never feed
// candidate ranking: a marked 4K candidate still outranks an SDR 1080p sibling
// for a 4K/HDR-capable device.
func TestProbeFailureMarkDoesNotChangeCandidateRanking(t *testing.T) {
	uri4k := "virtual://movie/tt-rank?result=4k"
	uri1080 := "virtual://movie/tt-rank?result=1080"
	key := virtualProbeFailureKey(uri4k, 5)
	virtualProbeFailures.clear(key)
	t.Cleanup(func() { virtualProbeFailures.clear(key) })
	virtualProbeFailures.mark(key)

	h := &PlaybackHandler{DeviceCapabilitySource: staticDeviceCapabilitySource{caps: plugins.DeviceCapabilities{
		CodecsVideo:   []string{"hevc"},
		CodecsAudio:   []string{"eac3"},
		Containers:    []string{"mkv"},
		MaxResolution: "2160p",
		HDR:           true,
	}}}
	streams := []VirtualPlaybackStream{
		{ID: "4k", URI: uri4k, Resolution: "2160p", CodecVideo: "hevc", CodecAudio: "eac3", Container: "mkv", HDR: "HDR"},
		{ID: "1080", URI: uri1080, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	ranked, _ := h.rankVirtualCandidatesForDevice(req, streams)
	if len(ranked) != 2 || ranked[0].URI != uri4k {
		t.Fatalf("probe-failure mark changed ranking: %#v", ranked)
	}
}

// The provider fetch happens before the background goroutine starts, so its
// duration must not consume the probe budget. The background budget is shrunk
// here to keep the test fast; the resolver blocks longer than it, and the probe
// still succeeds because the goroutine gets a fresh budget after the fetch.
func TestBackgroundProbeBudgetIndependentOfProviderFetch(t *testing.T) {
	uri := "virtual://movie/tt-bg-fetch?result=cand-1"
	stored := &models.MediaFile{
		ID:                         401,
		ContentID:                  "movie-1",
		FilePath:                   uri,
		Container:                  "virtual",
		VirtualOwnerInstallationID: 5,
	}
	lister := VirtualPlaybackStreamListerFunc(func(_ context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
		return []VirtualPlaybackStream{{ID: "cand-1", URI: uri, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv"}}, nil
	})
	saverDone := make(chan struct{}, 1)
	h := virtualProbeGateCandidateHandler(stored, lister,
		func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			f.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
			f.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "h264", "aac", "1080p", "mkv"
			return f, nil
		},
		func(_ context.Context, _ models.VirtualFilePersistArgs) (int64, error) {
			saverDone <- struct{}{}
			return 1, nil
		})
	// Shrink this handler's background probe budget instead of the package
	// global, which a watchdog goroutine leaked by an earlier test may still be
	// reading.
	h.backgroundProbeBudgetOverride = 300 * time.Millisecond
	// The provider fetch blocks for longer than the background budget. A naive
	// implementation that created the probe context before resolving would
	// leave the probe with no budget and no evidence would be persisted.
	h.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
		time.Sleep(2 * h.probeBackgroundBudget()) //nolint:forbidigo // The resolver must block longer than the probe budget; that overrun is the timing scenario under test.
		return "http://provider.example/stream?path=" + path, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	file := *stored
	resolved, err := h.resolveVirtualPlaybackSource(req, &file, "profile-1", true, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("resolveVirtualPlaybackSource error: %v", err)
	}
	if resolved.Provenance != ProbeProvenancePending {
		t.Fatalf("provenance = %q, want pending deferred probe", resolved.Provenance)
	}
	select {
	case <-saverDone:
	case <-time.After(4 * time.Second):
		t.Fatal("probe evidence was not persisted; the provider fetch consumed the probe budget")
	}
}

// The background probe runs under its own budget, which must cover the shared
// scanner probe timeout rather than the shorter interactive startup budget.
// Asserting the deadline keeps the test fast while still proving a slow probe
// (the incident's ~20s) is covered.
func TestBackgroundProbeBudgetCoversSlowProbe(t *testing.T) {
	uri := "virtual://movie/tt-bg-slow?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	t.Cleanup(func() { virtualProbeFailures.clear(key) })

	stored := &models.MediaFile{
		ID:                         402,
		ContentID:                  "movie-2",
		FilePath:                   uri,
		Container:                  "virtual",
		VirtualOwnerInstallationID: 5,
	}
	lister := VirtualPlaybackStreamListerFunc(func(_ context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
		return []VirtualPlaybackStream{{ID: "cand-1", URI: uri, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv"}}, nil
	})
	deadlineCh := make(chan time.Duration, 1)
	saverDone := make(chan struct{}, 1)
	h := virtualProbeGateCandidateHandler(stored, lister,
		func(ctx context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				deadlineCh <- 0
				return f, context.DeadlineExceeded
			}
			deadlineCh <- time.Until(deadline)
			f.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
			f.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "h264", "aac", "1080p", "mkv"
			return f, nil
		},
		func(_ context.Context, _ models.VirtualFilePersistArgs) (int64, error) {
			saverDone <- struct{}{}
			return 1, nil
		})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	file := *stored
	if _, err := h.resolveVirtualPlaybackSource(req, &file, "profile-1", true, nil, "", "", 0, false); err != nil {
		t.Fatalf("resolveVirtualPlaybackSource error: %v", err)
	}
	select {
	case <-saverDone:
	case <-time.After(4 * time.Second):
		t.Fatal("probe evidence was not persisted under the background budget")
	}
	remaining := <-deadlineCh
	if remaining <= virtualProbeBudget {
		t.Fatalf("background probe deadline %s did not exceed the interactive budget %s", remaining, virtualProbeBudget)
	}
	if virtualProbeFailures.recent(key) {
		t.Fatal("a successful slow probe left a failure mark")
	}
}

// The synchronous probe is bounded by the path that asked for it. A start
// answers inside virtualStartProbeBudget and lets the deferred probe verify;
// a synchronous replan keeps the background budget, because no deferred probe
// follows it. Regression: the start path used the 15s background budget, so an
// early Play sat on the provider byte source for 15s before planning.
func TestVirtualResolveProbeBudgetSelectsByPath(t *testing.T) {
	if got := virtualResolveProbeBudget(true); got != virtualStartProbeBudget {
		t.Fatalf("start-path probe budget = %s, want %s", got, virtualStartProbeBudget)
	}
	if got := virtualResolveProbeBudget(false); got != virtualProbeBudget {
		t.Fatalf("replan probe budget = %s, want %s", got, virtualProbeBudget)
	}
	if virtualStartProbeBudget >= virtualProbeBudget {
		t.Fatalf("start probe budget %s is not shorter than the background budget %s",
			virtualStartProbeBudget, virtualProbeBudget)
	}
}

// A start's alternate-version walk is still start-path work, so the substitute
// probe it runs must carry virtualStartProbeBudget; only a replan rehydration
// keeps the longer budget, because its response carries the probed inventory.
// Regression: the alternate walk re-entered the 15s budget, so a start that
// substituted a version still waited on a long probe (measured bound_ms=15000).
func TestVirtualAlternateProbeBudgetSelectsByPath(t *testing.T) {
	cases := []struct {
		name      string
		startPath bool
		budgetMS  int64
	}{
		{name: "start alternate walk", startPath: true, budgetMS: virtualStartProbeBudget.Milliseconds()},
		{name: "replan rehydration", startPath: false, budgetMS: virtualProbeBudget.Milliseconds()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := "virtual://movie/tt-alt-budget-" + strings.ReplaceAll(tc.name, " ", "-") + "?result=cand-1"
			key := virtualProbeFailureKey(uri, 5)
			virtualProbeFailures.clear(key)
			t.Cleanup(func() { virtualProbeFailures.clear(key) })

			// The probe's own bound is what the wait log publishes as bound_ms,
			// which is the signal the incident was diagnosed from.
			var logs strings.Builder
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			stored := &models.MediaFile{
				ID:                         431,
				ContentID:                  "movie-alt-budget",
				FilePath:                   uri,
				Container:                  "virtual",
				VirtualOwnerInstallationID: 5,
			}
			probed := make(chan struct{}, 1)
			lister := VirtualPlaybackStreamListerFunc(func(_ context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
				// No declared codecs or container: the candidate cannot skip the
				// probe, so the walk runs the synchronous probe whose bound the
				// wait log publishes.
				return []VirtualPlaybackStream{{ID: "cand-1", URI: uri}}, nil
			})
			h := virtualProbeGateCandidateHandler(stored, lister,
				func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
					probed <- struct{}{}
					f.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
					f.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2}}
					f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "h264", "aac", "1080p", "mkv"
					return f, nil
				},
				func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil })

			req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
			alternate := *stored
			if _, _, err := h.prepareVirtualAlternateFileV3(req, &alternate, "profile-1", tc.startPath); err != nil {
				t.Fatalf("prepareVirtualAlternateFileV3 error: %v", err)
			}
			select {
			case <-probed:
			case <-time.After(4 * time.Second):
				t.Fatal("alternate resolve never ran its synchronous probe")
			}

			want := fmt.Sprintf("bound_ms=%d", tc.budgetMS)
			var probeLine string
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "virtual resolve wait started") && strings.Contains(line, "wait_on=ffprobe") {
					probeLine = line
					break
				}
			}
			if probeLine == "" {
				t.Fatalf("alternate resolve logged no probe wait:\n%s", logs.String())
			}
			if !strings.Contains(probeLine, want) {
				t.Fatalf("alternate probe wait = %q, want %s", probeLine, want)
			}
		})
	}
}

// The start's alternate-version walk bounds each candidate's listing and its
// probe separately. One deadline for both meant a listing that spent the
// candidate's window left the probe with nothing, so an alternate was judged
// on declared or partial evidence and the start-path probe budget never
// applied inside the walk.
func TestVirtualAlternateWalkBudgetsContainTheProbe(t *testing.T) {
	if got, want := virtualStartVersionFallbackCandidateBudget, virtualStartVersionFallbackListingBudget+virtualStartProbeBudget; got != want {
		t.Fatalf("per-candidate walk budget = %s, want listing %s + start probe %s = %s",
			got, virtualStartVersionFallbackListingBudget, virtualStartProbeBudget, want)
	}
	if virtualStartVersionFallbackDecisionBudget < virtualStartVersionFallbackCandidateBudget {
		t.Fatalf("walk decision budget %s does not contain one candidate budget %s",
			virtualStartVersionFallbackDecisionBudget, virtualStartVersionFallbackCandidateBudget)
	}
	// The walk runs inside the start phase, so its whole worst case must stay a
	// fraction of that phase rather than approach it.
	if virtualStartVersionFallbackDecisionBudget >= virtualStartPhaseBudget/2 {
		t.Fatalf("walk decision budget %s is not well below the %s start phase",
			virtualStartVersionFallbackDecisionBudget, virtualStartPhaseBudget)
	}
}

// The functional half of the rule above: the walk passes the listing budget as
// a listing-only deadline, so a slow listing leaves the probe its full
// start-path bound instead of shrinking it.
func TestVirtualAlternateWalkProbeKeepsTheStartBudgetAfterSlowListing(t *testing.T) {
	const listingBudget = 120 * time.Millisecond
	previous := virtualStartVersionFallbackListingBudget
	virtualStartVersionFallbackListingBudget = listingBudget
	t.Cleanup(func() { virtualStartVersionFallbackListingBudget = previous })

	uri := "virtual://movie/tt-walk-probe-budget?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	t.Cleanup(func() { virtualProbeFailures.clear(key) })

	stored := &models.MediaFile{
		ID: 432, ContentID: "movie-walk-probe-budget", FilePath: uri,
		Container: "virtual", VirtualOwnerInstallationID: 5,
	}
	// No declared codecs or container: the candidate cannot take a fast path,
	// so the walk runs the synchronous probe whose deadline is under test. The
	// listing spends most of its own budget before answering.
	lister := VirtualPlaybackStreamListerFunc(func(ctx context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
		select {
		case <-time.After(listingBudget * 3 / 4):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []VirtualPlaybackStream{{ID: "cand-1", URI: uri}}, nil
	})
	probeDeadline := make(chan time.Duration, 1)
	h := virtualProbeGateCandidateHandler(stored, lister,
		func(ctx context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			if deadline, ok := ctx.Deadline(); ok {
				probeDeadline <- time.Until(deadline)
			} else {
				probeDeadline <- 0
			}
			f.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
			f.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "h264", "aac", "1080p", "mkv"
			return f, nil
		},
		func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	ctx, cancel := context.WithTimeout(req.Context(), virtualStartVersionFallbackCandidateBudget)
	defer cancel()
	req = req.WithContext(ctx)
	if _, err := h.resolveVirtualPlaybackSource(req, stored, "profile-1", true, nil, "", "", 0, false,
		virtualResolveOptionsV3{
			startPathProbe:      true,
			bypassProviderFloor: true,
			listingBudget:       listingBudget,
		}); err != nil {
		t.Fatalf("alternate resolve: %v", err)
	}
	select {
	case remaining := <-probeDeadline:
		// The probe must keep (nearly all of) its own start-path bound: only
		// the time spent between the listing ending and the probe starting is
		// missing. A shared candidate deadline left it under one listing budget.
		if remaining < virtualStartProbeBudget/2 {
			t.Fatalf("probe deadline had %s left, want close to the %s start-path budget (listing budget was %s)",
				remaining, virtualStartProbeBudget, listingBudget)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the alternate resolve never ran its synchronous probe")
	}
}

// A probe that exceeds its bound inside the walk-shaped resolve still answers
// with the candidate's declared metadata and a playable URL; the walk's budget
// change must not turn a slow probe into a failed start.
func TestVirtualAlternateWalkProbeTimeoutServesDeclaredMetadata(t *testing.T) {
	uri := "virtual://movie/tt-walk-probe-timeout?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	t.Cleanup(func() { virtualProbeFailures.clear(key) })

	stored := &models.MediaFile{
		ID: 433, ContentID: "movie-walk-probe-timeout", FilePath: uri,
		Container: "virtual", VirtualOwnerInstallationID: 5,
	}
	lister := VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
		return []VirtualPlaybackStream{{ID: "cand-1", URI: uri}}, nil
	})
	h := virtualProbeGateCandidateHandler(stored, lister,
		func(_ context.Context, _ string, _ *models.MediaFile) (*models.MediaFile, error) {
			return nil, context.DeadlineExceeded
		},
		func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	resolved, err := h.resolveVirtualPlaybackSource(req, stored, "profile-1", true, nil, "", "", 0, false,
		virtualResolveOptionsV3{
			startPathProbe:      true,
			bypassProviderFloor: true,
			listingBudget:       virtualStartVersionFallbackListingBudget,
		})
	if err != nil {
		t.Fatalf("a probe timeout must not fail the alternate resolve: %v", err)
	}
	if resolved.URL == "" {
		t.Fatal("declared fallback must still return a playable URL")
	}
	if resolved.File == nil || resolved.ProbeSucceeded {
		t.Fatalf("resolved file = %v, probe_succeeded = %v; want the candidate's declared metadata, not a probed inventory",
			resolved.File != nil, resolved.ProbeSucceeded)
	}
	if resolved.Provenance != ProbeProvenanceDeclared && resolved.Provenance != ProbeProvenanceFailed {
		t.Fatalf("provenance = %q, want the declared-metadata fallback (declared or failed, never verified)", resolved.Provenance)
	}
}
