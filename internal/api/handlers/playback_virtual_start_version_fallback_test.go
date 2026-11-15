package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

// TestResolveVirtualPlaybackSourceUserRelinkBypassesFloor pins behavior 1: a
// deliberate user relink (force_relink) marks the resolve as an outage re-list
// so the provider re-lists past the fresh-serve floor and the
// provider-failure fail-fast. An automatic resolve and a session-bound rotation
// are server-initiated and stay on the floor.
func TestResolveVirtualPlaybackSourceUserRelinkBypassesFloor(t *testing.T) {
	newHandler := func(seen *bool) *PlaybackHandler {
		return &PlaybackHandler{
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "", errors.New("simple resolver must not be used when the detailed resolver is set")
			}),
			VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(ctx context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				*seen = virtuallibrary.ProviderOutageRelistFromContext(ctx)
				return ResolvedVirtualMedia{URL: "https://cdn.example/x.mkv", URI: uri, CandidateID: "cand-x", OwnerID: 5}, nil
			}),
		}
	}
	file := &models.MediaFile{ID: 1, ContentID: "movie-relink", FilePath: "virtual://movie/movie-relink?result=A", VirtualOwnerInstallationID: 5}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)

	userSeen := false
	if _, err := newHandler(&userSeen).resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, true, virtualResolveOptionsV3{sessionBound: false}); err != nil {
		t.Fatalf("user relink resolve: %v", err)
	}
	if !userSeen {
		t.Fatal("a user force_relink must mark the resolve as an outage re-list so it bypasses the floor and provider-failure fail-fast")
	}

	autoSeen := false
	if _, err := newHandler(&autoSeen).resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false}); err != nil {
		t.Fatalf("automatic resolve: %v", err)
	}
	if autoSeen {
		t.Fatal("an automatic resolve must stay on the floor")
	}

	rotationSeen := false
	if _, err := newHandler(&rotationSeen).resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, true, virtualResolveOptionsV3{sessionBound: true}); err != nil {
		t.Fatalf("session-bound rotation resolve: %v", err)
	}
	if rotationSeen {
		t.Fatal("a session-bound rotation is server-initiated and must stay on the floor")
	}
}

// TestResolveVirtualPlaybackSourceRecoveryBypassesFloor pins behavior 2: a
// declared recovery — a candidate rotation or an automatic alternate-version
// fallback — re-lists past the floor and hands the resolver a forced re-list,
// while an ordinary automatic resolve stays on the floor.
func TestResolveVirtualPlaybackSourceRecoveryBypassesFloor(t *testing.T) {
	type resolveCall struct {
		relist      bool
		forceRelist bool
	}
	newHandler := func(seen *resolveCall) *PlaybackHandler {
		return &PlaybackHandler{
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "", errors.New("simple resolver must not be used when the detailed resolver is set")
			}),
			VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(ctx context.Context, uri string, _ int, _ int, _ string, forceRefresh bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				seen.relist = virtuallibrary.ProviderOutageRelistFromContext(ctx)
				seen.forceRelist = forceRefresh
				return ResolvedVirtualMedia{URL: "https://cdn.example/x.mkv", URI: uri, CandidateID: "cand-x", OwnerID: 5}, nil
			}),
		}
	}
	file := &models.MediaFile{ID: 1, ContentID: "movie-recovery", FilePath: "virtual://movie/movie-recovery?result=A", VirtualOwnerInstallationID: 5}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)

	rotation := resolveCall{}
	if _, err := newHandler(&rotation).resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{rotateCandidates: true, sessionBound: false}); err != nil {
		t.Fatalf("candidate rotation resolve: %v", err)
	}
	if !rotation.relist || !rotation.forceRelist {
		t.Fatalf("candidate rotation = %+v, want an outage re-list with a forced provider re-list", rotation)
	}

	fallback := resolveCall{}
	if _, err := newHandler(&fallback).resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false, bypassProviderFloor: true}); err != nil {
		t.Fatalf("alternate-version fallback resolve: %v", err)
	}
	if !fallback.relist || !fallback.forceRelist {
		t.Fatalf("alternate-version fallback = %+v, want an outage re-list with a forced provider re-list", fallback)
	}

	auto := resolveCall{}
	if _, err := newHandler(&auto).resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false}); err != nil {
		t.Fatalf("automatic resolve: %v", err)
	}
	if auto.relist || auto.forceRelist {
		t.Fatalf("ordinary automatic resolve = %+v, want it to stay on the floor", auto)
	}
}

