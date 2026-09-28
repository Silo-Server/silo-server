package naming

import "testing"

func TestInferRootAssignments_DottedSeparatorDetectsSeasonStructure(t *testing.T) {
	files := []string{
		"/tv/Andor/Season 01/Andor.s01.e01.mkv",
		"/tv/Andor/Season 01/Andor.s01.e02.mkv",
	}
	snapshots, assignments := InferRootAssignments(files, "series", 1, nil)

	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1", len(snapshots))
	}
	if got, want := snapshots[0].InferredType, "series"; got != want {
		t.Errorf("InferredType = %q, want %q", got, want)
	}
	if got, want := snapshots[0].RootPath, "/tv/Andor"; got != want {
		t.Errorf("RootPath = %q, want %q", got, want)
	}

	assignment, ok := assignments["/tv/Andor/Season 01/Andor.s01.e01.mkv"]
	if !ok {
		t.Fatal("expected an assignment for the first episode")
	}
	if !assignment.HasEpisodePattern {
		t.Error("HasEpisodePattern = false, want true for a dotted s01.e01 filename")
	}
}
