package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// GET /api/v1/settings/plugins writes PluginUserSettingsView directly, so this struct
// is the wire shape of a response the bridge carries unchanged. The configured global
// keys reach clients on the v2 installation, which maps them from the Go field rather
// than from its JSON, so a tag here would grow a contract that is meant to be closed.
func TestPluginUserSettingsViewCarriesNoV2OnlyField(t *testing.T) {
	view := PluginUserSettingsView{
		ID:                   3,
		PluginID:             "io.example.markers",
		Version:              "1.2.3",
		UserConfigSchema:     []PluginConfigSchemaView{{Key: "report", Title: "Report", Required: true}},
		Routes:               []PluginRouteView{{ID: "report", Method: "POST", Path: "/report"}},
		Assets:               []PluginAssetView{{Path: "icon.svg", ContentType: "image/svg+xml"}},
		Category:             "Tools",
		ConfiguredGlobalKeys: []string{"account"},
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	want := `{"id":3,"plugin_id":"io.example.markers","version":"1.2.3",` +
		`"user_config_schema":[{"key":"report","title":"Report","description":"","json_schema":"","required":true}],` +
		`"routes":[{"id":"report","method":"POST","path":"/report","access":"","navigable":false,` +
		`"navigation_label":"","navigation_kind":"","static_asset":false}],` +
		`"assets":[{"path":"icon.svg","content_type":"image/svg+xml","integrity":""}],` +
		`"category":"Tools"}`
	if string(encoded) != want {
		t.Fatalf("v1 user settings wire = %s, want %s", encoded, want)
	}
	if strings.Contains(string(encoded), "configured_global_keys") {
		t.Fatalf("v1 user settings wire carries configured_global_keys: %s", encoded)
	}

	// The same value must still be readable in Go, because the v2 projection maps
	// from it.
	if got := view.ConfiguredGlobalKeys; len(got) != 1 || got[0] != "account" {
		t.Fatalf("configured global keys lost from the struct: %#v", got)
	}
}
