package apiv2

import (
	"context"
	"net/http"
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
