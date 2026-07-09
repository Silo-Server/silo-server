package arrwebhook

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func paths(changes []autoscan.Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.SourcePath)
	}
	return out
}

func TestParseSonarrDownload(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "sonarr_download.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Provider != ProviderSonarr || parsed.EventType != "Download" || parsed.Test {
		t.Fatalf("unexpected classification: %+v", parsed)
	}
	want := "/data/tv/Example Show/Season 02/Example Show - S02E01 - The One That Imports.mkv"
	if len(parsed.Changes) != 1 || parsed.Changes[0].SourcePath != want || parsed.Changes[0].Scope != autoscan.ChangeScopeFile {
		t.Fatalf("changes = %+v", parsed.Changes)
	}
}

func TestParseSonarrRenameIncludesPreviousPaths(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "sonarr_rename.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := paths(parsed.Changes)
	want := []string{
		"/data/tv/Example Show/Season 02/Example Show - S02E01.mkv",
		"/data/tv/Example Show/Season 02/old-name-e01.mkv",
		"/data/tv/Example Show/Season 02/Example Show - S02E02.mkv",
		"/data/tv/Example Show/Season 02/old-name-e02.mkv",
	}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, c := range parsed.Changes {
		if c.Scope != autoscan.ChangeScopeFile {
			t.Fatalf("rename changes must be file scope, got %+v", c)
		}
	}
}

func TestParseSonarrEpisodeFileDelete(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "sonarr_episodefile_delete.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.EventType != "EpisodeFileDelete" {
		t.Fatalf("event type = %q", parsed.EventType)
	}
	if len(parsed.Changes) != 1 || parsed.Changes[0].SourcePath != "/data/tv/Example Show/Season 02/Example Show - S02E01.mkv" {
		t.Fatalf("changes = %+v", parsed.Changes)
	}
}

func TestParseSonarrTest(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "sonarr_test.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !parsed.Test || len(parsed.Changes) != 0 {
		t.Fatalf("test event must be a no-op: %+v", parsed)
	}
	if parsed.Provider != ProviderSonarr {
		t.Fatalf("provider = %q", parsed.Provider)
	}
}

func TestParseRadarrDownload(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "radarr_download.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Provider != ProviderRadarr || parsed.EventType != "Download" {
		t.Fatalf("unexpected classification: %+v", parsed)
	}
	want := "/data/movies/Example Movie (2024)/Example Movie (2024) Bluray-1080p.mkv"
	if len(parsed.Changes) != 1 || parsed.Changes[0].SourcePath != want {
		t.Fatalf("changes = %+v", parsed.Changes)
	}
}

func TestParseUpgradeIncludesReplacedFiles(t *testing.T) {
	for name, body := range map[string]string{
		"sonarr": `{
			"eventType": "Download",
			"series": {"path": "/data/tv/Show"},
			"episodeFile": {"path": "/data/tv/Show/new.mkv"},
			"deletedFiles": [{"path": "/data/tv/Show/old.mkv"}],
			"isUpgrade": true
		}`,
		"radarr": `{
			"eventType": "Download",
			"movie": {"folderPath": "/data/movies/Movie"},
			"movieFile": {"path": "/data/movies/Movie/new.mkv"},
			"deletedFiles": [{"path": "/data/movies/Movie/old.mkv"}],
			"isUpgrade": true
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := Parse(ProviderAuto, []byte(body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := paths(parsed.Changes)
			if len(got) != 2 || got[1] == "" || got[1] == got[0] {
				t.Fatalf("upgrade paths = %v, want new and replaced file", got)
			}
			for _, change := range parsed.Changes {
				if change.Scope != autoscan.ChangeScopeFile {
					t.Fatalf("upgrade change = %+v, want file scope", change)
				}
			}
		})
	}
}

func TestParseRadarrRename(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "radarr_rename.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := paths(parsed.Changes)
	want := []string{
		"/data/movies/Example Movie (2024)/Example Movie (2024) Bluray-1080p.mkv",
		"/data/movies/Example Movie (2024)/old-release-name.mkv",
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestParseRadarrMovieFileDelete(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "radarr_moviefile_delete.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.EventType != "MovieFileDelete" || len(parsed.Changes) != 1 {
		t.Fatalf("unexpected parse: %+v", parsed)
	}
}

func TestParseRadarrTest(t *testing.T) {
	parsed, err := Parse(ProviderAuto, fixture(t, "radarr_test.json"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !parsed.Test || parsed.Provider != ProviderRadarr {
		t.Fatalf("unexpected parse: %+v", parsed)
	}
}

func TestParseUnknownEventIsNoOpNotError(t *testing.T) {
	parsed, err := Parse(ProviderSonarr, fixture(t, "unknown_event.json"))
	if err != nil {
		t.Fatalf("unknown event types must parse: %v", err)
	}
	if parsed.EventType != "Health" || parsed.Test || len(parsed.Changes) != 0 {
		t.Fatalf("unknown event must be a no-op: %+v", parsed)
	}
}

func TestParseMalformedBody(t *testing.T) {
	if _, err := Parse(ProviderAuto, []byte("{not json")); !errors.Is(err, ErrMalformedPayload) {
		t.Fatalf("want ErrMalformedPayload, got %v", err)
	}
	if _, err := Parse(ProviderAuto, []byte(`{"noEventType": true}`)); !errors.Is(err, ErrMalformedPayload) {
		t.Fatalf("missing eventType must be malformed, got %v", err)
	}
}

func TestParseAutoInferenceFailure(t *testing.T) {
	// A work-producing event with neither series nor movie shape cannot be
	// attributed in auto mode.
	body := []byte(`{"eventType": "Download"}`)
	if _, err := Parse(ProviderAuto, body); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("want ErrUnknownProvider, got %v", err)
	}
	// The same payload with an explicit provider parses (zero changes).
	parsed, err := Parse(ProviderSonarr, body)
	if err != nil {
		t.Fatalf("explicit provider must parse: %v", err)
	}
	if len(parsed.Changes) != 0 {
		t.Fatalf("changes = %+v, want none", parsed.Changes)
	}
}

func TestParseImportFallsBackToSubtree(t *testing.T) {
	body := []byte(`{
		"eventType": "Download",
		"series": {"path": "/data/tv/Example Show"}
	}`)
	parsed, err := Parse(ProviderAuto, body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Changes) != 1 ||
		parsed.Changes[0].SourcePath != "/data/tv/Example Show" ||
		parsed.Changes[0].Scope != autoscan.ChangeScopeSubtree {
		t.Fatalf("changes = %+v, want one subtree fallback", parsed.Changes)
	}
}

func TestParseDedupesExactPaths(t *testing.T) {
	body := []byte(`{
		"eventType": "Download",
		"episodeFile": {"path": "/data/tv/S/e1.mkv"},
		"episodeFiles": [{"path": "/data/tv/S/e1.mkv"}, {"path": "/data/tv/S/e2.mkv"}]
	}`)
	parsed, err := Parse(ProviderAuto, body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := paths(parsed.Changes); len(got) != 2 || got[0] != "/data/tv/S/e1.mkv" || got[1] != "/data/tv/S/e2.mkv" {
		t.Fatalf("paths = %v, want deduped pair", got)
	}
}
