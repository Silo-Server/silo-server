package metadata

import (
	"context"
	"errors"
	"testing"
)

// purgeProbeRecorder scripts verified-fetch verdicts by URL and records the
// order and count of probes so dedupe and resolver routing are observable.
type purgeProbeRecorder struct {
	failing  map[string]error
	resolved []string
}

func (r *purgeProbeRecorder) fetch(_ context.Context, rawURL string) error {
	r.resolved = append(r.resolved, rawURL)
	return r.failing[rawURL]
}

type staticSourceResolver struct {
	urls  map[string]string
	calls int
}

func (r *staticSourceResolver) ResolveImageURL(_ context.Context, path string, _ string) string {
	r.calls++
	return r.urls[path]
}

func remotePurgeTarget(keys string, source string) artworkPurgeTarget {
	return artworkPurgeTarget{
		surfaceName: artworkSurfaceItemPosters,
		keys:        []string{keys},
		path:        "artwork/v1/" + keys + "/poster/original.webp",
		source:      source,
		bytes:       1024,
	}
}

func TestRevalidateTargetsProtectsUnreachableRemoteSource(t *testing.T) {
	probes := &purgeProbeRecorder{failing: map[string]error{
		"https://images.example/dead.jpg": errors.New("unexpected status 404"),
	}}
	executor := &ArtworkPurgeExecutor{fetchSource: probes.fetch}
	targets := []artworkPurgeTarget{
		remotePurgeTarget("live", "https://images.example/live.jpg"),
		remotePurgeTarget("dead", "https://images.example/dead.jpg"),
	}

	if err := executor.revalidateTargets(context.Background(), targets); err != nil {
		t.Fatalf("revalidateTargets: %v", err)
	}
	if targets[0].protected || targets[0].fallback != "https://images.example/live.jpg" {
		t.Fatalf("reachable target = protected %v fallback %q", targets[0].protected, targets[0].fallback)
	}
	// A dead provider URL must not become the catalog's fallback: the
	// transition would leave the stored revision unreferenced and collectible
	// while nothing can ever re-materialize it.
	if !targets[1].protected || targets[1].fallback != "" {
		t.Fatalf("unreachable target = protected %v fallback %q, want protected with no fallback", targets[1].protected, targets[1].fallback)
	}
	if len(probes.resolved) != 2 {
		t.Fatalf("probed %d sources, want 2: %v", len(probes.resolved), probes.resolved)
	}
}

func TestRevalidateTargetsProbesEachSourceOnce(t *testing.T) {
	probes := &purgeProbeRecorder{}
	executor := &ArtworkPurgeExecutor{fetchSource: probes.fetch}
	targets := []artworkPurgeTarget{
		remotePurgeTarget("a", "https://images.example/shared.jpg"),
		remotePurgeTarget("b", "https://images.example/shared.jpg"),
		remotePurgeTarget("c", "https://images.example/other.jpg"),
	}

	if err := executor.revalidateTargets(context.Background(), targets); err != nil {
		t.Fatalf("revalidateTargets: %v", err)
	}
	if len(probes.resolved) != 2 {
		t.Fatalf("probed %v, want one fetch per distinct source", probes.resolved)
	}
	for i := range targets {
		if targets[i].protected {
			t.Fatalf("target %d protected after a successful probe", i)
		}
	}
}

func TestRevalidateTargetsSkipsSharedAndBundledSources(t *testing.T) {
	probes := &purgeProbeRecorder{}
	executor := &ArtworkPurgeExecutor{fetchSource: probes.fetch}
	shared := remotePurgeTarget("shared", "https://images.example/a.jpg")
	shared.shared = true
	targets := []artworkPurgeTarget{
		shared,
		remotePurgeTarget("bundled", "/assets/collection-template.webp"),
		remotePurgeTarget("upload", "upload://protected"),
	}

	if err := executor.revalidateTargets(context.Background(), targets); err != nil {
		t.Fatalf("revalidateTargets: %v", err)
	}
	if len(probes.resolved) != 0 {
		t.Fatalf("probed %v, want no network probe", probes.resolved)
	}
	if targets[0].fallback != "" || targets[0].protected {
		t.Fatalf("shared target was revalidated: fallback %q protected %v", targets[0].fallback, targets[0].protected)
	}
	if targets[1].fallback != "/assets/collection-template.webp" || targets[1].protected {
		t.Fatalf("bundled asset = fallback %q protected %v", targets[1].fallback, targets[1].protected)
	}
	if !targets[2].protected {
		t.Fatal("non-reconstructible upload source must stay protected")
	}
}