// A start that carries an offered candidate must hand that candidate id to the
// resolver and refuse a different one. The client's selected track ordinals
// belong to the offered release, so substituting another release would apply
// them to different bytes.
func TestResolveVirtualStartBindsOfferedCandidate(t *testing.T) {
	const offeredURI = "virtual://movie/movie-offered?result=C"
	primary := &models.MediaFile{ID: 1, ContentID: "movie-offered", FilePath: "virtual://movie/movie-offered", VirtualOwnerInstallationID: 5}
	var seenPreferred string
	h := &PlaybackHandler{
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("detailed resolver expected")
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, preferred string) (ResolvedVirtualMedia, error) {
			seenPreferred = preferred
			return ResolvedVirtualMedia{URL: "https://cdn.example/c.mkv", URI: offeredURI, CandidateID: "C", OwnerID: 5}, nil
		}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
	start := playback.StartRequestV3{QualityPreference: "auto", OfferedVirtualURI: offeredURI}
	resolved, err := h.resolveVirtualStartWithVersionFallback(req, primary, "profile-1", start, false, 0)
	if err != nil {
		t.Fatalf("offered candidate resolve: %v", err)
	}
	if seenPreferred != "C" {
		t.Fatalf("resolver preferred candidate = %q, want C", seenPreferred)
	}
	if !sameVirtualCandidate(resolved.URI, offeredURI) {
		t.Fatalf("resolved URI = %q, want the offered candidate %q", resolved.URI, offeredURI)
	}

	// A resolver that substitutes another candidate must fail the start, not
	// silently apply the offered track identities to D.
	h.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		return ResolvedVirtualMedia{URL: "https://cdn.example/d.mkv", URI: "virtual://movie/movie-offered?result=D", CandidateID: "D", OwnerID: 5}, nil
	})
	if _, err := h.resolveVirtualStartWithVersionFallback(req, primary, "profile-1", start, false, 0); !errors.Is(err, errOfferedCandidateUnavailable) {
		t.Fatalf("substituted offered candidate error = %v, want errOfferedCandidateUnavailable", err)
	}
}

// TestResolveVirtualStartWithVersionFallbackSkipsAltMountFailedVersion pins
// behavior 2: when the pinned release's listing fails on a fresh start, the
// walk tries alternate versions and returns the first that resolves. A version
// carrying an active AltMount SourceFailed verdict is skipped without a
// listing attempt, and the primary empty-listing failure is the honest cause
// returned only when no other version resolves.
func TestResolveVirtualStartWithVersionFallbackSkipsAltMountFailedVersion(t *testing.T) {
	const (
		primaryURI = "virtual://movie/movie-versions?result=A"
		failedURI  = "virtual://movie/movie-versions?result=B"
		workingURI = "virtual://movie/movie-versions?result=C"
	)
	primary := &models.MediaFile{ID: 1, ContentID: "movie-versions", FilePath: primaryURI, VirtualOwnerInstallationID: 5}
	failedAt := time.Now()
	failedAlt := &models.MediaFile{ID: 2, ContentID: "movie-versions", FilePath: failedURI, VirtualOwnerInstallationID: 5, FailedAt: &failedAt, ProviderVideoHash: "hash-b"}
	workingAlt := &models.MediaFile{ID: 3, ContentID: "movie-versions", FilePath: workingURI, VirtualOwnerInstallationID: 5, ProviderVideoHash: "hash-c"}

	var callsA, callsB, callsC int
	h := &PlaybackHandler{
		FileVersionFetcher: testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
			"movie-versions": {primary, failedAlt, workingAlt},
		}},
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used when the detailed resolver is set")
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			switch {
			case strings.Contains(uri, "result=A"):
				callsA++
				return ResolvedVirtualMedia{}, errors.New("resolve virtual playback: no streams available from provider")
			case strings.Contains(uri, "result=B"):
				callsB++
				return ResolvedVirtualMedia{}, errors.New("resolve virtual playback: no streams available from provider")
			case strings.Contains(uri, "result=C"):
				callsC++
				return ResolvedVirtualMedia{URL: "https://cdn.example/c.mkv", URI: uri, CandidateID: "cand-c", OwnerID: 5}, nil
			default:
				return ResolvedVirtualMedia{}, fmt.Errorf("unexpected URI %q", uri)
			}
		}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
	start := playback.StartRequestV3{QualityPreference: "auto"}

	resolved, err := h.resolveVirtualStartWithVersionFallback(req, primary, "profile-1", start, false, 0)
	if err != nil {
		t.Fatalf("cross-version fallback resolve: %v", err)
	}
	if resolved.File == nil || !strings.Contains(resolved.URI, "result=C") {
		t.Fatalf("resolved = %#v, want the working alternate version C", resolved)
	}
	if callsA == 0 {
		t.Fatal("the primary pinned release was never attempted")
	}
	if callsC == 0 {
		t.Fatal("the working alternate version was never attempted")
	}
	if callsB != 0 {
		t.Fatalf("the AltMount-failed version was attempted %d times; it must be skipped", callsB)
	}
}

