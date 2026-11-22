package handlers

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type markerServiceWriter struct {
	fakeMarkerWriter
	calls  int
	fileID int
	err    error
}

func (w *markerServiceWriter) UpsertAndClearMarkers(ctx context.Context, id int, update scanner.MarkerUpdate, clears []string) (bool, error) {
	w.calls++
	w.fileID = id
	if w.err != nil {
		return false, w.err
	}
	return w.fakeMarkerWriter.UpsertAndClearMarkers(ctx, id, update, clears)
}

type markerServiceAccess func(context.Context, string, catalog.AccessFilter) error

func (f markerServiceAccess) EnsureAccessible(ctx context.Context, id string, access catalog.AccessFilter) error {
	return f(ctx, id, access)
}

type markerServiceNotifier struct{ calls int }

func (n *markerServiceNotifier) MarkersUpdated(context.Context, *models.MediaFile) { n.calls++ }

func markerServiceFixture() (*MarkersHandler, *markerServiceWriter, *markerServiceNotifier) {
	file := &models.MediaFile{ID: 5, ContentID: "item", Duration: 100}
	files := fakeMarkerFiles{file: file, contentFiles: []*models.MediaFile{file}}
	writer := &markerServiceWriter{}
	notifier := &markerServiceNotifier{}
	h := newTestMarkersHandler(files, writer, nil, nil, notifier, nil)
	h.Authorizer = &MediaFileAuthorizer{FileResolver: files, ItemAccess: stubItemAccessChecker{}}
	return h, writer, notifier
}
func TestMarkerServiceWriterCancellationPreserved(t *testing.T) {
	h, w, n := markerServiceFixture()
	w.err = context.Canceled
	_, err := h.ClearMarker(t.Context(), catalog.AccessFilter{}, 5, "credits")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writer cancellation=%v", err)
	}
	if w.calls != 1 || n.calls != 0 {
		t.Fatalf("writes=%d notifications=%d", w.calls, n.calls)
	}
}

// A configured library lookup is required for every manual write. Most service
// fixtures represent movies; individual tests override the owning library kind.
type markerLibraryFixture struct {
	kind string
	err  error
}

func (f markerLibraryFixture) GetByID(context.Context, int) (*models.MediaFolder, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &models.MediaFolder{Type: f.kind}, nil
}
func newTestMarkersHandler(files MarkerFileResolver, writer ManualMarkerWriter, contributor MarkerContributor, contributions MarkerContributionLister, notifier PlaybackMarkerUpdateNotifier, logger *slog.Logger) *MarkersHandler {
	h := NewMarkersHandler(files, writer, contributor, contributions, notifier, logger)
	h.Libraries = markerLibraryFixture{kind: "movies"}
	return h
}

func requireMarkerServiceStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("error=%v, want status %d", err, status)
	}
}

func TestMarkerServiceAuthorizationBeforeEffects(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "unconfigured"}[missing], func(t *testing.T) {
			h, w, n := markerServiceFixture()
			status := http.StatusNotFound
			if missing {
				h.Authorizer = nil
				status = http.StatusServiceUnavailable
			} else {
				h.Authorizer.ItemAccess = stubItemAccessChecker{err: catalog.ErrItemNotFound}
			}
			_, err := h.SetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5}, MarkerChanges{"intro": {End: new(10.0)}})
			requireMarkerServiceStatus(t, err, status)
			if w.calls != 0 || n.calls != 0 {
				t.Fatalf("denied operation caused effects: writes=%d notifications=%d", w.calls, n.calls)
			}
		})
	}
}

func TestMarkerServiceValidatesWholeChangeBeforeWriting(t *testing.T) {
	tests := map[string]MarkerChanges{
		"invalid bounds":                        {"intro": {End: new(10.0)}, "credits": nil, "preview": {Start: new(90.0), End: new(80.0)}},
		"unknown segment":                       {"intro": {End: new(10.0)}, "unknown": nil},
		"beyond duration":                       {"intro": {End: new(102.0)}},
		"nonfinite":                             {"intro": {End: new(math.NaN())}},
		"credits at the intro":                  {"intro": {Start: new(5.0), End: new(80.0)}, "credits": {Start: new(76.0)}},
		"credits before the end-credits window": {"credits": {Start: new(60.0)}},
	}
	for name, changes := range tests {
		t.Run(name, func(t *testing.T) {
			h, w, n := markerServiceFixture()
			_, err := h.SetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5}, changes)
			requireMarkerServiceStatus(t, err, http.StatusBadRequest)
			if w.calls != 0 || n.calls != 0 {
				t.Fatal("invalid mixed change caused effects")
			}
		})
	}
}

func TestMarkerServiceRejectsUnsupportedLibrariesBeforeEffects(t *testing.T) {
	for _, kind := range []string{"audiobooks", "ebooks", "podcasts", "manga"} {
		for _, target := range []MarkerTarget{{FileID: 5}, {ItemID: "item"}} {
			h, w, n := markerServiceFixture()
			h.Libraries = markerLibraryFixture{kind: kind}
			for _, changes := range []MarkerChanges{{"intro": {Start: new(10.0), End: new(30.0)}}, {"intro": nil}} {
				_, err := h.SetMarkers(t.Context(), catalog.AccessFilter{}, target, changes)
				requireMarkerServiceStatus(t, err, http.StatusUnprocessableEntity)
			}
			_, err := h.ClearMarker(t.Context(), catalog.AccessFilter{}, 5, "intro")
			requireMarkerServiceStatus(t, err, http.StatusUnprocessableEntity)
			if w.calls != 0 || n.calls != 0 {
				t.Fatalf("%s caused effects: writes=%d notifications=%d", kind, w.calls, n.calls)
			}
		}
	}
}
