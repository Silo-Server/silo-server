package main

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

func TestPresentationMetadataIsGeneratedForEveryClientLanguage(t *testing.T) {
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}

	tests := []struct {
		name     string
		generate func() ([]byte, error)
		markers  []string
	}{
		{
			name:     "typescript",
			generate: func() ([]byte, error) { return generateTypeScript(contract) },
			markers: []string{
				"export const SETTING_OPTION_SETS",
				`catalog_metadata_languages`,
				`suggestedOptions: "playback_subtitle_languages"`,
				`unsetLabel: "None"`,
				// Advisory platform tags reach the web UI so it can hide
				// settings that do not apply to the device being edited.
				`platforms: ["web"]`,
				`platforms: ["ios", "android"]`,
			},
		},
		{
			name: "kotlin",
			generate: func() ([]byte, error) {
				return generateKotlin(contract, "org.siloserver.silo.model.settings")
			},
			markers: []string{
				"object SettingPresentationMetadata",
				`"playback_audio_languages" to SettingOptionSet`,
				`suggestedOptions = "catalog_metadata_languages"`,
				`unsetLabel = "Library default"`,
			},
		},
		{
			name:     "swift",
			generate: func() ([]byte, error) { return generateSwift(contract) },
			markers: []string{
				"public enum SettingPresentationMetadata",
				`"playback_subtitle_languages": SettingOptionSet`,
				`suggestedOptions: "playback_audio_languages"`,
				`unsetLabel: "No preference"`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			generated, err := tc.generate()
			if err != nil {
				t.Fatalf("generating: %v", err)
			}
			for _, marker := range tc.markers {
				if !strings.Contains(string(generated), marker) {
					t.Errorf("generated output omitted %q", marker)
				}
			}
		})
	}
}

func TestGeneratedOptionOrderMatchesTheManifest(t *testing.T) {
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	generated, err := generateTypeScript(contract)
	if err != nil {
		t.Fatalf("generating TypeScript: %v", err)
	}

	text := string(generated)
	arabic := strings.Index(text, `{ value: "ar", introducedIn: 1 }`)
	bengali := strings.Index(text, `{ value: "bn", introducedIn: 1 }`)
	if arabic < 0 || bengali < 0 || arabic >= bengali {
		t.Fatalf("generated option order does not preserve the manifest: ar=%d bn=%d", arabic, bengali)
	}
}
