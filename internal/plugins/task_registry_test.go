package plugins

import "testing"

func TestPluginTaskPresentationPrefersManifestText(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		wantName string
		wantDesc string
	}{
		{
			name:     "manifest text",
			metadata: map[string]any{"display_name": " Refresh feeds ", "description": "Pulls new episodes."},
			wantName: "Refresh feeds (example.feeds)",
			wantDesc: "Pulls new episodes.",
		},
		{
			name:     "identifier fallback",
			metadata: map[string]any{"display_name": "  ", "description": ""},
			wantName: "example.feeds / refresh",
			wantDesc: "Runs plugin scheduled task refresh",
		},
		{
			name:     "no metadata",
			wantName: "example.feeds / refresh",
			wantDesc: "Runs plugin scheduled task refresh",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, desc := pluginTaskPresentation("example.feeds", &Capability{ID: "refresh", Metadata: tt.metadata})
			if name != tt.wantName || desc != tt.wantDesc {
				t.Fatalf("pluginTaskPresentation() = %q, %q; want %q, %q", name, desc, tt.wantName, tt.wantDesc)
			}
		})
	}
}
