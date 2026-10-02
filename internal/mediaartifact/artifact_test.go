package mediaartifact

import (
	"testing"
	"time"
)

// Stored artifacts of new kinds carry keys derived this way; changing the
// derivation discards them.
func TestConfigHashIsNamespacedByKind(t *testing.T) {
	if got := ConfigHash("credits_fingerprint", "example"); got != "79c3967ac83084ea" {
		t.Fatalf("ConfigHash() = %s, want 79c3967ac83084ea", got)
	}
	if ConfigHash("credits_fingerprint", "25:10") == ConfigHash("credits_tail", "25:10") {
		t.Fatal("two kinds with the same parameters must not share a config hash")
	}
}

func TestArtifactState(t *testing.T) {
	now := time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	earlier := now.Add(-time.Hour)
	identity := Identity{FileHash: "h", FileSize: 10, DurationSeconds: 1500, WindowEndSeconds: 375}
	changed := func(edit func(*Identity)) Identity {
		out := identity
		edit(&out)
		return out
	}
	row := func(status string, edit func(*Artifact)) *Artifact {
		a := &Artifact{Identity: identity, Status: status}
		if edit != nil {
			edit(a)
		}
		return a
	}
	for _, tt := range []struct {
		name     string
		artifact *Artifact
		identity Identity
		node     string
		want     State
	}{
		{"no row", nil, identity, "a", Missing},
		{"complete", row(StatusComplete, nil), identity, "a", Ready},
		{"complete after the file hash changed", row(StatusComplete, nil), changed(func(i *Identity) { i.FileHash = "other" }), "a", Missing},
		{"complete after the size changed", row(StatusComplete, nil), changed(func(i *Identity) { i.FileSize = 11 }), "a", Missing},
		{"complete after the duration changed", row(StatusComplete, nil), changed(func(i *Identity) { i.DurationSeconds = 1501 }), "a", Missing},
		{"complete for another window start", row(StatusComplete, nil), changed(func(i *Identity) { i.WindowStartSeconds = 1 }), "a", Missing},
		{"complete for another window end", row(StatusComplete, nil), changed(func(i *Identity) { i.WindowEndSeconds = 300 }), "a", Missing},
		{"unusable", row(StatusUnusable, nil), identity, "a", Skipped},
		{"unusable after the file changed", row(StatusUnusable, nil), changed(func(i *Identity) { i.FileHash = "other" }), "a", Missing},
		{"failed here, backing off", row(StatusFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "a", &later }), identity, "a", Skipped},
		{"failed here, backoff over", row(StatusFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "a", &earlier }), identity, "a", Missing},
		{"failed elsewhere", row(StatusFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "b", &later }), identity, "a", Missing},
		{"failed here without a retry time", row(StatusFailed, func(a *Artifact) { a.RecordedBy = "a" }), identity, "a", Missing},
		{"failed here, then the file changed", row(StatusFailed, func(a *Artifact) { a.RecordedBy, a.RetryAfter = "a", &later }), changed(func(i *Identity) { i.FileSize = 11 }), "a", Missing},
		{"unknown status", row("pending", nil), identity, "a", Missing},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.artifact.State(tt.identity, tt.node, now); got != tt.want {
				t.Fatalf("State() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNextFailureBacksOffPerServer(t *testing.T) {
	at := time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC)
	identity := Identity{FileHash: "h", FileSize: 10, DurationSeconds: 1500}
	failure := Failure{Identity: identity, RecordedBy: "a", At: at}
	failedHere := func(count int, retryAfter time.Time) *Artifact {
		return &Artifact{Identity: identity, Status: StatusFailed, RecordedBy: "a", FailureCount: count, RetryAfter: &retryAfter}
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
		{"after a complete row", &Artifact{Identity: identity, Status: StatusComplete}, 1, at.Add(12 * time.Hour)},
		{"retry after the backoff", failedHere(2, at.Add(-time.Hour)), 3, at.Add(48 * time.Hour)},
		{"forced run inside the backoff", failedHere(2, at.Add(time.Hour)), 2, at.Add(time.Hour)},
		{"another server's failure", otherServer, 1, at.Add(12 * time.Hour)},
		{"the file changed", otherFile, 1, at.Add(12 * time.Hour)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			count, retry := NextFailure(tt.previous, failure)
			if count != tt.wantCount || !retry.Equal(tt.wantRetry) {
				t.Fatalf("NextFailure() = %d, %v; want %d, %v", count, retry, tt.wantCount, tt.wantRetry)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		0:  12 * time.Hour,
		1:  12 * time.Hour,
		2:  24 * time.Hour,
		3:  48 * time.Hour,
		4:  96 * time.Hour,
		5:  7 * 24 * time.Hour,
		60: 7 * 24 * time.Hour,
	} {
		if got := RetryDelay(failures); got != want {
			t.Errorf("RetryDelay(%d) = %v, want %v", failures, got, want)
		}
	}
}
