package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type adminSectionPreviewSpy struct {
	user     int
	profile  string
	filter   catalog.AccessFilter
	resolved sections.ResolvedSection
}

func (s *adminSectionPreviewSpy) FetchOne(_ context.Context, resolved sections.ResolvedSection, _ *int, _ []int, user int, profile string, filter catalog.AccessFilter) (sections.SectionWithItems, error) {
	s.user = user
	s.profile = profile
	s.filter = filter
	s.resolved = resolved
	return sections.SectionWithItems{TotalCount: 77}, nil
}
func TestAdminSectionPreviewPreservesViewerAndBoundedSample(t *testing.T) {
	spy := &adminSectionPreviewSpy{}
	h := &SectionHandler{previewFetcher: spy}
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 21})
	ctx = apimw.SetProfileID(ctx, "selected")
	ctx = access.SetScope(ctx, access.Scope{AllowedLibraryIDs: []int{4}, DisabledLibraryIDs: []int{5}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}})
	result, err := h.PreviewAdminSection(ctx, AdminSectionPreviewRequest{SectionType: string(sections.SectionRecentlyAdded), Config: json.RawMessage(`{}`), ItemLimit: 999})
	if err != nil {
		t.Fatal(err)
	}
	if spy.user != 21 || spy.profile != "selected" || !reflect.DeepEqual(spy.filter.AllowedLibraryIDs, []int{4}) || !reflect.DeepEqual(spy.filter.DisabledLibraryIDs, []int{5}) || spy.filter.MaxContentRating != "PG" {
		t.Fatalf("lost preview context: %+v", spy)
	}
	if spy.resolved.ItemLimit != 10 || result.Items == nil || result.TotalCount != 77 {
		t.Fatalf("unbounded or incorrect preview response %+v %+v", spy.resolved, result)
	}
}
func TestAdminSectionServiceGuardAndDefinitionSemanticsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := sections.NewRepository(f.pool)
	h := NewSectionHandler(repo, nil)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id=$1`, f.library)
	})
	created, err := h.CreateAdminSection(t.Context(), AdminSectionCreate{Scope: "library", LibraryID: &f.library, Title: "Original", SectionType: string(sections.SectionRecentlyAdded), Position: 9, Featured: true, Enabled: true, Config: json.RawMessage(`{"filter_type":"movie"}`)})
	if err != nil {
		t.Fatal(err)
	}
	before, revision, err := h.GetAdminSection(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := h.UpdateAdminSection(WithAdminSectionExpectedRevision(t.Context(), revision), created.ID, AdminSectionUpdate{Title: "Changed", Featured: new(false), Enabled: new(false), Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if after.Position != before.Position || after.Featured || after.Enabled || string(after.Config) != "{}" {
		t.Fatalf("PATCH semantics changed: %+v", after)
	}
	if err = h.DeleteAdminSection(WithAdminSectionExpectedRevision(t.Context(), revision), created.ID); !errors.Is(err, sections.ErrSectionRevisionMismatch) {
		t.Fatalf("stale delete lost guard: %v", err)
	}
	order, err := h.AdminSectionOrder(t.Context(), "library", &f.library)
	if err != nil || !reflect.DeepEqual(order.OrderedIDs, []string{created.ID}) {
		t.Fatalf("canonical disabled row missing %+v %v", order, err)
	}
	if err = h.ReorderAdminSections(WithAdminSectionExpectedRevision(t.Context(), order.Revision), "library", &f.library, []string{created.ID}); err != nil {
		t.Fatal(err)
	}
	if err = h.ReorderAdminSections(WithAdminSectionExpectedRevision(t.Context(), order.Revision), "library", &f.library, []string{created.ID}); !errors.Is(err, sections.ErrSectionRevisionMismatch) {
		t.Fatalf("stale surface lost guard: %v", err)
	}
}
func TestAdminSectionRestoreUnsupportedBeforeDefinitionRead(t *testing.T) {
	h := &SectionHandler{}
	_, err := h.RestoreAdminSections(t.Context(), AdminSectionRestore{Scope: "home", ResetProfiles: true})
	e, ok := errors.AsType[*APIError](err)
	if !ok || e.Status != 501 {
		t.Fatalf("unsupported reset must refuse before repo read: %v", err)
	}
}

func TestAdminSectionWildcardPatchRemergesAfterConcurrentWriteDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := sections.NewRepository(f.pool)
	h := NewSectionHandler(repo, nil)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id=$1`, f.library)
	})
	row, err := h.CreateAdminSection(t.Context(), AdminSectionCreate{Scope: "library", LibraryID: &f.library, Title: "Original", SectionType: string(sections.SectionRecentlyAdded), Position: 9, Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var backend int
	if err = tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&backend); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `SELECT id FROM page_sections WHERE id=$1 FOR UPDATE`, row.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := h.UpdateAdminSection(WithAdminSectionExpectedRevision(t.Context(), -1), row.ID, AdminSectionUpdate{Title: "My draft"})
		result <- e
	}()
	deadline := time.Now().Add(10 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, backend).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
	}
	if !blocked {
		t.Fatal("wildcard writer never reached target lock")
	}
	if _, err = tx.Exec(t.Context(), `UPDATE page_sections SET position=41,enabled=false,config='{"filter_type":"movie"}' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetByID(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "My draft" || updated.Position != 41 || updated.Enabled || !strings.Contains(string(updated.Config), "movie") {
		t.Fatalf("wildcard replay lost concurrent unmentioned fields: %+v", updated)
	}
}

func TestAdminSectionCreateRejectsEmptyEditorsPicks(t *testing.T) {
	_, err := (&SectionHandler{}).CreateAdminSection(t.Context(), AdminSectionCreate{
		Title: "Editor's Picks", SectionType: string(sections.SectionAdminCuratedList),
		Config: json.RawMessage(`{"item_ids":[]}`), ValidateRecipe: true,
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusBadRequest || !strings.Contains(apiErr.Message, "item_ids") {
		t.Fatalf("error = %#v, want a 400 naming item_ids", err)
	}
}

func TestAdminSectionUpdateValidatesRecipeConfigDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	repo := sections.NewRepository(f.pool)
	h := NewSectionHandler(repo, nil)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id=$1`, f.library)
	})
	picks, err := h.CreateAdminSection(t.Context(), AdminSectionCreate{Scope: "library", LibraryID: &f.library, Title: "Picks", SectionType: string(sections.SectionAdminCuratedList), Enabled: true, Config: json.RawMessage(`{"item_ids":["a"]}`), ValidateRecipe: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.UpdateAdminSection(t.Context(), picks.ID, AdminSectionUpdate{Config: json.RawMessage(`{"item_ids":[]}`), ValidateRecipe: true})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("emptying Editor's Picks: error = %#v, want 400", err)
	}
	stored, err := repo.GetByID(t.Context(), picks.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored.Config), `"a"`) {
		t.Fatalf("rejected update changed the row: %s", stored.Config)
	}

	// A row saved before create validated its recipe can still be switched off.
	legacy, err := repo.Create(t.Context(), &sections.PageSection{Scope: "library", LibraryID: &f.library, SectionType: sections.SectionAdminCuratedList, Title: "Empty picks", ItemLimit: 20, Config: json.RawMessage(`{"item_ids":[]}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// The admin editor echoes the unchanged type and config with every save.
	updated, err := h.UpdateAdminSection(t.Context(), legacy.ID, AdminSectionUpdate{
		Enabled: new(false), SectionType: string(legacy.SectionType), Config: json.RawMessage(`{"item_ids": []}`), ValidateRecipe: true,
	})
	if err != nil {
		t.Fatalf("disabling an existing row with its unchanged config echoed: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("row stayed enabled: %+v", updated)
	}
}

// A v1 update can rename or disable a row whose rules use the v2 vocabulary,
// echoing its config unchanged, but cannot save a changed config with one.
func TestAdminSectionV1UpdateKeepsV2RulesDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	h := NewSectionHandler(sections.NewRepository(f.pool), nil)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id=$1`, f.library)
	})
	titleRule := json.RawMessage(`{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"begins_with","value":"the "}]}]}`)
	row, err := h.CreateAdminSection(t.Context(), AdminSectionCreate{Scope: "library", LibraryID: &f.library, Title: "The films", SectionType: string(sections.SectionCustomFilter), Enabled: true, Config: titleRule})
	if err != nil {
		t.Fatalf("v2 create with a title rule: %v", err)
	}
	if _, err := h.UpdateAdminSection(t.Context(), row.ID, AdminSectionUpdate{Title: "Renamed", V1Rules: true}); err != nil {
		t.Fatalf("v1 rename: %v", err)
	}
	if _, err := h.UpdateAdminSection(t.Context(), row.ID, AdminSectionUpdate{Enabled: new(false), Config: titleRule, V1Rules: true}); err != nil {
		t.Fatalf("v1 disable echoing the unchanged config: %v", err)
	}
	changed := json.RawMessage(`{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"ends_with","value":"s"}]}]}`)
	if _, err := h.UpdateAdminSection(t.Context(), row.ID, AdminSectionUpdate{Config: changed, V1Rules: true}); err == nil {
		t.Fatal("v1 saved a changed config with a title rule")
	}
}

// The frozen /api/v1 routes don't request the recipe check, so they keep
// storing what they stored before it existed.
func TestAdminSectionV1WritesSkipRecipeConfigDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	h := NewSectionHandler(sections.NewRepository(f.pool), nil)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id=$1`, f.library)
	})
	empty, err := h.CreateAdminSection(t.Context(), AdminSectionCreate{Scope: "library", LibraryID: &f.library, Title: "Picks", SectionType: string(sections.SectionAdminCuratedList), Enabled: true, Config: json.RawMessage(`{"item_ids":[]}`)})
	if err != nil {
		t.Fatalf("v1 create of an empty Editor's Picks row: %v", err)
	}
	if _, err := h.UpdateAdminSection(t.Context(), empty.ID, AdminSectionUpdate{Config: json.RawMessage(`{"item_ids":[]}`), Title: "Still empty"}); err != nil {
		t.Fatalf("v1 update of an empty Editor's Picks row: %v", err)
	}
}