func TestRevalidateTargetsProtectsProviderSourceWithoutResolver(t *testing.T) {
	probes := &purgeProbeRecorder{}
	executor := &ArtworkPurgeExecutor{fetchSource: probes.fetch}
	targets := []artworkPurgeTarget{remotePurgeTarget("plugin", "tmdb://t/p/original/abc.jpg")}

	if err := executor.revalidateTargets(context.Background(), targets); err != nil {
		t.Fatalf("revalidateTargets: %v", err)
	}
	if !targets[0].protected || targets[0].fallback != "" {
		t.Fatalf("unresolvable provider source = protected %v fallback %q", targets[0].protected, targets[0].fallback)
	}
	if len(probes.resolved) != 0 {
		t.Fatalf("probed %v without a resolver", probes.resolved)
	}
}

func TestRevalidateTargetsResolvesProviderSourceBeforeFetching(t *testing.T) {
	probes := &purgeProbeRecorder{}
	resolver := &staticSourceResolver{urls: map[string]string{
		"tmdb://t/p/original/abc.jpg": "https://image.tmdb.example/original/abc.jpg",
	}}
	executor := &ArtworkPurgeExecutor{fetchSource: probes.fetch, sources: resolver}
	targets := []artworkPurgeTarget{
		remotePurgeTarget("known", "tmdb://t/p/original/abc.jpg"),
		remotePurgeTarget("gone", "tmdb://t/p/original/removed.jpg"),
	}

	if err := executor.revalidateTargets(context.Background(), targets); err != nil {
		t.Fatalf("revalidateTargets: %v", err)
	}
	if targets[0].protected || targets[0].fallback != "tmdb://t/p/original/abc.jpg" {
		t.Fatalf("resolvable provider target = protected %v fallback %q", targets[0].protected, targets[0].fallback)
	}
	if len(probes.resolved) != 1 || probes.resolved[0] != "https://image.tmdb.example/original/abc.jpg" {
		t.Fatalf("fetched %v, want the resolved provider URL", probes.resolved)
	}
	// A provider that no longer resolves the path is as unusable as a 404.
	if !targets[1].protected {
		t.Fatal("source resolving to an empty URL must stay protected")
	}
}

func TestPurgePlanFingerprintIgnoresRevalidationVerdicts(t *testing.T) {
	req := ArtworkPurgeRequest{Scope: ArtworkPurgeScope{Server: true}, Mode: ArtworkPurgeModeSafeMaterialized}
	planned := []artworkPurgeTarget{remotePurgeTarget("a", "https://images.example/a.jpg")}
	base := purgePlanFingerprint(req, planned)

	revalidated := []artworkPurgeTarget{remotePurgeTarget("a", "https://images.example/a.jpg")}
	revalidated[0].fallback = "https://images.example/a.jpg"
	if got := purgePlanFingerprint(req, revalidated); got != base {
		t.Fatal("fingerprint changed after a fallback verdict; a resume could not revalidate")
	}
	revalidated[0].fallback, revalidated[0].protected = "", true
	if got := purgePlanFingerprint(req, revalidated); got != base {
		t.Fatal("fingerprint changed after a protected verdict; a resume could not revalidate")
	}

	// Catalog drift must still be detected.
	drifted := []artworkPurgeTarget{remotePurgeTarget("a", "https://images.example/b.jpg")}
	if got := purgePlanFingerprint(req, drifted); got == base {
		t.Fatal("fingerprint ignored a changed source path")
	}
	drifted = []artworkPurgeTarget{remotePurgeTarget("a", "https://images.example/a.jpg")}
	drifted[0].path = "artwork/v1/other/poster/original.webp"
	if got := purgePlanFingerprint(req, drifted); got == base {
		t.Fatal("fingerprint ignored a changed selected path")
	}
}

