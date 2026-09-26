package handlers

import (
	"reflect"
	"testing"
)

func TestLibraryPathsRejectBlankBeforeCatalogAccess(t *testing.T) {
	h := &LibraryHandler{}
	for _, paths := range [][]string{{""}, {" \t "}, {"/media", " "}} {
		if _, err := h.CreateLibrary(t.Context(), LibraryCreateRequest{Paths: paths, Type: "movies", Name: "Movies"}); err == nil {
			t.Errorf("create accepted %q", paths)
		}
		if _, err := h.UpdateLibrary(t.Context(), 1, 1, LibraryUpdateRequest{Paths: &paths}); err == nil {
			t.Errorf("update accepted %q", paths)
		}
	}
}

func TestNormalizeLibraryPaths(t *testing.T) {
	paths := []string{" /media/movies/../films/ ", "/media/tv"}
	got, err := normalizeLibraryPaths(paths)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/media/films", "/media/tv"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	if paths[0] != " /media/movies/../films/ " {
		t.Fatal("mutated caller paths")
	}
}
