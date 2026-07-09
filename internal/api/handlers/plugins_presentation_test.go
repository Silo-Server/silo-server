package handlers

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestToPluginPresentationJSONPreservesOperatorMetadata(t *testing.T) {
	t.Parallel()

	got := toPluginPresentationJSON(&pluginv1.PluginPresentation{
		DisplayName:         "Example Plugin",
		Summary:             "Explains the example.",
		DescriptionMarkdown: "Longer description.",
		SetupMarkdown:       "Configure the example.",
		HomepageUrl:         "https://example.com",
		SourceUrl:           "https://github.com/Silo-Server/example-plugin",
		SupportUrl:          "https://github.com/Silo-Server/example-plugin/issues",
		ChangelogUrl:        "https://github.com/Silo-Server/example-plugin/releases",
		PublisherName:       "Silo",
		PublisherUrl:        "https://github.com/Silo-Server",
		LicenseSpdx:         "AGPL-3.0-or-later",
	})

	if got == nil {
		t.Fatal("toPluginPresentationJSON() = nil")
	}
	if got.DisplayName != "Example Plugin" || got.Summary != "Explains the example." {
		t.Fatalf("identity fields = %#v", got)
	}
	if got.SourceURL != "https://github.com/Silo-Server/example-plugin" {
		t.Fatalf("source_url = %q", got.SourceURL)
	}
	if got.ChangelogURL != "https://github.com/Silo-Server/example-plugin/releases" {
		t.Fatalf("changelog_url = %q", got.ChangelogURL)
	}
}

func TestToPluginPresentationJSONKeepsLegacyManifestOptional(t *testing.T) {
	t.Parallel()

	if got := toPluginPresentationJSON(nil); got != nil {
		t.Fatalf("toPluginPresentationJSON(nil) = %#v, want nil", got)
	}
}
