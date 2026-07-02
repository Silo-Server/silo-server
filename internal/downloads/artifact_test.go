package downloads

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestParamsHashStableAndDistinct(t *testing.T) {
	base := paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false)
	if base == "" || len(base) != 64 {
		t.Fatalf("params hash should be a 64-char sha256 hex, got %q", base)
	}
	// Deterministic for identical inputs (dedup key).
	if again := paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false); again != base {
		t.Fatalf("params hash not stable: %q != %q", base, again)
	}
	// Distinct when any parameter differs.
	for _, other := range []string{
		paramsHash("remux", "mp4", "h264", "aac", "1080p", -1, 10000, false),
		paramsHash("transcode", "mp4", "hevc", "aac", "1080p", -1, 10000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "720p", -1, 10000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", 1, 10000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 5000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, true),
	} {
		if other == base {
			t.Fatalf("params hash collision: %q", other)
		}
	}
}

func TestArtifactOutputPathDeterministic(t *testing.T) {
	p1 := artifactOutputPath("/var/artifacts", 42, "transcode", "abcdef0123456789deadbeef")
	p2 := artifactOutputPath("/var/artifacts", 42, "transcode", "abcdef0123456789deadbeef")
	if p1 != p2 {
		t.Fatalf("output path not deterministic: %q != %q", p1, p2)
	}
	if !strings.HasPrefix(p1, "/var/artifacts/") || !strings.HasSuffix(p1, ".mp4") {
		t.Fatalf("unexpected output path %q", p1)
	}
	if !strings.Contains(p1, "42_transcode_") {
		t.Fatalf("output path missing identity components: %q", p1)
	}
}

func TestEffectiveArtifactDir(t *testing.T) {
	// Explicit config wins verbatim.
	if got := effectiveArtifactDir("/data/artifacts", "/tmp/silo-transcode"); got != "/data/artifacts" {
		t.Fatalf("explicit dir = %q, want /data/artifacts", got)
	}
	// Unset: a sibling of the transcode dir, never inside it (the transcode
	// cleanup sweep would otherwise delete a nested artifact dir) and never the
	// process cwd (a relative/empty path).
	got := effectiveArtifactDir("", "/var/lib/silo/transcode")
	if got != "/var/lib/silo/silo-download-artifacts" {
		t.Fatalf("default dir = %q, want sibling of transcode dir", got)
	}
	if strings.HasPrefix(got, "/var/lib/silo/transcode/") {
		t.Fatalf("default dir %q is nested under the transcode dir", got)
	}
	// Unset transcode dir falls back to the absolute default root, not "".
	fallback := effectiveArtifactDir("", "")
	if !strings.HasPrefix(fallback, "/") {
		t.Fatalf("fallback dir %q is not absolute", fallback)
	}
}

type fakeUserRepo struct{ user *models.User }

func (f fakeUserRepo) GetByID(context.Context, int) (*models.User, error) { return f.user, nil }

func TestCapabilityQualityPresetsGating(t *testing.T) {
	newSvc := func(user *models.User, transcodeEnabled bool) *Service {
		cfg := config.DownloadConfig{Enabled: true, TranscodeEnabled: transcodeEnabled}
		return NewService(nil, nil, nil, nil, nil, nil, fakeUserRepo{user}, nil, nil, &cfg)
	}
	allowAll := &models.User{DownloadAllowed: true, DownloadTranscodeAllowed: true}

	// No artifact pipeline wired → only original is fulfillable.
	svc := newSvc(allowAll, true)
	capInfo, err := svc.Capability(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original" {
		t.Fatalf("quality presets without pipeline = %q, want original", got)
	}

	// Pipeline wired + transcode server/user gates open → full bitrate ladder.
	svc = newSvc(allowAll, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original,20mbps,10mbps,5mbps,2mbps,1mbps" {
		t.Fatalf("quality presets with pipeline = %q, want full ladder", got)
	}

	// Transcode gated off (user flag) → original only.
	svc = newSvc(&models.User{DownloadAllowed: true, DownloadTranscodeAllowed: false}, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original" {
		t.Fatalf("quality presets with transcode gated = %q, want original", got)
	}

	// Download permission revoked → an EMPTY array, never nil: the capability
	// contract documents quality_presets as an array, and a nil slice would
	// serialize as JSON null and break typed clients.
	svc = newSvc(&models.User{DownloadAllowed: false}, true)
	capInfo, _ = svc.Capability(context.Background(), 1)
	if capInfo.QualityPresets == nil {
		t.Fatal("quality presets for a denied user must be an empty array, not nil")
	}
	if len(capInfo.QualityPresets) != 0 {
		t.Fatalf("quality presets for a denied user = %v, want empty", capInfo.QualityPresets)
	}
	if b, err := json.Marshal(capInfo.QualityPresets); err != nil || string(b) != "[]" {
		t.Fatalf("quality presets serialize to %s (%v), want []", b, err)
	}
}

// TestTriggerDrainDoesNotBlockCaller pins the async-dispatch contract:
// Ensure runs on request goroutines and the kick drains the whole encode
// queue (ffmpeg included), so triggerDrain must return without waiting on it.
func TestTriggerDrainDoesNotBlockCaller(t *testing.T) {
	m := &ArtifactManager{}
	started := make(chan struct{})
	release := make(chan struct{})
	m.SetKick(func() {
		close(started)
		<-release
	})

	done := make(chan struct{})
	go func() {
		m.triggerDrain()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerDrain blocked on the kick; it must dispatch asynchronously")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("kick was never invoked")
	}
	close(release)
}
