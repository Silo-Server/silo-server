package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

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
func TestAdminSectionRestoreUnsupportedBeforeDefinitionRead(t *testing.T) {
	h := &SectionHandler{}
	_, err := h.RestoreAdminSections(t.Context(), AdminSectionRestore{Scope: "home", ResetProfiles: true})
	e, ok := errors.AsType[*APIError](err)
	if !ok || e.Status != 501 {
		t.Fatalf("unsupported reset must refuse before repo read: %v", err)
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

func TestAdminSectionCreateRefusesNextUp(t *testing.T) {
	_, err := (&SectionHandler{}).CreateAdminSection(t.Context(), AdminSectionCreate{
		Title: "On Deck", SectionType: string(sections.SectionNextUp), ValidateRecipe: true,
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusBadRequest || !strings.Contains(apiErr.Message, "next_up") {
		t.Fatalf("error = %#v, want a 400 naming next_up", err)
	}
}
