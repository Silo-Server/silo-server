package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// LoadWithChecksum loads an embedded manifest, optionally overrides its version,
// and stamps Checksum with the hex sha256 of the running binary. It reads
// os.Executable() itself, so it cannot be precomputed at SDK build time. This is
// the canonical plugin manifest-bootstrap previously copied into each plugin's
// main.go.
func LoadWithChecksum(embedded []byte, version string) (*pluginv1.PluginManifest, error) {
	m, err := Load(embedded)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}
	if version != "" {
		m.Version = version
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", exe, err)
	}
	sum := sha256.Sum256(data)
	m.Checksum = hex.EncodeToString(sum[:])
	return m, nil
}
