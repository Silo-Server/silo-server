package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestInferGroupAssignments_FileIdentityOverrideSplitsCollidingKey(t *testing.T) {
	t.Parallel()

	// Two different films whose names parse to the same title+year: without an
	// override they merge into one group as fake "versions".
	filePaths := []string{
		"/Media/Movies/The Grudge (2004)/The Grudge (2004).mkv",
		"/Media/Movies/The Grudge (2004)/The Grudge (2004) JP Original.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	merged := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)
	if got, want := len(merged.ScannedGroups), 1; got != want {
		t.Fatalf("pre-override len(ScannedGroups) = %d, want %d", got, want)
	}

	overrides := newIdentityOverrideSet([]models.MediaIdentityOverride{{
		MediaFolderID: 1,
		Scope:         "file",
		FilePath:      filePaths[1],
		ForcedTitle:   "Ju-on: The Grudge",
		ForcedTmdbID:  "11838",
	}})
	split := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, overrides)

	if got, want := len(split.ScannedGroups), 2; got != want {
		t.Fatalf("post-override len(ScannedGroups) = %d, want %d", got, want)
	}
	kept := split.Assignments[filePaths[0]]
	moved := split.Assignments[filePaths[1]]
	if kept.ContentGroupKey == moved.ContentGroupKey {
		t.Fatalf("override did not split group key: %q", kept.ContentGroupKey)
	}
	if moved.TmdbID != "11838" {
		t.Fatalf("moved TmdbID = %q, want %q", moved.TmdbID, "11838")
	}
	if moved.State != "resolved" || moved.Confidence != "high" {
		t.Fatalf("moved state/confidence = %q/%q, want resolved/high", moved.State, moved.Confidence)
	}
	if !moved.Overridden || kept.Overridden {
		t.Fatalf("Overridden flags = moved:%v kept:%v, want true/false", moved.Overridden, kept.Overridden)
	}

	// The shared folder now intentionally hosts two groups; the location must
	// not stay flagged ambiguous forever.
	if got, want := len(split.Locations), 1; got != want {
		t.Fatalf("len(Locations) = %d, want %d", got, want)
	}
	if got, want := split.Locations[0].State, "resolved"; got != want {
		t.Fatalf("location State = %q, want %q", got, want)
	}

	// Overridden groups surface as manual for admin visibility.
	overrideSources := map[string]bool{}
	for _, group := range split.ScannedGroups {
		overrideSources[group.OverrideSource] = true
	}
	if !overrideSources["manual"] || !overrideSources["none"] {
		t.Fatalf("override sources = %v, want both manual and none", overrideSources)
	}
}

func TestInferGroupAssignments_RootIdentityOverrideRelinksFolder(t *testing.T) {
	t.Parallel()

	// Two folders that parse to the same identity; a root-scope override on the
	// second forces it to a different film.
	filePaths := []string{
		"/Media/Movies/Crash (2004)/Crash (2004).mkv",
		"/Media/Movies/Crash (2004) Cronenberg/Crash (2004).mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	overrides := newIdentityOverrideSet([]models.MediaIdentityOverride{{
		MediaFolderID: 1,
		Scope:         "root",
		RootPath:      "/Media/Movies/Crash (2004) Cronenberg",
		ForcedTmdbID:  "10723",
	}})
	result := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, overrides)

	kept := result.Assignments[filePaths[0]]
	moved := result.Assignments[filePaths[1]]
	if kept.ContentGroupKey == moved.ContentGroupKey {
		t.Fatalf("root override did not split group key: %q", kept.ContentGroupKey)
	}
	if moved.TmdbID != "10723" {
		t.Fatalf("moved TmdbID = %q, want %q", moved.TmdbID, "10723")
	}
	if kept.TmdbID != "" {
		t.Fatalf("kept TmdbID = %q, want empty", kept.TmdbID)
	}
}

func TestInferGroupAssignments_FileOverrideBeatsRootOverride(t *testing.T) {
	t.Parallel()

	filePath := "/Media/Movies/Pack (2001)/Pack (2001) Part2.mkv"
	rootInference := inferRootAssignments([]string{filePath}, "movies", 1, nil)
	overrides := newIdentityOverrideSet([]models.MediaIdentityOverride{
		{MediaFolderID: 1, Scope: "root", RootPath: "/Media/Movies/Pack (2001)", ForcedTmdbID: "100"},
		{MediaFolderID: 1, Scope: "file", FilePath: filePath, ForcedTmdbID: "200"},
	})
	result := inferGroupAssignments([]string{filePath}, "movies", 1, rootInference.Assignments, overrides)

	got := result.Assignments[filePath]
	if got.TmdbID != "200" {
		t.Fatalf("TmdbID = %q, want file-scope override %q", got.TmdbID, "200")
	}
}
