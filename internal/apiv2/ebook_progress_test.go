package apiv2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeEbookProgress struct {
	saved  *handlers.EbookReaderProgress
	filter catalogpkg.AccessFilter
	calls  int
}

func (f *fakeEbookProgress) ReaderProgress(_ context.Context, userID int, profileID, contentID string, filter catalogpkg.AccessFilter) (*handlers.EbookReaderProgress, error) {
	f.calls++
	f.filter = filter
	return f.saved, nil
}
func (f *fakeEbookProgress) SaveReaderProgress(_ context.Context, progress handlers.EbookReaderProgress, filter catalogpkg.AccessFilter) (*handlers.EbookReaderProgress, error) {
	f.calls++
	f.filter = filter
	f.saved = &progress
	return f.saved, nil
}

func TestEbookProgressTransport(t *testing.T) {
	service := &fakeEbookProgress{}
	deps := pilotDeps(nil, nil)
	deps.EbookProgress = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/ebooks/book-one/progress"
	rec := do(t, h, "GET", path, "", viewer)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"progress"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := `{"file_id":"12","location":"epubcfi(/6/2)","progress":0.25,"updated_at":"2026-01-02T03:04:05.000Z"}`
	rec = do(t, h, "PUT", path, body, viewer)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if service.saved.ProfileID != "p-owner" || service.saved.UserID == 0 || service.saved.ContentID != "book-one" || service.saved.FileID != 12 || service.filter.ProfileID != "p-owner" {
		t.Fatalf("identity or file mismatch: %+v %+v", service.saved, service.filter)
	}
	if !service.saved.UpdatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || !strings.Contains(rec.Body.String(), `"file_id":"12"`) {
		t.Fatalf("wire mismatch: %s", rec.Body.String())
	}
	for _, tc := range []struct{ name, body string }{
		{"missing event time", `{"file_id":"12","location":"here","progress":0.25}`},
		{"null event time", `{"file_id":"12","location":"here","progress":0.25,"updated_at":null}`},
		{"numeric ID", strings.Replace(body, `"file_id":"12"`, `"file_id":12`, 1)},
		{"invalid ID", strings.Replace(body, `"file_id":"12"`, `"file_id":"0"`, 1)},
		{"progress range", strings.Replace(body, "0.25", "1.1", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := service.calls
			rec := do(t, h, "PUT", path, tc.body, viewer)
			if rec.Code != 422 || service.calls != before {
				t.Fatalf("%d %s; calls=%d", rec.Code, rec.Body.String(), service.calls)
			}
		})
	}
	before := service.calls
	for _, method := range []string{"GET", "PUT"} {
		rec = do(t, h, method, path, body, bearer(memberToken))
		if rec.Code != 422 || service.calls != before {
			t.Fatalf("profile gate: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func (f *fakeEbookProgress) ReaderCapability(context.Context) handlers.EbookReaderCapability {
	return handlers.EbookReaderCapability{Progress: true}
}
