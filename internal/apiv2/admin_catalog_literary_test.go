package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

type fakeAdminLiterary struct {
	calls, limit, userID int
	source, target, work string
	ids                  []string
	err                  error
}

func (f *fakeAdminLiterary) ListCandidates(_ context.Context, id string, limit int) ([]literaryworks.Candidate, error) {
	f.calls++
	f.source, f.limit = id, limit
	return []literaryworks.Candidate{{SourceContentID: id, TargetContentID: "audio-1", Score: 0.9, LinkSource: "metadata_match"}}, f.err
}
func (f *fakeAdminLiterary) LinkItems(_ context.Context, work string, ids []string) (string, error) {
	f.calls++
	f.work, f.ids = work, ids
	return "work-1", f.err
}
func (f *fakeAdminLiterary) UnlinkItem(_ context.Context, work, id string) error {
	f.calls++
	f.work, f.source = work, id
	return f.err
}
func (f *fakeAdminLiterary) ConfirmMatch(_ context.Context, source, target string, userID int) (string, error) {
	f.calls++
	f.source, f.target, f.userID = source, target, userID
	return "work-1", f.err
}
func (f *fakeAdminLiterary) IgnoreMatch(_ context.Context, source, target string, userID int) error {
	f.calls++
	f.source, f.target, f.userID = source, target, userID
	return f.err
}
func literaryAdminHandler(t *testing.T, f *fakeAdminLiterary) http.Handler {
	deps := pilotDeps(nil, nil)
	if f != nil {
		deps.AdminLiteraryWorks = f
	}
	return newTestHandler(t, deps)
}
func TestAdminLiteraryTransport(t *testing.T) {
	f := &fakeAdminLiterary{}
	h := literaryAdminHandler(t, f)
	rec := do(t, h, "GET", Prefix+"/admin/literary-works/items/ebook-1/candidates", "", bearer(adminToken))
	var candidates AdminLiteraryCandidates
	if err := json.Unmarshal(rec.Body.Bytes(), &candidates); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || f.limit != 20 || f.source != "ebook-1" || len(candidates.Candidates) != 1 || candidates.Candidates[0].Evidence == nil {
		t.Fatalf("candidates: %d %s %#v", rec.Code, rec.Body, f)
	}
	rec = do(t, h, "POST", Prefix+"/admin/literary-works/link", `{"work_id":"work-1","content_ids":[" ebook-1 ","audio-1"]}`, bearer(adminToken))
	if rec.Code != 200 || f.work != "work-1" || len(f.ids) != 2 || f.ids[0] != "ebook-1" {
		t.Fatalf("link: %d %s %#v", rec.Code, rec.Body, f)
	}
	for _, action := range []string{"confirm", "ignore"} {
		before := f.calls
		rec = do(t, h, "POST", Prefix+"/admin/literary-works/matches/"+action, `{"source_content_id":"ebook-1","target_content_id":"audio-1"}`, bearer(adminToken))
		if rec.Code != 200 || f.calls != before+1 || f.userID != 2 || f.source != "ebook-1" || f.target != "audio-1" {
			t.Fatalf("%s: %d %s %#v", action, rec.Code, rec.Body, f)
		}
	}
	rec = do(t, h, "DELETE", Prefix+"/admin/literary-works/work-1/items/ebook-1", "", bearer(adminToken))
	if rec.Code != 204 || rec.Body.Len() != 0 || f.work != "work-1" || f.source != "ebook-1" {
		t.Fatalf("unlink: %d %s", rec.Code, rec.Body)
	}
}
func TestAdminLiteraryValidationAndAuthority(t *testing.T) {
	f := &fakeAdminLiterary{}
	h := literaryAdminHandler(t, f)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/items/ebook-1/candidates?limit=101", ""},
		{"GET", "/items/ebook-1/candidates?limit=0", ""},
		{"POST", "/link", `{"content_ids":[]}`},
		{"POST", "/link", `{"content_ids":[null]}`},
		{"POST", "/link", `{"content_ids":[" "]}`},
		{"POST", "/matches/confirm", `{"source_content_id":"ebook-1","target_content_id":" ebook-1 "}`},
		{"POST", "/matches/ignore", `{"source_content_id":null,"target_content_id":"audio-1"}`},
	} {
		rec := do(t, h, tc.method, Prefix+"/admin/literary-works"+tc.path, tc.body, bearer(adminToken))
		if rec.Code != 422 || f.calls != 0 {
			t.Fatalf("invalid %s: %d %s calls=%d", tc.path, rec.Code, rec.Body, f.calls)
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/items/ebook-1/candidates", ""},
		{"POST", "/link", `{"content_ids":["ebook-1"]}`},
		{"POST", "/matches/confirm", `{"source_content_id":"ebook-1","target_content_id":"audio-1"}`},
		{"POST", "/matches/ignore", `{"source_content_id":"ebook-1","target_content_id":"audio-1"}`},
		{"DELETE", "/work-1/items/ebook-1", ""},
	} {
		rec := do(t, h, tc.method, Prefix+"/admin/literary-works"+tc.path, tc.body, nil)
		if rec.Code != 401 || f.calls != 0 {
			t.Fatalf("anonymous %s: %d calls=%d", tc.path, rec.Code, f.calls)
		}
		rec = do(t, h, tc.method, Prefix+"/admin/literary-works"+tc.path, tc.body, bearer(memberToken))
		if rec.Code != 403 || f.calls != 0 {
			t.Fatalf("member %s: %d calls=%d", tc.path, rec.Code, f.calls)
		}
	}
}
func TestAdminLiteraryFailures(t *testing.T) {
	f := &fakeAdminLiterary{err: literaryworks.ErrWorkNotFound}
	h := literaryAdminHandler(t, f)
	path := Prefix + "/admin/literary-works/items/ebook-1/candidates"
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 404 {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body)
	}
	f.err = errors.New("private database details")
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private database") {
		t.Fatalf("failure: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, literaryAdminHandler(t, nil), "GET", path, "", bearer(adminToken))
	if rec.Code != 503 {
		t.Fatalf("unwired: %d %s", rec.Code, rec.Body)
	}
}
