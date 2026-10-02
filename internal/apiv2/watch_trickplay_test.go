package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/trickplay"
)

// fakeTrickplay publishes previews for file 42 only.
type fakeTrickplay struct {
	err   error
	calls []int
}

func (f *fakeTrickplay) SignedManifest(_ context.Context, fileID int) (trickplay.SignedManifest, bool, error) {
	f.calls = append(f.calls, fileID)
	if f.err != nil {
		return trickplay.SignedManifest{}, false, f.err
	}
	if fileID != 42 {
		return trickplay.SignedManifest{}, false, nil
	}
	return trickplay.SignedManifest{
		Manifest: trickplay.Manifest{FileID: 42, Revision: 9913, Width: 300, Height: 126, TileColumns: 10, TileRows: 10,
			IntervalMS: 10000, ThumbnailCount: 1020, SheetCount: 11, Bandwidth: 1600},
		SheetURLs: []string{
			"/api/v2/artwork/trickplay/42/9913/0.9913.jpg?exp=1767409445&sig=a", "/api/v2/artwork/trickplay/42/9913/1.9913.jpg?exp=1767409445&sig=b",
			"/api/v2/artwork/trickplay/42/9913/2.9913.jpg?exp=1767409445&sig=c", "/api/v2/artwork/trickplay/42/9913/3.9913.jpg?exp=1767409445&sig=d",
			"/api/v2/artwork/trickplay/42/9913/4.9913.jpg?exp=1767409445&sig=e", "/api/v2/artwork/trickplay/42/9913/5.9913.jpg?exp=1767409445&sig=f",
			"/api/v2/artwork/trickplay/42/9913/6.9913.jpg?exp=1767409445&sig=g", "/api/v2/artwork/trickplay/42/9913/7.9913.jpg?exp=1767409445&sig=h",
			"/api/v2/artwork/trickplay/42/9913/8.9913.jpg?exp=1767409445&sig=i", "/api/v2/artwork/trickplay/42/9913/9.9913.jpg?exp=1767409445&sig=j",
			"/api/v2/artwork/trickplay/42/9913/10.9913.jpg?exp=1767409445&sig=k",
		},
		ExpiresAt: time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC),
	}, true, nil
}

func trickplayDeps(watch *fakeWatch, tp *fakeTrickplay) Dependencies {
	deps := watchDeps(watch)
	deps.Trickplay = tp
	return deps
}

func TestGetWatchTrickplay(t *testing.T) {
	watch, tp := &fakeWatch{}, &fakeTrickplay{}
	h := newTestHandler(t, trickplayDeps(watch, tp))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995/trickplay?file_id=42", "", with(owner, deviceIDHeader, "tv-1"))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var body WatchTrickplay
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.FileID != "42" || body.IntervalMS != 10000 || body.ThumbnailWidth != 300 || body.ThumbnailHeight != 126 ||
		body.TileColumns != 10 || body.ThumbnailCount != 1020 || len(body.Sheets) != 11 || body.Sheets[10].Index != 10 ||
		body.ExpiresAt != NewInstant(time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if filter := watch.filters[len(watch.filters)-1]; filter.SelectedFileID != 42 || filter.DeviceID != "tv-1" {
		t.Fatalf("filter = %+v", filter)
	}

	// Only the item's own files; only files with previews.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995/trickplay?file_id=7", "", owner), TypeNotFound)
	if len(tp.calls) != 1 {
		t.Fatalf("previews of a file the item does not list were read: %v", tp.calls)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:missing/trickplay?file_id=42", "", owner), TypeNotFound)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995/trickplay?file_id=abc", "", owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.file_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995/trickplay", "", owner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995/trickplay?file_id=42", "", nil), TypeAuthenticationRequired)

	tp.err = errors.New("database down")
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995/trickplay?file_id=42", "", owner), TypeInternalError)

	off := newTestHandler(t, watchDeps(&fakeWatch{}))
	requireProblem(t, do(t, off, http.MethodGet, "/api/v2/watch/movie:heat-1995/trickplay?file_id=42", "", owner), TypeDependencyUnavailable)
}

func TestWatchVersionsCarryTrickplayAvailability(t *testing.T) {
	watch := &fakeWatch{}
	h := newTestHandler(t, watchDeps(watch))
	rec := do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995", "", bearer(memberToken))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	version := body["versions"].([]any)[0].(map[string]any)
	if available, ok := version["trickplay_available"]; !ok || available != false {
		t.Fatalf("trickplay_available = %v (%t), want false present", available, ok)
	}
}

func watchTrickplayFixtureCases() []fixtureCase {
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "get_watch_trickplay_ok", operationID: "getWatchTrickplay",
			scenario: "The seek-bar previews of a movie's file: eleven sheets of 10x10 thumbnails, one every ten seconds, each sheet URL signed.",
			method:   http.MethodGet, path: "/api/v2/watch/movie:heat-1995/trickplay?file_id=42", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WatchTrickplay"},
		{name: "get_watch_trickplay_not_found", operationID: "getWatchTrickplay",
			scenario: "A file the item does not list, or one without published previews: not found.",
			method:   http.MethodGet, path: "/api/v2/watch/movie:heat-1995/trickplay?file_id=7", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
	}
}

func TestWatchVersionOfReportsTrickplay(t *testing.T) {
	if watchVersionOf(catalogpkg.FileVersion{FileID: 1}).TrickplayAvailable {
		t.Fatal("a version without previews reported them")
	}
	if !watchVersionOf(catalogpkg.FileVersion{FileID: 1, Trickplay: &catalogpkg.TrickplayGrid{Width: 300}}).TrickplayAvailable {
		t.Fatal("a version with previews did not report them")
	}
}
