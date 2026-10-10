package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type sectionResolverFunc func(context.Context, CatalogRequest) (SectionDefinition, error)

func (f sectionResolverFunc) ResolveCatalogSection(ctx context.Context, req CatalogRequest) (SectionDefinition, error) {
	return f(ctx, req)
}

func TestCatalogSectionResolver(t *testing.T) {
	req := CatalogRequest{Source: CatalogSourceSection, Scope: "library", LibraryID: 7, SectionID: "personal-row"}
	resolver := new(CatalogResolver).WithSectionResolver(sectionResolverFunc(func(_ context.Context, got CatalogRequest) (SectionDefinition, error) {
		if got.Scope != req.Scope || got.LibraryID != req.LibraryID || got.SectionID != req.SectionID {
			t.Fatalf("lookup = %+v, want %+v", got, req)
		}
		return SectionDefinition{SectionType: "collection", Title: "Personal", ItemLimit: 4,
			Config: json.RawMessage(`{"user_collection_id":"mine"}`)}, nil
	}))
	section, err := resolver.loadCatalogSection(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if section.UserCollectionID != "mine" || section.Title != "Personal" || section.LibraryID == nil || *section.LibraryID != 7 {
		t.Fatalf("resolved row = %+v", section)
	}
	for _, want := range []error{ErrCatalogSourceNotFound, errors.New("profile store unavailable")} {
		resolver.WithSectionResolver(sectionResolverFunc(func(context.Context, CatalogRequest) (SectionDefinition, error) {
			return SectionDefinition{}, want
		}))
		// No DB is installed: a fallback would panic instead of propagating
		// the authoritative result from the profile-aware lookup.
		if _, err := resolver.loadCatalogSection(t.Context(), req); !errors.Is(err, want) {
			t.Fatalf("lookup error = %v, want %v", err, want)
		}
	}
}
