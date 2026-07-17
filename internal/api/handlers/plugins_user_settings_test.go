package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestToUserPluginSettingsSummaryIncludesManifestCategory(t *testing.T) {
	t.Parallel()

	installation := &plugins.Installation{
		ID:       42,
		PluginID: "example-audiobooks",
		Version:  "1.2.3",
	}
	manifest := &pluginv1.PluginManifest{
		Category: "Tools/Utilities",
	}

	got := toUserPluginSettingsSummary(installation, manifest)

	if got.Category != "Tools/Utilities" {
		t.Fatalf("Category = %q, want %q", got.Category, "Tools/Utilities")
	}
	if got.ID != 42 || got.PluginID != "example-audiobooks" || got.Version != "1.2.3" {
		t.Fatalf("identity fields = %#v", got)
	}
}

func TestToUserPluginSettingsSummaryOmitsEmptyCategory(t *testing.T) {
	t.Parallel()

	installation := &plugins.Installation{
		ID:       7,
		PluginID: "example-plain",
		Version:  "0.1.0",
	}

	got := toUserPluginSettingsSummary(installation, &pluginv1.PluginManifest{})

	if got.Category != "" {
		t.Fatalf("Category = %q, want empty", got.Category)
	}

	// The field is additive-only; ensure it stays absent from the JSON
	// payload for manifests without a category so existing clients see an
	// unchanged response shape.
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshaling summary: %v", err)
	}
	if strings.Contains(string(encoded), "\"category\"") {
		t.Fatalf("JSON payload should omit empty category, got %s", encoded)
	}
}