// TestResolveVirtualStartWithVersionFallbackReturnsHonestEmptyListing pins that
// when every alternate's listing is empty, the original empty-listing cause is
// returned (not a fabricated all-versions verdict), so the caller can report
// the honest transient condition.
func TestResolveVirtualStartWithVersionFallbackReturnsHonestEmptyListing(t *testing.T) {
	primary := &models.MediaFile{ID: 1, ContentID: "movie-empty", FilePath: "virtual://movie/movie-empty?result=A", VirtualOwnerInstallationID: 5}
	alt := &models.MediaFile{ID: 2, ContentID: "movie-empty", FilePath: "virtual://movie/movie-empty?result=B", VirtualOwnerInstallationID: 5}
	h := &PlaybackHandler{
		FileVersionFetcher: testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
			"movie-empty": {primary, alt},
		}},
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used when the detailed resolver is set")
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{}, errors.New("resolve virtual playback: no streams available from provider")
		}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)

	_, err := h.resolveVirtualStartWithVersionFallback(req, primary, "profile-1", playback.StartRequestV3{QualityPreference: "auto"}, false, 0)
	if err == nil {
		t.Fatal("expected the empty-listing resolve to fail")
	}
	terminal := virtualStartUnresolvedTerminalV3(err).Terminal
	if terminal == nil || terminal.Reason != virtualSourceUnavailableReasonV3 || !strings.Contains(terminal.Message, "no streams") {
		t.Fatalf("terminal = %#v, want the honest empty-listing cause", terminal)
	}
	if !terminal.Retryable {
		t.Fatal("an empty provider listing is transient and must stay retryable")
	}
}

// TestVirtualStartUnresolvedTerminalHonestCause pins the exhaustion
// classification: an edge provider failure is the retryable
// provider_unavailable dependency condition, an empty listing names the empty
// answer, and every other cause keeps the generic resolve message instead of a
// fabricated verdict.
func TestVirtualStartUnresolvedTerminalHonestCause(t *testing.T) {
	edge := virtualStartUnresolvedTerminalV3(fmt.Errorf("resolve virtual input: %w: provider listing failed recently", resolver.ErrProviderUnavailable))
	if edge.Terminal == nil || edge.Terminal.Reason != providerUnavailableReasonV3 || !edge.Terminal.Retryable {
		t.Fatalf("edge failure terminal = %#v, want retryable %s", edge.Terminal, providerUnavailableReasonV3)
	}
	empty := virtualStartUnresolvedTerminalV3(errors.New("no streams available from provider"))
	if empty.Terminal == nil || empty.Terminal.Reason != virtualSourceUnavailableReasonV3 || !strings.Contains(empty.Terminal.Message, "no streams") {
		t.Fatalf("empty listing terminal = %#v, want the empty-listing message", empty.Terminal)
	}
	dead := virtualStartUnresolvedTerminalV3(errors.New("virtual stream provider returned no matching candidate"))
	if dead.Terminal == nil || dead.Terminal.Reason != virtualSourceUnavailableReasonV3 || strings.Contains(dead.Terminal.Message, "no streams") {
		t.Fatalf("other terminal = %#v, want the generic resolve message", dead.Terminal)
	}
}

