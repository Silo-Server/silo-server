package intromarkers

import (
	"testing"
	"time"
)

// Stored artifacts of new kinds carry keys derived this way; changing the
// derivation discards them.
func TestArtifactConfigHashIsNamespacedByKind(t *testing.T) {
	if got := ArtifactConfigHash("credits_fingerprint", "example"); got != "79c3967ac83084ea" {
		t.Fatalf("ArtifactConfigHash() = %s, want 79c3967ac83084ea", got)
	}
	if ArtifactConfigHash("credits_fingerprint", "25:10") == ArtifactConfigHash("credits_tail", "25:10") {
		t.Fatal("two kinds with the same parameters must not share a config hash")
	}
	if ArtifactConfigHash(ArtifactKindIntroFingerprint, "25:10:15:120") == DefaultConfig("ffmpeg").ConfigHash() {
		t.Fatal("the intro fingerprint key predates namespacing and must not be derived through it")
	}
}

func TestArtifactState(t *testing.T) {
	now := time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	earlier := now.Add(-time.Hour)
	identity := ArtifactIdentity{FileHash: "h", FileSize: 10, DurationSeconds: 1500, WindowEndSeconds: 375}
	changed := func(edit func(*ArtifactIdentity)) ArtifactIdentity {
		out := identity
		edit(&out)
		return out
	}
	row := func(status string, edit func(*Artifact)) *Artifact {
		a := &Artifact{ArtifactIdentity: identity, Status: status}
		if edit != nil {
			edit(a)
		}
		return a
	}
	for _, tt := range []struct {
		name     string
		artifact *Artifact
		identity ArtifactIdentity
		node     string
		want     ArtifactState
	}{
		{"no row", nil, identity, "a", ArtifactMissing},
		{"complete", row(ArtifactComplete, nil), identity, "a", ArtifactReady},
		{"complete after the file hash changed", row(ArtifactComplete, nil), changed(func(i *ArtifactIdentity) { i.FileHash = "other" }), "a", ArtifactMissing},
		{"complete after the size changed", row(ArtifactComplete, nil), changed(func(i *ArtifactIdentity) { i.FileSize = 11 }), "a", ArtifactMissing},
		{"complete after the duration changed", row(ArtifactComplete, nil), changed(func(i *ArtifactIdentity) { i.DurationSeconds = 1501 }), "a", ArtifactMissing},
		{"complete for another window start", row(ArtifactComplete, nil), changed(func(i *ArtifactIdentity) { i.WindowStartSeconds = 1 }), "a", ArtifactMissing},
		{"complete for another window end", row(ArtifactComplete, nil), changed(func(i *ArtifactIdentity) { i.WindowEndSeconds = 300 }), "a", ArtifactMissing},
		{"unusable", row(ArtifactUnusable, nil), identity, "a", ArtifactSkipped},
		{"unusable after the file changed", row(ArtifactUnusable, nil), changed(func(i *ArtifactIdentity) { i.FileHash = "other" }), "a", ArtifactMissing},
		{"failed here, backing off", row(ArtifactFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "a", &later }), identity, "a", ArtifactSkipped},
		{"failed here, backoff over", row(ArtifactFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "a", &earlier }), identity, "a", ArtifactMissing},
		{"failed elsewhere", row(ArtifactFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "b", &later }), identity, "a", ArtifactMissing},
		{"failed here without a retry time", row(ArtifactFailed, func(a *Artifact) { a.RecordedBy = "a" }), identity, "a", ArtifactMissing},
		{"failed here, then the file changed", row(ArtifactFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "a", &later }), changed(func(i *ArtifactIdentity) { i.FileSize = 11 }), "a", ArtifactMissing},
		{"unknown status", row("pending", nil), identity, "a", ArtifactMissing},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.artifact.State(tt.identity, tt.node, now); got != tt.want {
				t.Fatalf("State() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNextArtifactFailureBacksOffPerServer(t *testing.T) {
	at := time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC)
	identity := ArtifactIdentity{FileHash: "h", FileSize: 10, DurationSeconds: 1500}
	failure := ArtifactFailure{ArtifactIdentity: identity, RecordedBy: "a", At: at}
	failedHere := func(count int, retryAfter time.Time) *Artifact {
		return &Artifact{ArtifactIdentity: identity, Status: ArtifactFailed, RecordedBy: "a", FailureCount: count, RetryAfter: &retryAfter}
	}
	otherFile := failedHere(3, at.Add(-time.Hour))
	otherFile.FileHash = "other"
	otherServer := failedHere(3, at.Add(-time.Hour))
	otherServer.RecordedBy = "b"

	for _, tt := range []struct {
		name      string
		previous  *Artifact
		wantCount int
		wantRetry time.Time
	}{
		{"first failure", nil, 1, at.Add(12 * time.Hour)},
		{"after a complete row", &Artifact{ArtifactIdentity: identity, Status: ArtifactComplete}, 1, at.Add(12 * time.Hour)},
		{"retry after the backoff", failedHere(2, at.Add(-time.Hour)), 3, at.Add(48 * time.Hour)},
		{"forced run inside the backoff", failedHere(2, at.Add(time.Hour)), 2, at.Add(time.Hour)},
		{"another server's failure", otherServer, 1, at.Add(12 * time.Hour)},
		{"the file changed", otherFile, 1, at.Add(12 * time.Hour)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			count, retry := nextArtifactFailure(tt.previous, failure)
			if count != tt.wantCount || !retry.Equal(tt.wantRetry) {
				t.Fatalf("nextArtifactFailure() = %d, %v; want %d, %v", count, retry, tt.wantCount, tt.wantRetry)
			}
		})
	}
}
