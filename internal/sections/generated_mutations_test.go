package sections

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSectionGeneratedWritersAdvanceRevisionsDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	homeRevision, err := f.repo.ScopeRevision(t.Context(), "home", nil)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := f.repo.CreateGeneratedHomeLibraryRecentSections(t.Context(), f.library, "Original Library", "movies")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.repo.DeleteGeneratedHomeLibraryRecentSections(context.Background(), f.library) })
	if len(generated) == 0 {
		t.Fatal("no generated sections")
	}
	assertScopeAdvanced := func(before int64) int64 {
		t.Helper()
		after, err := f.repo.ScopeRevision(t.Context(), "home", nil)
		if err != nil {
			t.Fatal(err)
		}
		if after <= before {
			t.Fatalf("home scope revision did not advance: %d -> %d", before, after)
		}
		return after
	}
	homeRevision = assertScopeAdvanced(homeRevision)
	rowRevision := f.revision(t, generated[0].ID)
	if err := f.repo.SyncGeneratedHomeLibraryRecentTitles(t.Context(), f.library, "Original Library", "Renamed Library"); err != nil {
		t.Fatal(err)
	}
	homeRevision = assertScopeAdvanced(homeRevision)
	updated, err := f.repo.GetByID(t.Context(), generated[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title == generated[0].Title {
		t.Fatal("generated title did not change")
	}
	if f.revision(t, updated.ID) <= rowRevision {
		t.Fatal("generated title did not invalidate row witness")
	}
	rowRevision = f.revision(t, updated.ID)
	if err := f.repo.DeleteGeneratedHomeLibraryRecentSections(t.Context(), f.library); err != nil {
		t.Fatal(err)
	}
	assertScopeAdvanced(homeRevision)
	var tombstone int64
	if err := f.pool.QueryRow(t.Context(), `SELECT revision FROM page_section_revisions WHERE section_id=$1`, updated.ID).Scan(&tombstone); err != nil {
		t.Fatal(err)
	}
	if tombstone <= rowRevision {
		t.Fatal("generated deletion did not retain advanced row witness")
	}
}

func TestSectionFeaturedWritersAdvanceRevisionsDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	s := f.section("Featured")
	s.Featured = true
	// A preexisting template section may have no collection reference after a
	// historical import. Clearing/deleting it must remain possible.
	s.SectionType = SectionCollection
	s.Config = json.RawMessage(`{"generated_source":"template_bundle_featured","template_bundle":"revision-test"}`)
	s, err := f.repo.Create(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	rowRevision := f.revision(t, s.ID)
	scopeRevision, err := f.repo.ScopeRevision(t.Context(), "library", &f.library)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.ClearFeaturedForSurface(t.Context(), "library", &f.library, ""); err != nil {
		t.Fatal(err)
	}
	if f.revision(t, s.ID) <= rowRevision {
		t.Fatal("clear featured did not invalidate row witness")
	}
	currentScope, err := f.repo.ScopeRevision(t.Context(), "library", &f.library)
	if err != nil || currentScope <= scopeRevision {
		t.Fatalf("clear featured scope revision: %d %v", currentScope, err)
	}
	rowRevision = f.revision(t, s.ID)
	if err := f.repo.DeleteGeneratedTemplateBundleFeaturedSections(t.Context(), "revision-test", []int{f.library}); err != nil {
		t.Fatal(err)
	}
	var tombstone int64
	if err := f.pool.QueryRow(t.Context(), `SELECT revision FROM page_section_revisions WHERE section_id=$1`, s.ID).Scan(&tombstone); err != nil {
		t.Fatal(err)
	}
	if tombstone <= rowRevision {
		t.Fatal("template cleanup did not advance deleted row witness")
	}
	nextScope, err := f.repo.ScopeRevision(t.Context(), "library", &f.library)
	if err != nil || nextScope <= currentScope {
		t.Fatalf("template cleanup scope revision: %d %v", nextScope, err)
	}
}
