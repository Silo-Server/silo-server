package handlers

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestPlayableTargetLibraryIDsIncludesSectionLibraryScope(t *testing.T) {
	req := catalog.CatalogRequest{
		Source:    catalog.CatalogSourceSection,
		Scope:     "library",
		LibraryID: 42,
	}
	if got := playableTargetLibraryIDs(req); !reflect.DeepEqual(got, []int{42}) {
		t.Fatalf("playableTargetLibraryIDs() = %v, want [42]", got)
	}
}

func TestPlayableTargetLibraryIDsUsesQueryLibrariesOtherwise(t *testing.T) {
	req := catalog.CatalogRequest{Query: catalog.QueryDefinition{LibraryIDs: []int{2, 3}}}
	if got := playableTargetLibraryIDs(req); !reflect.DeepEqual(got, []int{2, 3}) {
		t.Fatalf("playableTargetLibraryIDs() = %v, want [2 3]", got)
	}
}
