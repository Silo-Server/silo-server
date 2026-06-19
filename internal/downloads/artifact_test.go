package downloads

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestParamsHashStableAndDistinct(t *testing.T) {
	base := paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, false)
	if base == "" || len(base) != 64 {
		t.Fatalf("params hash should be a 64-char sha256 hex, got %q", base)
	}
	// Deterministic for identical inputs (dedup key).
	if again := paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, false); again != base {
		t.Fatalf("params hash not stable: %q != %q", base, again)
	}
	// Distinct when any parameter differs.
	for _, other := range []string{
		paramsHash("remux", "mp4", "h264", "aac", "1080p", -1, false),
		paramsHash("transcode", "mp4", "hevc", "aac", "1080p", -1, false),
		paramsHash("transcode", "mp4", "h264", "aac", "720p", -1, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", 1, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, true),
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

type fakeUserRepo struct{ user *models.User }

func (f fakeUserRepo) GetByID(context.Context, int) (*models.User, error) { return f.user, nil }

func TestCapabilityFormatsGating(t *testing.T) {
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
	if got := strings.Join(capInfo.Formats, ","); got != "original" {
		t.Fatalf("formats without pipeline = %q, want original", got)
	}

	// Pipeline wired + transcode server/user gates open → all three formats.
	svc = newSvc(allowAll, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.Formats, ","); got != "original,remux,transcode" {
		t.Fatalf("formats with pipeline = %q, want original,remux,transcode", got)
	}

	// Transcode gated off (user flag) → original + remux only.
	svc = newSvc(&models.User{DownloadAllowed: true, DownloadTranscodeAllowed: false}, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.Formats, ","); got != "original,remux" {
		t.Fatalf("formats with transcode gated = %q, want original,remux", got)
	}
}
