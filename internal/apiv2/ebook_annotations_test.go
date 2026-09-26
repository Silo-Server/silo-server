package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeEbookAnnotations struct {
	rows  []handlers.EbookReaderAnnotation
	scope handlers.EbookAnnotationScope
	after *handlers.EbookAnnotationPosition
	limit int
	patch handlers.EbookAnnotationPatch
}

func (f *fakeEbookAnnotations) ReaderAnnotationPage(_ context.Context, scope handlers.EbookAnnotationScope, after *handlers.EbookAnnotationPosition, limit int) ([]handlers.EbookReaderAnnotation, error) {
	f.scope = scope
	f.after = after
	f.limit = limit
	return f.rows, nil
}
func (f *fakeEbookAnnotations) CreateReaderAnnotation(_ context.Context, scope handlers.EbookAnnotationScope, id string, req handlers.EbookAnnotationCreate) (*handlers.EbookReaderAnnotation, bool, error) {
	f.scope = scope
	for _, row := range f.rows {
		if row.ID == id {
			return &row, false, nil
		}
	}
	row := handlers.EbookReaderAnnotation{ID: id, ContentID: scope.ContentID, Kind: req.Kind, Location: req.Location, Note: req.Note, Metadata: json.RawMessage(`{}`), CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	f.rows = append(f.rows, row)
	return &row, true, nil
}
func (f *fakeEbookAnnotations) UpdateReaderAnnotation(_ context.Context, scope handlers.EbookAnnotationScope, id string, req handlers.EbookAnnotationPatch, guard handlers.EbookAnnotationGuard) (*handlers.EbookReaderAnnotation, error) {
	f.scope = scope
	for i := range f.rows {
		if f.rows[i].ID == id {
			if err := guard(f.rows[i]); err != nil {
				return nil, err
			}
			f.patch = req
			if req.Note != nil {
				f.rows[i].Note = *req.Note
			}
			f.rows[i].UpdatedAt = f.rows[i].UpdatedAt.Add(time.Second)
			return &f.rows[i], nil
		}
	}
	return nil, handlers.ErrEbookAnnotationNotFound
}
func (f *fakeEbookAnnotations) DeleteReaderAnnotation(_ context.Context, scope handlers.EbookAnnotationScope, id string, guard handlers.EbookAnnotationGuard) error {
	f.scope = scope
	for i, row := range f.rows {
		if row.ID == id {
			if err := guard(row); err != nil {
				return err
			}
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return handlers.ErrEbookAnnotationNotFound
}
func TestEbookAnnotationsTransport(t *testing.T) {
	service := &fakeEbookAnnotations{}
	deps := pilotDeps(nil, nil)
	deps.EbookAnnotations = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/ebooks/book/annotations"
	body := `{"id":"intent1","kind":"note","location":"chapter1","note":"original"}`
	rec := do(t, h, "POST", path, body, viewer)
	if rec.Code != 201 || rec.Header().Get("ETag") == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	tag := rec.Header().Get("ETag")
	rec = do(t, h, "POST", path, strings.ReplaceAll(body, "original", "retry"), viewer)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"note":"original"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if service.scope.UserID != 1 || service.scope.ProfileID != "p-owner" || service.scope.ContentID != "book" {
		t.Fatalf("%+v", service.scope)
	}
	for _, tc := range []struct {
		match, none string
		want        int
	}{
		{"", "", 428}, {`"stale"`, "", 412}, {tag, "*", 412},
	} {
		headers := viewer
		if tc.match != "" {
			headers = with(headers, "If-Match", tc.match)
		}
		if tc.none != "" {
			headers = with(headers, "If-None-Match", tc.none)
		}
		rec = do(t, h, "PATCH", path+"/intent1", `{"note":"changed"}`, headers)
		if rec.Code != tc.want {
			t.Fatalf("guard: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, "PATCH", path+"/intent1", `{"note":null,"metadata":null}`, with(viewer, "If-Match", tag))
	if rec.Code != 200 {
		t.Fatalf("null patch: %d %s", rec.Code, rec.Body.String())
	}
	if service.patch.Note == nil || *service.patch.Note != "" || service.patch.Kind != nil || string(service.patch.Metadata) != "{}" {
		t.Fatalf("%+v", service.patch)
	}
	fresh := rec.Header().Get("ETag")
	rec = do(t, h, "DELETE", path+"/intent1", "", with(viewer, "If-Match", tag))
	if rec.Code != 412 {
		t.Fatalf("stale delete: %d", rec.Code)
	}
	rec = do(t, h, "DELETE", path+"/intent1", "", with(viewer, "If-Match", fresh))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "DELETE", path+"/intent1", "", with(viewer, "If-Match", fresh))
	if rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	rec = do(t, h, "POST", path, `{"id":"large","note":"`+strings.Repeat("x", 256<<10)+`"}`, viewer)
	if rec.Code != 413 {
		t.Fatalf("cap: %d", rec.Code)
	}
	rec = do(t, h, "GET", path, "", nil)
	if rec.Code != 401 {
		t.Fatalf("auth: %d", rec.Code)
	}
}
func TestEbookAnnotationCursorScopeAndBounds(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	service := &fakeEbookAnnotations{rows: []handlers.EbookReaderAnnotation{
		{ID: "b", ContentID: "book", Metadata: json.RawMessage(`{}`), CreatedAt: at, UpdatedAt: at},
		{ID: "a", ContentID: "book", Metadata: json.RawMessage(`{}`), CreatedAt: at, UpdatedAt: at},
	}}
	deps := pilotDeps(nil, nil)
	deps.EbookAnnotations = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/ebooks/book/annotations"
	rec := do(t, h, "GET", path+"?limit=1", "", viewer)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var page Collection[EbookAnnotation]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ETag == "" || service.limit != 2 {
		t.Fatalf("%s limit%d", rec.Body.String(), service.limit)
	}
	cursor := page.Page.NextCursor
	if cursor == "" {
		t.Fatal("missing continuation")
	}
	service.rows = nil
	rec = do(t, h, "GET", path+"?cursor="+cursor, "", viewer)
	if rec.Code != 200 || service.after == nil || service.after.ID != "b" || !service.after.UpdatedAt.Equal(at) {
		t.Fatalf("%d %s after%+v", rec.Code, rec.Body.String(), service.after)
	}
	rec = do(t, h, "GET", Prefix+"/ebooks/other/annotations?cursor="+cursor, "", viewer)
	if rec.Code != 400 {
		t.Fatalf("foreign content cursor: %d %s", rec.Code, rec.Body.String())
	}
	for _, limit := range []string{"0", "51"} {
		rec = do(t, h, "GET", path+"?limit="+limit, "", viewer)
		if rec.Code != 422 {
			t.Fatalf("limit%s: %d", limit, rec.Code)
		}
	}
}
