package manifest_test

import (
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

func TestLoadAcceptsScanSourceCapability(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [
	    {"type": "scan_source.v1", "id": "arr", "display_name": "X", "description": "Y"}
	  ]
	}`)
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if len(m.GetCapabilities()) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(m.GetCapabilities()))
	}
	if got := m.GetCapabilities()[0].GetType(); got != "scan_source.v1" {
		t.Fatalf("capability type = %q, want scan_source.v1", got)
	}
}
