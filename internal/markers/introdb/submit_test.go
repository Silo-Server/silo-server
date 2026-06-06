package introdb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
)

func ptrDur(d time.Duration) *time.Duration { return &d }

func TestSubmitMarkerSendsExpectedBody(t *testing.T) {
	var gotBody submitRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"submissions":[{"id":"abc","status":"pending","weight":1.5}]}`))
	}))
	defer srv.Close()

	c := NewClient("secret-key")
	c.SetBaseURL(srv.URL)
	p := NewProvider(c)

	res, err := p.SubmitMarker(context.Background(), markers.SubmissionRequest{
		Kind:          markers.ItemKindEpisode,
		ExternalIDs:   map[string]string{markers.ExternalIDKeyTMDB: "1234"},
		SeasonNumber:  1,
		EpisodeNumber: 2,
		Segment:       markers.MarkerKindIntro,
		Start:         ptrDur(0),
		End:           ptrDur(60 * time.Second),
		Duration:      30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("SubmitMarker: %v", err)
	}
	if res.ID != "abc" || res.Status != markers.SubmissionStatusPending {
		t.Errorf("result = %+v, want id abc status pending", res)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth = %q, want Bearer secret-key", gotAuth)
	}
	if gotBody.TmdbID != 1234 || gotBody.Type != "tv" || gotBody.Segment != "intro" {
		t.Errorf("body = %+v, want tmdb 1234 type tv segment intro", gotBody)
	}
	if gotBody.StartMs != nil {
		t.Errorf("intro start_ms = %v, want null (zero start dropped)", *gotBody.StartMs)
	}
	if gotBody.EndMs == nil || *gotBody.EndMs != 60000 {
		t.Errorf("end_ms = %v, want 60000", gotBody.EndMs)
	}
	if gotBody.Season == nil || *gotBody.Season != 1 || gotBody.Episode == nil || *gotBody.Episode != 2 {
		t.Errorf("season/episode = %v/%v, want 1/2", gotBody.Season, gotBody.Episode)
	}
}

func TestSubmitMarkerRequiresTMDB(t *testing.T) {
	p := NewProvider(NewClient("secret-key"))
	_, err := p.SubmitMarker(context.Background(), markers.SubmissionRequest{
		Kind:        markers.ItemKindMovie,
		ExternalIDs: map[string]string{markers.ExternalIDKeyIMDB: "tt1"},
		Segment:     markers.MarkerKindIntro,
		End:         ptrDur(60 * time.Second),
	})
	if err == nil {
		t.Fatal("expected error when TMDB id absent")
	}
}

func TestSubmitMarkerRequiresKey(t *testing.T) {
	p := NewProvider(NewClient("")) // no key
	_, err := p.SubmitMarker(context.Background(), markers.SubmissionRequest{
		Kind:          markers.ItemKindEpisode,
		ExternalIDs:   map[string]string{markers.ExternalIDKeyTMDB: "1"},
		SeasonNumber:  1,
		EpisodeNumber: 1,
		Segment:       markers.MarkerKindIntro,
		End:           ptrDur(60 * time.Second),
	})
	if err == nil {
		t.Fatal("expected error when API key missing")
	}
}

func TestFetchUserStatsParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total":10,"accepted":7,"pending":2,"rejected":1,"acceptance_rate":0.7,"current_streak":3,"best_streak":5}`))
	}))
	defer srv.Close()

	c := NewClient("secret-key")
	c.SetBaseURL(srv.URL)
	p := NewProvider(c)
	stats, err := p.FetchUserStats(context.Background())
	if err != nil {
		t.Fatalf("FetchUserStats: %v", err)
	}
	if stats.Total != 10 || stats.Accepted != 7 || stats.AcceptanceRate != 0.7 || stats.BestStreak != 5 {
		t.Errorf("stats = %+v", stats)
	}
}
