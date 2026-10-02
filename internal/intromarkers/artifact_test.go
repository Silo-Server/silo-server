package intromarkers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
)

func TestIntroFingerprintKeyIsNotNamespaced(t *testing.T) {
	if mediaartifact.ConfigHash(ArtifactKindIntroFingerprint, "25:10:15:120") == DefaultConfig("ffmpeg").ConfigHash() {
		t.Fatal("the intro fingerprint key predates namespacing and must not be derived through it")
	}
}
