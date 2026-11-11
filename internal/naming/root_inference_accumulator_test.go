package naming

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A root's snapshot counts every file added to it, however the files were
// split into calls, and stores its evidence with the keys in alphabetical
// order. A movie file inside a season-structured, tagged show folder makes the
// root ambiguous.
func TestRootInferenceSnapshotOfAMixedRoot(t *testing.T) {
	t.Parallel()

	const root = "/lib/Alpha (2019) [tvdb-81189]"
	inference := NewRootInference("mixed", 3, nil, "/lib")
	for _, path := range []string{
		root + "/Season 01/Alpha - S01E01.mkv",
		root + "/Season 01/Alpha - S01E02.mkv",
		root + "/Alpha (2019).mkv",
	} {
		if got := inference.Add(path).RootPath; got != root {
			t.Fatalf("Add(%q).RootPath = %q, want %q", path, got, root)
		}
	}

	want := []models.ScannedMediaRoot{{
		MediaFolderID:     3,
		RootPath:          root,
		State:             "ambiguous",
		InferredType:      "series",
		TypeConfidence:    "high",
		Title:             "Alpha",
		Year:              2019,
		TvdbID:            "81189",
		ObservedFileCount: 3,
		SampleFilePath:    root + "/Season 01/Alpha - S01E01.mkv",
		EvidenceJSON: []byte(`{"ancestor_promotions":0,"contradiction_files":0,"episode_pattern_files":2,` +
			`"has_folder_ids":true,"legacy_disagreements":0,"movie_evidence_files":1,"movie_type_votes":1,` +
			`"observed_file_count":3,"override_applied":false,"release_density_files":0,` +
			`"season_structure_files":2,"series_type_votes":2,"wrapper_collapses":0}`),
		OverrideSource: "none",
	}}
	if got := inference.Snapshots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshots() = %+v\nwant %+v\nevidence %s", got, want, got[0].EvidenceJSON)
	}
}
