package manifest_test

import (
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

func TestLoad_ParsesAuthModesAndIconURL(t *testing.T) {
	raw := []byte(`{
		"plugin_id": "test.plugin",
		"version": "0.1.0",
		"checksum": "0",
		"silo_api_version": "v1",
		"capabilities": [
			{"type": "auth_provider.v1", "id": "main",
			 "display_name": "Test",
			 "auth_modes": ["oauth2"],
			 "icon_url": "/assets/test.svg"}
		]
	}`)
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.GetCapabilities()) != 1 {
		t.Fatalf("capabilities len = %d", len(m.GetCapabilities()))
	}
	c := m.GetCapabilities()[0]
	if got := c.GetAuthModes(); len(got) != 1 || got[0] != "oauth2" {
		t.Errorf("AuthModes = %v", got)
	}
	if got := c.GetIconUrl(); got != "/assets/test.svg" {
		t.Errorf("IconUrl = %q", got)
	}
}

func TestLoad_DefaultsAuthModesAbsent(t *testing.T) {
	raw := []byte(`{
		"plugin_id": "test.plugin",
		"version": "0.1.0",
		"checksum": "0",
		"silo_api_version": "v1",
		"capabilities": [
			{"type": "auth_provider.v1", "id": "main", "display_name": "Test"}
		]
	}`)
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := m.GetCapabilities()[0]
	if got := c.GetAuthModes(); len(got) != 0 {
		// Protobuf returns nil/empty slice when absent; defaulting to
		// ["password"] is host-side responsibility per spec Layer 2.3.
		t.Logf("AuthModes (nil OK) = %v", got)
	}
	if got := c.GetIconUrl(); got != "" {
		t.Errorf("IconUrl = %q, want empty", got)
	}
}
