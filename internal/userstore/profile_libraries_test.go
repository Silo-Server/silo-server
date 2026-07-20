package userstore

import (
	"reflect"
	"testing"
)

func TestAttachAllowedLibraries(t *testing.T) {
	profiles := []Profile{
		{ID: "p1", AllowedLibraryIDs: []int{99}},
		{ID: "p2", AllowedLibraryIDs: []int{99}},
		{ID: "p3", AllowedLibraryIDs: []int{99}},
	}
	allowedLibraries := []ProfileAllowedLibrary{
		{ProfileID: "p2", LibraryID: 1},
		{ProfileID: "p1", LibraryID: 2},
		{ProfileID: "p1", LibraryID: 5},
	}

	AttachAllowedLibraries(profiles, allowedLibraries)

	if got, want := profiles[0].AllowedLibraryIDs, []int{2, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("p1 AllowedLibraryIDs = %v, want %v", got, want)
	}
	if got, want := profiles[1].AllowedLibraryIDs, []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("p2 AllowedLibraryIDs = %v, want %v", got, want)
	}
	if profiles[2].AllowedLibraryIDs != nil {
		t.Fatalf("p3 AllowedLibraryIDs = %v, want nil", profiles[2].AllowedLibraryIDs)
	}
}
