package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

const testManifestJSON = `{
  "plugin_id": "silo.test.plugin",
  "version": "0.0.1",
  "silo_api_version": "v1",
  "capabilities": []
}`

func TestLoadWithChecksumOverridesVersionAndStampsChecksum(t *testing.T) {
	m, err := LoadWithChecksum([]byte(testManifestJSON), "9.9.9")
	if err != nil {
		t.Fatalf("LoadWithChecksum: %v", err)
	}
	if m.GetPluginId() != "silo.test.plugin" {
		t.Fatalf("plugin_id: got %q", m.GetPluginId())
	}
	if m.GetVersion() != "9.9.9" {
		t.Fatalf("version override: got %q want 9.9.9", m.GetVersion())
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read exe: %v", err)
	}
	sum := sha256.Sum256(data)
	if want := hex.EncodeToString(sum[:]); m.GetChecksum() != want {
		t.Fatalf("checksum: got %q want %q", m.GetChecksum(), want)
	}
}

func TestLoadWithChecksumEmptyVersionKeepsManifestVersion(t *testing.T) {
	m, err := LoadWithChecksum([]byte(testManifestJSON), "")
	if err != nil {
		t.Fatalf("LoadWithChecksum: %v", err)
	}
	if m.GetVersion() != "0.0.1" {
		t.Fatalf("version: got %q want 0.0.1", m.GetVersion())
	}
}
