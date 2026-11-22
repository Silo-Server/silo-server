package arrwebhook

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// A series or movie deleted with its files becomes one subtree change for its
// folder, marked Removed; one deleted with its files kept changes nothing.
func TestParseSeriesAndMovieDelete(t *testing.T) {
	removed := func(path string) []autoscan.Change {
		return []autoscan.Change{{SourcePath: path, Scope: autoscan.ChangeScopeSubtree, Removed: true}}
	}
	for _, tc := range []struct {
		name, eventType, provider string
		body                      []byte
		want                      []autoscan.Change
	}{
		{"series, files deleted", "SeriesDelete", ProviderSonarr, fixture(t, "sonarr_series_delete.json"), removed("/data/tv/Example Show")},
		{"movie, files kept", "MovieDelete", ProviderRadarr, fixture(t, "radarr_movie_delete.json"), nil},
		{"series, files kept", "SeriesDelete", ProviderSonarr,
			[]byte(`{"eventType":"SeriesDelete","series":{"path":"/data/tv/Show"},"deletedFiles":false}`), nil},
		{"movie, files deleted", "MovieDelete", ProviderRadarr,
			[]byte(`{"eventType":"MovieDelete","movie":{"folderPath":"/data/movies/Film (2024)","path":"/data/movies/Film (2024)"},"deletedFiles":true}`),
			removed("/data/movies/Film (2024)")},
		{"movie without folderPath", "MovieDelete", ProviderRadarr,
			[]byte(`{"eventType":"MovieDelete","movie":{"path":"/data/movies/Film (2024)"},"deletedFiles":true}`),
			removed("/data/movies/Film (2024)")},
		{"deletedFiles absent", "MovieDelete", ProviderRadarr,
			[]byte(`{"eventType":"MovieDelete","movie":{"folderPath":"/data/movies/Film (2024)"}}`), nil},
		{"no folder named", "MovieDelete", "", []byte(`{"eventType":"MovieDelete","deletedFiles":true}`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := Parse(ProviderAuto, tc.body)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if parsed.EventType != tc.eventType || parsed.Provider != tc.provider || parsed.Test {
				t.Fatalf("want %s from %s, got %+v", tc.eventType, tc.provider, parsed)
			}
			if !reflect.DeepEqual(parsed.Changes, tc.want) {
				t.Fatalf("changes = %+v, want %+v", parsed.Changes, tc.want)
			}
		})
	}
}

// Outside SeriesDelete and MovieDelete, a deletedFiles value that is not a list
// of files is malformed, so a broken upgrade payload is not accepted with its
// replaced files dropped.
func TestParseMalformedDeletedFiles(t *testing.T) {
	for _, deleted := range []string{`[1]`, `{"path":"/tv/x/old.mkv"}`, `"/tv/x/old.mkv"`, `1`, `true`} {
		body := []byte(`{"eventType":"Download","series":{"path":"/tv/x"},"deletedFiles":` + deleted + `}`)
		if _, err := Parse(ProviderAuto, body); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("deletedFiles %s: want ErrMalformedPayload, got %v", deleted, err)
		}
	}
	body := []byte(`{"eventType":"Download","series":{"path":"/tv/x"},"episodeFile":{"path":"/tv/x/new.mkv"},"deletedFiles":null}`)
	if parsed, err := Parse(ProviderAuto, body); err != nil || len(parsed.Changes) != 1 {
		t.Fatalf("deletedFiles null: got %+v, %v; want the imported file only", parsed, err)
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
