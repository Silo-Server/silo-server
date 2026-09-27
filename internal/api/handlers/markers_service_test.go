package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"reflect"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
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
	files := fakeMarkerFiles{file: &models.MediaFile{ID: 5, ContentID: "item", Duration: 100}}
	writer := &markerServiceWriter{}
	notifier := &markerServiceNotifier{}
	h := NewMarkersHandler(files, writer, nil, nil, notifier, nil)
	h.Authorizer = &MediaFileAuthorizer{FileResolver: files, ItemAccess: stubItemAccessChecker{}}
	return h, writer, notifier
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

func TestMarkerServiceItemSkipsInaccessibleFile(t *testing.T) {
	first := &models.MediaFile{ID: 1, ContentID: "denied", Duration: 100}
	second := &models.MediaFile{ID: 2, ContentID: "allowed", Duration: 100}
	files := fakeMarkerFiles{byID: map[int]*models.MediaFile{1: first, 2: second}, contentFiles: []*models.MediaFile{nil, first, second}}
	writer := &markerServiceWriter{}
	h := NewMarkersHandler(files, writer, nil, nil, nil, nil)
	var checked []string
	h.Authorizer = &MediaFileAuthorizer{FileResolver: files, ItemAccess: markerServiceAccess(func(_ context.Context, id string, access catalog.AccessFilter) error {
		checked = append(checked, id)
		if access.ProfileID != "viewer" {
			t.Fatal("access filter lost")
		}
		if id == "denied" {
			return catalog.ErrItemNotFound
		}
		return nil
	})}
	_, err := h.SetMarkers(t.Context(), catalog.AccessFilter{ProfileID: "viewer"}, MarkerTarget{ItemID: "item"}, MarkerChanges{"recap": {End: new(20.0)}})
	if err != nil {
		t.Fatal(err)
	}
	if writer.fileID != 2 || !reflect.DeepEqual(checked, []string{"denied", "allowed"}) {
		t.Fatalf("file=%d checked=%v", writer.fileID, checked)
	}
}

func TestMarkerServiceValidatesWholeChangeBeforeWriting(t *testing.T) {
	tests := map[string]MarkerChanges{
		"invalid bounds":  {"intro": {End: new(10.0)}, "credits": nil, "preview": {Start: new(90.0), End: new(80.0)}},
		"unknown segment": {"intro": {End: new(10.0)}, "unknown": nil},
		"beyond duration": {"intro": {End: new(102.0)}},
		"nonfinite":       {"intro": {End: new(math.NaN())}},
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

func TestMarkerServicePresenceDefaultsAndAudit(t *testing.T) {
	h, w, n := markerServiceFixture()
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 7, APIKeyID: 9, ImpersonatorUserID: new(8)})
	ctx = MarkerRequestAuditContext(ctx, "marker-service-test")
	_, err := h.SetMarkers(ctx, catalog.AccessFilter{}, MarkerTarget{FileID: 5}, MarkerChanges{
		"intro": {End: new(10.0)}, "credits": {Start: new(90.0)}, "recap": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 || len(w.upserts) != 1 || n.calls != 1 {
		t.Fatalf("writes=%d updates=%d notifications=%d", w.calls, len(w.upserts), n.calls)
	}
	u := w.upserts[0]
	if u.IntroStart == nil || *u.IntroStart != 0 || u.CreditsEnd == nil || *u.CreditsEnd != 100 || u.PreviewStart != nil || u.PreviewEnd != nil {
		t.Fatalf("defaults/presence wrong: %+v", u)
	}
	if !reflect.DeepEqual(w.clears, [][]string{{"recap"}}) {
		t.Fatalf("clears=%v", w.clears)
	}
	if len(w.audits) != 1 || w.audits[0].UserID == nil || *w.audits[0].UserID != 7 || w.audits[0].APIKeyID == nil || *w.audits[0].APIKeyID != 9 || w.audits[0].ImpersonatorUserID == nil || *w.audits[0].ImpersonatorUserID != 8 || w.audits[0].UserAgent != "marker-service-test" {
		t.Fatalf("audit lost: %+v", w.audits)
	}
}

func TestMarkerServiceCancellationAndWriterFailure(t *testing.T) {
	t.Run("authorization cancellation", func(t *testing.T) {
		h, w, n := markerServiceFixture()
		h.Authorizer.ItemAccess = markerServiceAccess(func(ctx context.Context, _ string, _ catalog.AccessFilter) error { return ctx.Err() })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := h.SetMarkers(ctx, catalog.AccessFilter{}, MarkerTarget{FileID: 5}, MarkerChanges{"intro": nil})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
		if w.calls != 0 || n.calls != 0 {
			t.Fatal("canceled authorization caused effects")
		}
	})
	t.Run("writer failure", func(t *testing.T) {
		h, w, n := markerServiceFixture()
		w.err = errors.New("writer unavailable")
		_, err := h.SetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5}, MarkerChanges{"intro": nil})
		requireMarkerServiceStatus(t, err, http.StatusInternalServerError)
		if w.calls != 1 || n.calls != 0 {
			t.Fatalf("writer calls=%d notifications=%d", w.calls, n.calls)
		}
	})
}

func TestMarkerServiceUnknownDurationRequiresEnd(t *testing.T) {
	h, w, n := markerServiceFixture()
	h.Files.(fakeMarkerFiles).file.Duration = 0
	_, err := h.SetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5}, MarkerChanges{"credits": {Start: new(10.0)}})
	requireMarkerServiceStatus(t, err, http.StatusBadRequest)
	if w.calls != 0 || n.calls != 0 {
		t.Fatal("invalid duration default caused effects")
	}
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
