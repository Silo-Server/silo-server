package introdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
)

func newProvider(t *testing.T, body string) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("")
	c.SetBaseURL(srv.URL)
	return NewProvider(c)
}

func episodeReq(ids map[string]string) markers.Request {
	return markers.Request{
		Kind:          markers.ItemKindEpisode,
		ExternalIDs:   ids,
		SeasonNumber:  1,
		EpisodeNumber: 1,
		Duration:      30 * time.Minute,
	}
}

func TestProviderResolvesTVDBOnly(t *testing.T) {
	p := newProvider(t, `{"type":"episode","intro":[{"end_ms":60000}]}`)
	res, err := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTVDB: "777"}))
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if len(res.Markers) != 1 || res.Markers[0].Kind != markers.MarkerKindIntro {
		t.Fatalf("expected one intro marker, got %+v", res.Markers)
	}
}

func TestProviderUsesRealConfidence(t *testing.T) {
	p := newProvider(t, `{"type":"episode","intro":[{"end_ms":60000,"confidence":0.42}]}`)
	res, _ := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTMDB: "1"}))
	if len(res.Markers) != 1 {
		t.Fatalf("want 1 marker, got %d", len(res.Markers))
	}
	if res.Markers[0].Confidence != 0.42 {
		t.Errorf("confidence = %v, want 0.42 (real value, not hardcoded)", res.Markers[0].Confidence)
	}
}

func TestProviderDefaultsConfidenceWhenAbsent(t *testing.T) {
	p := newProvider(t, `{"type":"episode","intro":[{"end_ms":60000}]}`)
	res, _ := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTMDB: "1"}))
	if len(res.Markers) != 1 {
		t.Fatalf("want 1 marker, got %d", len(res.Markers))
	}
	if res.Markers[0].Confidence != defaultConfidence {
		t.Errorf("confidence = %v, want default %v", res.Markers[0].Confidence, defaultConfidence)
	}
}

func TestProviderPicksMostSubmittedCandidate(t *testing.T) {
	body := `{"type":"episode","intro":[
		{"end_ms":50000,"confidence":0.6,"submission_count":2},
		{"end_ms":61000,"confidence":0.5,"submission_count":9}
	]}`
	p := newProvider(t, body)
	res, _ := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTMDB: "1"}))
	if len(res.Markers) != 1 {
		t.Fatalf("want 1 marker, got %d", len(res.Markers))
	}
	if got := res.Markers[0].End; got != 61*time.Second {
		t.Errorf("picked end = %v, want 61s (the submission_count=9 candidate)", got)
	}
	if res.Markers[0].SubmissionCount != 9 {
		t.Errorf("submission_count = %d, want 9", res.Markers[0].SubmissionCount)
	}
}