// TestResolveVirtualPlaybackSourceBypassDamperIsBounded pins finding 1: the
// declared recovery bypass (the alternate-version walk's per-version resolve)
// draws on the same per-provider recovery budget as the stale fallback, so a
// provider that keeps failing its listing cannot have the floor bypassed on
// every version of every press. Once the budget is spent, a bypass resolve
// honors the floor instead of forcing another re-list.
func TestResolveVirtualPlaybackSourceBypassDamperIsBounded(t *testing.T) {
	resetVirtualRecoveryRelists(t)
	file := &models.MediaFile{ID: 1, ContentID: "movie-bypass-damper", FilePath: "virtual://movie/movie-bypass-damper?result=A", VirtualOwnerInstallationID: 5}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)

	seen := make([]bool, 0, virtualRecoveryRelistMax+1)
	h := &PlaybackHandler{
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used when the detailed resolver is set")
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(ctx context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			seen = append(seen, virtuallibrary.ProviderOutageRelistFromContext(ctx))
			return ResolvedVirtualMedia{}, errors.New("resolve virtual playback: no streams available from provider")
		}),
	}
	for i := 0; i < virtualRecoveryRelistMax; i++ {
		_, _ = h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false, bypassProviderFloor: true})
	}
	if len(seen) != virtualRecoveryRelistMax {
		t.Fatalf("resolver calls = %d, want %d admitted bypasses", len(seen), virtualRecoveryRelistMax)
	}
	for i, relist := range seen {
		if !relist {
			t.Fatalf("admitted bypass %d did not re-list past the floor", i)
		}
	}

	// The budget is now spent: the next bypass must honor the floor.
	_, _ = h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false, bypassProviderFloor: true})
	if len(seen) != virtualRecoveryRelistMax+1 {
		t.Fatalf("resolver calls = %d, want the exhausted bypass to still resolve on the floor", len(seen))
	}
	if seen[len(seen)-1] {
		t.Fatal("the exhausted bypass still forced a re-list past the floor")
	}

	// A non-bypass resolve shares the same budget key and stays on the floor.
	_, _ = h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false})
	if seen[len(seen)-1] {
		t.Fatal("an ordinary resolve must stay on the floor")
	}
}

// TestResolveVirtualPlaybackSourceBypassBudgetClearsOnAnswer pins the
// answer-clears-budget half of findings 1 and 2 on the bypass path: a declared
// recovery whose provider listing answers with candidates clears its budget, so
// a later failure starts from a full window instead of inheriting the earlier
// exhaustion.
func TestResolveVirtualPlaybackSourceBypassBudgetClearsOnAnswer(t *testing.T) {
	resetVirtualRecoveryRelists(t)
	const (
		neutral = "virtual://movie/movie-bypass-clear"
		goodURI = neutral + "?result=good"
	)
	file := &models.MediaFile{ID: 1, ContentID: "movie-bypass-clear", FilePath: neutral + "?result=A", VirtualOwnerInstallationID: 5}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)

	// Spend two of the three slots first, so only a real clear admits the full
	// fresh window asserted below.
	failing := errors.New("resolve virtual playback: no streams available from provider")
	for i := 0; i < virtualRecoveryRelistMax-1; i++ {
		h := &PlaybackHandler{
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "", errors.New("simple resolver must not be used when the detailed resolver is set")
			}),
			VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				return ResolvedVirtualMedia{}, failing
			}),
		}
		_, _ = h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false, bypassProviderFloor: true})
	}
	// The provider answers with a healthy candidate. The candidate listing
	// clears the budget and the healthy stream resolves, so the resolve returns
	// nil.
	answering := &PlaybackHandler{
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used when the detailed resolver is set")
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{ID: "good", URI: goodURI, Resolution: "1080p"}}, nil
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{URL: "https://cdn.example/good.mp4", URI: uri, CandidateID: "good", OwnerID: 5}, nil
		}),
	}
	if _, err := answering.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false, bypassProviderFloor: true}); err != nil {
		t.Fatalf("answering bypass resolve: %v", err)
	}

	// A fresh window of bypasses is now admitted. The loop's first
	// virtualRecoveryRelistMax attempts each force a re-list past the floor;
	// the overflow attempt must not. An inherited exhaustion would stop the
	// first attempt; a budget that only ever decremented would keep re-listing
	// the overflow.
	relisted := make([]bool, 0, virtualRecoveryRelistMax+1)
	for i := 0; i < virtualRecoveryRelistMax+1; i++ {
		h := &PlaybackHandler{
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "", errors.New("simple resolver must not be used when the detailed resolver is set")
			}),
		}
		probeSeen := false
		h.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			probeSeen = virtuallibrary.ProviderOutageRelistFromContext(ctx)
			return ResolvedVirtualMedia{}, failing
		})
		_, _ = h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false, virtualResolveOptionsV3{sessionBound: false, bypassProviderFloor: true})
		relisted = append(relisted, probeSeen)
	}
	for i := 0; i < virtualRecoveryRelistMax; i++ {
		if !relisted[i] {
			t.Fatalf("post-clear bypass %d honored the floor; the answering resolve did not clear the budget", i)
		}
	}
	if relisted[virtualRecoveryRelistMax] {
		t.Fatal("the overflow bypass still re-listed; the fresh window was not bounded")
	}
}

