package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type sectionResolverFunc func(context.Context, CatalogRequest) (SectionDefinition, error)

func (f sectionResolverFunc) ResolveCatalogSection(ctx context.Context, req CatalogRequest) (SectionDefinition, error) {
	return f(ctx, req)
}

// rowBackedSectionResolver answers one row-backed section with items.
type rowBackedSectionResolver struct {
	sectionType string
	items       []*models.MediaItem
	err         error
	limits      []int
}

func (f *rowBackedSectionResolver) ResolveCatalogSection(context.Context, CatalogRequest) (SectionDefinition, error) {
	return SectionDefinition{SectionType: f.sectionType, Title: "Row", ItemLimit: 20, Config: json.RawMessage(`{}`),
		FetchItems: func(_ context.Context, limit int) ([]*models.MediaItem, error) {
			f.limits = append(f.limits, limit)
			return f.items, f.err
		}}, nil
}

func numberedItems(n int) []*models.MediaItem {
	items := make([]*models.MediaItem, n)
	for i := range items {
		items[i] = &models.MediaItem{ContentID: fmt.Sprintf("item-%03d", i)}
	}
	return items
}

func TestRowBackedSectionSourcePagesTheRow(t *testing.T) {
	rows := &rowBackedSectionResolver{sectionType: "recommended_for_you", items: numberedItems(45)}
	resolver := new(CatalogResolver).WithSectionResolver(rows)
	req := CatalogRequest{Source: CatalogSourceSection, Scope: "home", SectionID: "for-you", CursorPaging: true, Limit: 20}

	var got []string
	for offset := 0; ; {
		req.Offset = offset
		result, err := resolver.resolveSectionSource(t.Context(), req, AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 45 || !result.TotalExact {
			t.Fatalf("total = %d exact=%v, want 45 exact", result.Total, result.TotalExact)
		}
		if result.SnapshotAt.IsZero() {
			t.Fatal("row-backed page carries no snapshot for the cursor")
		}
		for _, item := range result.Items {
			got = append(got, item.ContentID)
		}
		offset += len(result.Items)
		if !result.HasMore {
			break
		}
	}
	if len(got) != 45 || got[0] != "item-000" || got[44] != "item-044" {
		t.Fatalf("paged %d items (%v..), want the row's 45 in order", len(got), got[:1])
	}
	for _, limit := range rows.limits {
		if limit != SectionBrowseItemCap {
			t.Fatalf("row fetched with limit %d, want %d", limit, SectionBrowseItemCap)
		}
	}
}

func TestRowBackedSectionSourceTotalIsInexactAtTheCap(t *testing.T) {
	rows := &rowBackedSectionResolver{sectionType: "new_to_library", items: numberedItems(SectionBrowseItemCap)}
	resolver := new(CatalogResolver).WithSectionResolver(rows)
	req := CatalogRequest{Source: CatalogSourceSection, Scope: "home", SectionID: "new", CursorPaging: true, Limit: 20}
	result, err := resolver.resolveSectionSource(t.Context(), req, AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != SectionBrowseItemCap || result.TotalExact {
		t.Fatalf("total = %d exact=%v, want %d inexact", result.Total, result.TotalExact, SectionBrowseItemCap)
	}
}

func TestRowBackedSectionSourceKeepsV1Empty(t *testing.T) {
	rows := &rowBackedSectionResolver{sectionType: "trending_on_server", items: numberedItems(5)}
	resolver := new(CatalogResolver).WithSectionResolver(rows)
	req := CatalogRequest{Source: CatalogSourceSection, Scope: "home", SectionID: "trending", Limit: 20}
	result, err := resolver.resolveSectionSource(t.Context(), req, AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || result.Total != 0 || len(rows.limits) != 0 {
		t.Fatalf("v1 row-backed read = %d items, total %d, %d fetches; want the frozen empty answer",
			len(result.Items), result.Total, len(rows.limits))
	}
}

func TestRowBackedSectionSourcePropagatesFetchErrors(t *testing.T) {
	want := errors.New("recommendations unavailable")
	resolver := new(CatalogResolver).WithSectionResolver(&rowBackedSectionResolver{sectionType: "taste_match", err: want})
	req := CatalogRequest{Source: CatalogSourceSection, Scope: "home", SectionID: "taste", CursorPaging: true, Limit: 20}
	if _, err := resolver.resolveSectionSource(t.Context(), req, AccessFilter{}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
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
