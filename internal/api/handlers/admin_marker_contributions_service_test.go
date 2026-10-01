package handlers

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"testing"
)

type adminContributionCapture struct {
	calls  int
	opts   markers.ContributeOptions
	fileID int
}

func (f *adminContributionCapture) ContributeFile(_ context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	f.calls++
	f.opts = opts
	f.fileID = file.ID
	return nil, nil
}
func TestAdminMarkerContributionsRequireFileAccess(t *testing.T) {
	files := fakeMarkerFiles{file: &models.MediaFile{ID: 42, ContentID: "item"}}
	f := &adminContributionCapture{}
	h := NewMarkersHandler(files, nil, f, nil, nil, nil)
	h.Authorizer = &MediaFileAuthorizer{FileResolver: files, ItemAccess: stubItemAccessChecker{err: catalog.ErrItemNotFound}}
	_, err := h.ContributeAdminMarkers(t.Context(), catalog.AccessFilter{}, 42, MarkerContributionRequest{})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 404 || f.calls != 0 {
		t.Fatalf("denied: %v calls=%d", err, f.calls)
	}
	_, _, err = h.ListAdminMarkerContributions(t.Context(), catalog.AccessFilter{}, 42, 50, markers.ContributionPagePosition{})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 404 {
		t.Fatalf("list denied: %v", err)
	}
	h.Authorizer.ItemAccess = stubItemAccessChecker{}
	out, err := h.ContributeAdminMarkers(t.Context(), catalog.AccessFilter{}, 42, MarkerContributionRequest{Provider: "p", Segments: []string{"intro"}})
	if err != nil || out == nil || f.calls != 1 || f.fileID != 42 || f.opts.Provider != "p" || len(f.opts.Segments) != 1 {
		t.Fatalf("submit: %v %v %+v", out, err, f)
	}
	_, err = h.ContributeAdminMarkers(t.Context(), catalog.AccessFilter{}, 42, MarkerContributionRequest{Segments: []string{"unknown"}})
	if err == nil || f.calls != 1 {
		t.Fatalf("invalid: %v calls=%d", err, f.calls)
	}
}