// A start whose own budget is nearly spent must not walk the alternate versions.
// The selected release was never disproven — the resolve simply ran out of the
// request's time — and every alternate would observe the same expired deadline.
// Landing on whichever sibling answers from cache is exactly the measured
// silent swap (requested 613586 played as 1436768), so the walk keeps the
// selection and surfaces the honest resolve error.
func TestResolveVirtualStartWithVersionFallbackDoesNotSwapWhenStartBudgetIsSpent(t *testing.T) {
	const (
		primaryURI = "virtual://movie/movie-budget?result=A"
		workingURI = "virtual://movie/movie-budget?result=C"
	)
	primary := &models.MediaFile{ID: 1, ContentID: "movie-budget", FilePath: primaryURI, VirtualOwnerInstallationID: 5}
	working := &models.MediaFile{ID: 3, ContentID: "movie-budget", FilePath: workingURI, VirtualOwnerInstallationID: 5}

	var primaryCalls, alternateCalls int
	h := &PlaybackHandler{
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used when the detailed resolver is set")
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			if strings.Contains(uri, "result=C") {
				alternateCalls++
				return ResolvedVirtualMedia{URL: "https://cdn.example/c.mkv", URI: uri, CandidateID: "cand-c", OwnerID: 5}, nil
			}
			primaryCalls++
			return ResolvedVirtualMedia{}, fmt.Errorf("resolve virtual playback: %w", resolver.ErrProviderUnavailable)
		}),
	}
	h.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		"movie-budget": {primary, working},
	}}

	// The start's own budget is already spent when the walk would begin.
	spentCtx, budgetCancel := context.WithDeadline(context.Background(), time.Now().Add(-40*time.Millisecond))
	defer budgetCancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil).WithContext(spentCtx)

	resolved, err := h.resolveVirtualStartWithVersionFallback(req, primary, "profile-1", playback.StartRequestV3{QualityPreference: "auto"}, false, 0)
	if err == nil {
		t.Fatalf("a start with no resolve budget returned %#v; want the honest resolve error", resolved.File)
	}
	if alternateCalls != 0 {
		t.Fatalf("the alternate walk resolved %d substitute(s); it must not run once the start budget is spent", alternateCalls)
	}
	if resolved.File != nil && resolved.File.ID != primary.ID {
		t.Fatalf("substituted file %d for requested %d on an exhausted budget", resolved.File.ID, primary.ID)
	}
	if resolved.SubstitutedFromFileID != 0 {
		t.Fatalf("an exhausted budget reported a substitution from %d", resolved.SubstitutedFromFileID)
	}
}

