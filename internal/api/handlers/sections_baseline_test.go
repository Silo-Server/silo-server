package handlers

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type stubSectionScopeLister struct {
	all []*sections.PageSection
}

func (s *stubSectionScopeLister) ListByScope(_ context.Context, _ string, _ *int) ([]*sections.PageSection, error) {
	out := make([]*sections.PageSection, 0, len(s.all))
	for _, row := range s.all {
		if row.Enabled {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *stubSectionScopeLister) ListByScopeAll(_ context.Context, _ string, _ *int) ([]*sections.PageSection, error) {
	return s.all, nil
}

func TestGetProfileSectionBaselineDistinguishesAllActiveAndVisible(t *testing.T) {
	h := &SectionHandler{
		sectionLister: &stubSectionScopeLister{
			all: []*sections.PageSection{
				{ID: "s-home", Scope: "home", Position: 0, SectionType: sections.SectionContinueWatching, Title: "Continue Watching", Enabled: true},
				{ID: "s-kids", Scope: "home", Position: 1, SectionType: sections.SectionRecentlyAdded, Title: "Kids", Config: json.RawMessage(`{"library_ids":[2]}`), Enabled: true},
				{ID: "s-disabled", Scope: "home", Position: 2, SectionType: sections.SectionCollectionPosters, Title: "Collections", Enabled: false},
			},
		},
	}

	ctx := access.SetScope(context.Background(), access.Scope{
		LibrariesRestricted: true,
		AllowedLibraryIDs:   []int{1},
	})
	got, err := h.GetProfileSectionBaseline(ctx, 1, "home", nil)
	if err != nil {
		t.Fatalf("GetProfileSectionBaseline failed: %v", err)
	}

	if !slices.Equal(got.AllSectionIDs, []string{"s-home", "s-kids", "s-disabled"}) {
		t.Fatalf("AllSectionIDs = %v, want [s-home s-kids s-disabled]", got.AllSectionIDs)
	}
	if !slices.Equal(got.ActiveSectionIDs, []string{"s-home", "s-kids"}) {
		t.Fatalf("ActiveSectionIDs = %v, want [s-home s-kids]", got.ActiveSectionIDs)
	}
	if len(got.VisibleSections) != 1 || got.VisibleSections[0].ID != "s-home" {
		t.Fatalf("VisibleSections = %+v, want only s-home", got.VisibleSections)
	}
}

func TestSectionBaselineAndSettingsFallBackWhenNoRowsEnabled(t *testing.T) {
	h := &SectionHandler{
		sectionLister: &stubSectionScopeLister{
			all: []*sections.PageSection{
				{ID: "s-disabled-only", Scope: "home", Position: 0, SectionType: sections.SectionCollectionPosters, Title: "Collections", Enabled: false},
			},
		},
	}

	baseline, err := h.GetProfileSectionBaseline(context.Background(), 1, "home", nil)
	if err != nil {
		t.Fatalf("GetProfileSectionBaseline failed: %v", err)
	}
	if !slices.Contains(baseline.AllSectionIDs, "s-disabled-only") {
		t.Fatalf("AllSectionIDs = %v, expected disabled stored row to remain included", baseline.AllSectionIDs)
	}
	if len(baseline.ActiveSectionIDs) == 0 || !slices.Contains(baseline.ActiveSectionIDs, "default-continue-watching") {
		t.Fatalf("ActiveSectionIDs = %v, expected fallback home defaults", baseline.ActiveSectionIDs)
	}
	if slices.Contains(baseline.ActiveSectionIDs, "s-disabled-only") {
		t.Fatalf("ActiveSectionIDs = %v, disabled row should not be active", baseline.ActiveSectionIDs)
	}
	if len(baseline.VisibleSections) == 0 {
		t.Fatal("VisibleSections is empty, expected fallback home defaults")
	}

	settings, err := h.ResolveProfileSectionSettings(context.Background(), 1, "p-1", "home", nil, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("ResolveProfileSectionSettings failed: %v", err)
	}
	if len(settings) == 0 || settings[0].ID != "default-continue-watching" {
		t.Fatalf("ResolveProfileSectionSettings = %+v, expected fallback home defaults so hidden fallback rows stay reversible", settings)
	}
}