func TestResumeTargetsRevalidatesOnlyTheUnappliedRemainder(t *testing.T) {
	probes := &purgeProbeRecorder{failing: map[string]error{
		"https://images.example/gone.jpg": errors.New("unexpected status 404"),
	}}
	executor := &ArtworkPurgeExecutor{fetchSource: probes.fetch}
	req := ArtworkPurgeRequest{Scope: ArtworkPurgeScope{Server: true}, Mode: ArtworkPurgeModeSafeMaterialized}
	planned := []artworkPurgeTarget{
		remotePurgeTarget("applied", "https://images.example/applied.jpg"),
		remotePurgeTarget("pending", "https://images.example/gone.jpg"),
	}
	// The plan was validated and the first batch applied before the crash.
	planned[0].fallback = "https://images.example/applied.jpg"
	planned[1].fallback = "https://images.example/gone.jpg"
	cp := ArtworkPurgeCheckpoint{
		Version:         artworkPurgeCheckpointVersion,
		PlanFingerprint: purgePlanFingerprint(req, planned),
		BatchIndex:      1,
		Targets:         checkpointTargets(planned),
	}

	resumed, err := executor.resumeTargets(context.Background(), req, &cp)
	if err != nil {
		t.Fatalf("resumeTargets: %v", err)
	}
	if len(probes.resolved) != 1 || probes.resolved[0] != "https://images.example/gone.jpg" {
		t.Fatalf("probed %v, want only the unapplied remainder", probes.resolved)
	}
	if resumed[0].protected || resumed[0].fallback != "https://images.example/applied.jpg" {
		t.Fatalf("already-applied target changed: protected %v fallback %q", resumed[0].protected, resumed[0].fallback)
	}
	// The source died between the checkpoint and the resume, so the pending
	// transition must be skipped rather than replayed from the stale verdict.
	if !resumed[1].protected || resumed[1].fallback != "" {
		t.Fatalf("pending target = protected %v fallback %q, want protected", resumed[1].protected, resumed[1].fallback)
	}
	if cp.ProtectedSkipped != 1 {
		t.Fatalf("checkpoint protected_skipped = %d, want 1", cp.ProtectedSkipped)
	}
	if cp.ReclaimableBytes != 1024 {
		t.Fatalf("checkpoint reclaimable_bytes = %d, want only the still-valid target", cp.ReclaimableBytes)
	}
	if len(cp.Targets) != 2 || !cp.Targets[1].Protected {
		t.Fatal("refreshed verdict was not written back to the checkpoint")
	}
}

func TestResumeTargetsRejectsCatalogDrift(t *testing.T) {
	executor := &ArtworkPurgeExecutor{fetchSource: func(context.Context, string) error { return nil }}
	req := ArtworkPurgeRequest{Scope: ArtworkPurgeScope{Server: true}, Mode: ArtworkPurgeModeSafeMaterialized}
	planned := []artworkPurgeTarget{remotePurgeTarget("a", "https://images.example/a.jpg")}
	cp := ArtworkPurgeCheckpoint{
		Version:         artworkPurgeCheckpointVersion,
		PlanFingerprint: purgePlanFingerprint(req, planned),
		Targets:         checkpointTargets(planned),
	}
	cp.Targets[0].Path = "artwork/v1/moved/poster/original.webp"

	if _, err := executor.resumeTargets(context.Background(), req, &cp); err == nil {
		t.Fatal("resumeTargets accepted a checkpoint whose catalog rows moved")
	}
}