// The cross-version fallback applies the same guard #448 added for rotations: a
// substitute that would offer fewer audio or subtitle tracks than the selected
// row is refused, and when no substitute can serve the selection the start
// terminally names the version list instead of playing different bytes with a
// poorer menu. This is the measured track-losing swap (3 audio / 4 subtitles
// played as 1 audio / 0 subtitles).
func TestResolveVirtualStartWithVersionFallbackRefusesTrackLosingAlternate(t *testing.T) {
	const (
		primaryURI = "virtual://movie/movie-tracks?result=A"
		poorURI    = "virtual://movie/movie-tracks?result=B"
		goodURI    = "virtual://movie/movie-tracks?result=C"
	)
	primaryAudio, primarySubs := rotationTrackFixtureTracks(3, 4)
	poorAudio, poorSubs := rotationTrackFixtureTracks(1, 0)
	goodAudio, goodSubs := rotationTrackFixtureTracks(3, 4)
	primary := &models.MediaFile{
		ID: 1, ContentID: "movie-tracks", FilePath: primaryURI, VirtualOwnerInstallationID: 5,
		AudioTracks: primaryAudio, SubtitleTracks: primarySubs,
	}
	poor := &models.MediaFile{
		ID: 2, ContentID: "movie-tracks", FilePath: poorURI, VirtualOwnerInstallationID: 5,
		AudioTracks: poorAudio, SubtitleTracks: poorSubs, ProviderVideoHash: "hash-b",
	}
	good := &models.MediaFile{
		ID: 3, ContentID: "movie-tracks", FilePath: goodURI, VirtualOwnerInstallationID: 5,
		AudioTracks: goodAudio, SubtitleTracks: goodSubs, ProviderVideoHash: "hash-c",
	}

	newHandler := func() *PlaybackHandler {
		rows := map[string]*models.MediaFile{primaryURI: primary, poorURI: poor, goodURI: good}
		return &PlaybackHandler{
			FileVersionFetcher: testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
				"movie-tracks": {primary, poor, good},
			}},
			// The catalog owns each candidate URI, so a resolve serves the
			// row's own inventory instead of a metadata-less transient.
			VirtualFileLookup: func(_ context.Context, path string) (*models.MediaFile, error) {
				return rows[path], nil
			},
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "", errors.New("simple resolver must not be used when the detailed resolver is set")
			}),
			VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				switch {
				case strings.Contains(uri, "result=B"):
					return ResolvedVirtualMedia{URL: "https://cdn.example/b.mkv", URI: uri, CandidateID: virtualResultCandidateID(uri), OwnerID: 5}, nil
				case strings.Contains(uri, "result=C"):
					return ResolvedVirtualMedia{URL: "https://cdn.example/c.mkv", URI: uri, CandidateID: virtualResultCandidateID(uri), OwnerID: 5}, nil
				default:
					return ResolvedVirtualMedia{}, errors.New("resolve virtual playback: no streams available from provider")
				}
			}),
		}
	}

	t.Run("a resolve timeout does not swap to a track-losing substitute", func(t *testing.T) {
		// The production shape: the provider fetch times out, which reaches the
		// fallback as a listing outage (the fetch wraps the client error), so the
		// walk runs. The only sibling it can serve carries 1 audio / 0 subtitles.
		timedOut := &PlaybackHandler{
			FileVersionFetcher: testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
				"movie-tracks": {primary, poor},
			}},
			VirtualFileLookup: func(_ context.Context, path string) (*models.MediaFile, error) {
				if path == poorURI {
					return poor, nil
				}
				return primary, nil
			},
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "", errors.New("simple resolver must not be used when the detailed resolver is set")
			}),
			VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				if strings.Contains(uri, "result=B") {
					return ResolvedVirtualMedia{URL: "https://cdn.example/b.mkv", URI: uri, CandidateID: virtualResultCandidateID(uri), OwnerID: 5}, nil
				}
				return ResolvedVirtualMedia{}, fmt.Errorf("request streaming provider failed: %w: %w", resolver.ErrProviderUnavailable, context.DeadlineExceeded)
			}),
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
		resolved, err := timedOut.resolveVirtualStartWithVersionFallback(req, primary, "profile-1", playback.StartRequestV3{QualityPreference: "auto"}, false, 0)
		if err == nil {
			t.Fatalf("the walk served the 1-audio/0-subtitle substitute %d; want a refusal", resolved.File.ID)
		}
		terminal := virtualStartUnresolvedTerminalV3(err).Terminal
		if terminal == nil || !strings.Contains(terminal.Message, "version list") {
			t.Fatalf("terminal = %#v, want the version-list hint when no substitute can serve the selection", terminal)
		}
	})

	t.Run("a track-preserving substitute still plays and is reported", func(t *testing.T) {
		h := newHandler()
		req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
		resolved, err := h.resolveVirtualStartWithVersionFallback(req, primary, "profile-1", playback.StartRequestV3{QualityPreference: "auto"}, false, 0)
		if err != nil {
			t.Fatalf("track-preserving fallback resolve: %v", err)
		}
		if resolved.File == nil || resolved.File.ID != good.ID {
			t.Fatalf("resolved file = %#v, want the track-preserving alternate %d", resolved.File, good.ID)
		}
		if resolved.SubstitutedFromFileID != primary.ID {
			t.Fatalf("substituted_from_file_id = %d, want the requested row %d", resolved.SubstitutedFromFileID, primary.ID)
		}
		if resolved.SubstitutionReason != substitutionReasonListingFailedV3 {
			t.Fatalf("substitution_reason = %q, want %q", resolved.SubstitutionReason, substitutionReasonListingFailedV3)
		}
	})
}
