package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/trickplay"
)

type fakeAdminTrickplay struct {
	regenerated []string
	err         error
}

func (f *fakeAdminTrickplay) ItemStatus(_ context.Context, itemID string) ([]trickplay.FileStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	if itemID != "movie:heat-1995" {
		return nil, trickplay.ErrItemNotFound
	}
	generated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return []trickplay.FileStatus{
		{FileID: 42, State: "ready", Servable: true, GeneratedAt: &generated, ThumbnailCount: 720, Width: 300, IntervalMS: 10000, SheetBytes: 2_100_000},
		{FileID: 43, State: "unusable", Failures: 1, LastError: "ffmpeg sampling failed (invalid_data)"},
	}, nil
}

func (f *fakeAdminTrickplay) Regenerate(_ context.Context, itemID string) (int, error) {
	switch itemID {
	case "movie:heat-1995":
		f.regenerated = append(f.regenerated, itemID)
		return 2, nil
	case "movie:off":
		return 0, trickplay.ErrNotOptedIn
	}
	return 0, trickplay.ErrItemNotFound
}

func (f *fakeAdminTrickplay) LibraryStatuses(context.Context) ([]trickplay.LibraryStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []trickplay.LibraryStatus{{LibraryID: 1, Name: "Movies", Pending: 12, Running: 1, Ready: 840, Unusable: 2, SheetBytes: 1_800_000_000}}, nil
}

func TestAdminTrickplay(t *testing.T) {
	admin := &fakeAdminTrickplay{}
	deps := pilotDeps(nil, nil)
	deps.AdminTrickplay = admin
	h := newTestHandler(t, deps)
	acting := bearer(adminToken)

	rec := do(t, h, http.MethodGet, "/api/v2/admin/items/movie:heat-1995/trickplay", "", acting)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var status AdminItemTrickplay
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 2 || status.Files[0].State != "ready" || !status.Files[0].Servable || status.Files[0].GeneratedAt == nil ||
		status.Files[1].LastError == "" || status.Files[1].ThumbnailWidth != 0 {
		t.Fatalf("status %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/items/movie:missing/trickplay", "", acting), TypeNotFound)

	rec = do(t, h, http.MethodPost, "/api/v2/admin/items/movie:heat-1995/trickplay/regenerate", "", acting)
	if rec.Code != http.StatusAccepted || len(admin.regenerated) != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/admin/items/movie:off/trickplay/regenerate", "", acting), TypeCapabilityDisabled)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/admin/items/movie:missing/trickplay/regenerate", "", acting), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/admin/items/movie:heat-1995/trickplay/regenerate", "", bearer(memberToken)), TypePermissionDenied)

	rec = do(t, h, http.MethodGet, "/api/v2/admin/trickplay/libraries", "", acting)
	var libraries AdminTrickplayLibraries
	if err := json.Unmarshal(rec.Body.Bytes(), &libraries); err != nil || rec.Code != http.StatusOK || len(libraries.Items) != 1 || libraries.Items[0].Ready != 840 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	admin.err = errors.New("database down")
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/trickplay/libraries", "", acting), TypeInternalError)

	off := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, off, http.MethodGet, "/api/v2/admin/trickplay/libraries", "", acting), TypeDependencyUnavailable)
}

func adminTrickplayFixtureCases() []fixtureCase {
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "get_admin_item_trickplay_ok", operationID: "getAdminItemTrickplay",
			scenario: "An item's two files: one with published previews, one its library could not make previews of.",
			method:   http.MethodGet, path: "/api/v2/admin/items/movie:heat-1995/trickplay", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminItemTrickplay"},
		{name: "regenerate_admin_item_trickplay_accepted", operationID: "regenerateAdminItemTrickplay",
			scenario: "Both of the item's files queued ahead of the backlog.",
			method:   http.MethodPost, path: "/api/v2/admin/items/movie:heat-1995/trickplay/regenerate", headers: bearer(adminToken),
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminTrickplayRegeneration"},
		{name: "regenerate_admin_item_trickplay_disabled", operationID: "regenerateAdminItemTrickplay",
			scenario: "The item's library does not generate previews.",
			method:   http.MethodPost, path: "/api/v2/admin/items/movie:off/trickplay/regenerate", headers: bearer(adminToken),
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_admin_trickplay_libraries_ok", operationID: "listAdminTrickplayLibraries",
			scenario: "One library generating previews, with its files' progress and storage.",
			method:   http.MethodGet, path: "/api/v2/admin/trickplay/libraries", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminTrickplayLibraries"},
	}
}
