package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/recommendations"
)

type fakeAdminRecommendations struct {
	calls int
	last  recommendations.JobName
	err   error
}

func (f *fakeAdminRecommendations) StatusCounts(context.Context) (int, int, int, int, int, error) {
	return 3, 10, 4, 5, 6, f.err
}
func (*fakeAdminRecommendations) IsRunning(job recommendations.JobName) bool {
	return job == recommendations.JobEmbeddings
}
func (f *fakeAdminRecommendations) start(job recommendations.JobName) error {
	f.calls++
	f.last = job
	return f.err
}
func (f *fakeAdminRecommendations) TriggerEmbeddings() error {
	return f.start(recommendations.JobEmbeddings)
}
func (f *fakeAdminRecommendations) TriggerTasteProfiles() error {
	return f.start(recommendations.JobTasteProfiles)
}
func (f *fakeAdminRecommendations) TriggerCowatch() error { return f.start(recommendations.JobCowatch) }
func (f *fakeAdminRecommendations) TriggerRecommendations() error {
	return f.start(recommendations.JobRecommendations)
}

func TestAdminRecommendationsTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminRecommendations{}
	deps.AdminRecommendations = f
	h := newTestHandler(t, deps)
	rec := do(t, h, "GET", Prefix+"/admin/recommendations/status", "", bearer(adminToken))
	var status AdminRecommendationsStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || !status.Embeddings.Running || status.Embeddings.Count != 3 || status.Embeddings.Total == nil || *status.Embeddings.Total != 10 || status.TasteProfiles.Count != 4 || status.Recommendations.Count != 5 || status.Cowatch.Count != 6 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct {
		path string
		job  recommendations.JobName
	}{
		{"embeddings", recommendations.JobEmbeddings}, {"taste-profiles", recommendations.JobTasteProfiles}, {"cowatch", recommendations.JobCowatch}, {"recommendations", recommendations.JobRecommendations},
	} {
		path := Prefix + "/admin/recommendations/trigger/" + tc.path
		before := f.calls
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != 200 || f.calls != before+1 || f.last != tc.job || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), `"status":"started"`) {
			t.Fatalf("start %s: %d %s %#v", tc.path, rec.Code, rec.Body, f)
		}
		f.err = errors.New("private worker detail")
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != 409 || strings.Contains(rec.Body.String(), "private worker") {
			t.Fatalf("conflict %s: %d %s", tc.path, rec.Code, rec.Body)
		}
		before = f.calls
		rec = do(t, h, "POST", path, "", bearer(memberToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("member triggered work: %d calls=%d", rec.Code, f.calls)
		}
		f.err = nil
	}
	f.err = errors.New("private database details")
	rec = do(t, h, "GET", Prefix+"/admin/recommendations/status", "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private database") {
		t.Fatalf("status failure: %d %s", rec.Code, rec.Body)
	}
	deps.AdminRecommendations = nil
	h = newTestHandler(t, deps)
	for _, tc := range []struct{ method, path string }{{"GET", "/status"}, {"POST", "/trigger/embeddings"}, {"POST", "/trigger/taste-profiles"}, {"POST", "/trigger/cowatch"}, {"POST", "/trigger/recommendations"}} {
		rec = do(t, h, tc.method, Prefix+"/admin/recommendations"+tc.path, "", bearer(adminToken))
		if rec.Code != 503 {
			t.Fatalf("unwired %s: %d %s", tc.path, rec.Code, rec.Body)
		}
	}
}
