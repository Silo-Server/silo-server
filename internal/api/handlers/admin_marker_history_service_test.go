package handlers

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type historyAuditFake struct {
	ids          []int
	limit, calls int
	global       bool
	err          error
}

func (f *historyAuditFake) ListMarkerEditAudit(_ context.Context, ids []int, limit int) ([]scanner.MarkerEditAuditRow, error) {
	f.ids = ids
	f.limit = limit
	f.calls++
	return nil, f.err
}
func (f *historyAuditFake) ListAllMarkerEditAudit(ctx context.Context, limit int) ([]scanner.MarkerEditAuditRow, error) {
	f.global = true
	return f.ListMarkerEditAudit(ctx, nil, limit)
}
func TestAdminMarkerHistorySelections(t *testing.T) {
	files := fakeMarkerFiles{file: &models.MediaFile{ID: 42, ContentID: "item"}, episodeFiles: []*models.MediaFile{{ID: 42}, nil, {ID: 43}}}
	audit := &historyAuditFake{}
	h := NewMarkersHandler(files, nil, nil, nil, nil, nil)
	h.AuditHistory = audit
	h.Authorizer = &MediaFileAuthorizer{FileResolver: files, ItemAccess: stubItemAccessChecker{}}
	for _, tc := range []struct {
		target MarkerTarget
		ids    []int
		global bool
	}{{MarkerTarget{}, nil, true}, {MarkerTarget{ItemID: "episode"}, []int{42, 43}, false}, {MarkerTarget{FileID: 42}, []int{42}, false}} {
		audit.global = false
		rows, err := h.AdminMarkerHistory(t.Context(), catalog.AccessFilter{}, tc.target, 17)
		if err != nil || rows == nil || audit.limit != 17 || !reflect.DeepEqual(audit.ids, tc.ids) || audit.global != tc.global {
			t.Fatalf("target=%+v rows=%v err=%v audit=%+v", tc.target, rows, err, audit)
		}
	}
	h.Authorizer.ItemAccess = stubItemAccessChecker{err: catalog.ErrItemNotFound}
	before := audit.calls
	_, err := h.AdminMarkerHistory(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 42}, 25)
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 404 || audit.calls != before {
		t.Fatalf("unauthorized: %v calls=%d", err, audit.calls)
	}
	h.Files = fakeMarkerFiles{contentFiles: []*models.MediaFile{{ID: 51}, {ID: 52}}}
	_, err = h.AdminMarkerHistory(t.Context(), catalog.AccessFilter{}, MarkerTarget{ItemID: "movie"}, 25)
	if err != nil || !reflect.DeepEqual(audit.ids, []int{51, 52}) {
		t.Fatalf("content fallback: %v %+v", err, audit)
	}
	h.Files = fakeMarkerFiles{}
	_, err = h.AdminMarkerHistory(t.Context(), catalog.AccessFilter{}, MarkerTarget{ItemID: "missing"}, 25)
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 404 {
		t.Fatalf("missing: %v", err)
	}
	h.AuditHistory = nil
	rows, err := h.AdminMarkerHistory(t.Context(), catalog.AccessFilter{}, MarkerTarget{}, 25)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("disabled: %v %v", rows, err)
	}
}
