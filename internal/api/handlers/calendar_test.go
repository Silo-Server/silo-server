package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type stubCalendarRepo struct {
	calls  int
	last   catalog.CalendarFilter
	events []catalog.CalendarEvent
	err    error
}

func (r *stubCalendarRepo) ListEvents(_ context.Context, f catalog.CalendarFilter) ([]catalog.CalendarEvent, error) {
	r.calls++
	r.last = f
	return r.events, r.err
}

type stubCalendarImageResolver struct {
	resolveURLCalls  int
	resolveURLsCalls int
	lastPaths        []string
	lastVariant      string
	urls             map[string]string
}

func (r *stubCalendarImageResolver) ResolveImageURL(_ context.Context, path string, _ string) string {
	r.resolveURLCalls++
	return r.urls[path]
}

func (r *stubCalendarImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	r.resolveURLsCalls++
	r.lastPaths = append([]string(nil), paths...)
	r.lastVariant = variant

	result := make(map[string]string, len(paths))
	for _, path := range paths {
		if url, ok := r.urls[path]; ok {
			result[path] = url
		}
	}
	return result
}

func TestHandleGetCalendar_RejectsEndBeforeStart(t *testing.T) {
	t.Parallel()

	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-12&end=2026-04-06", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if repo.calls != 0 {
		t.Fatalf("expected repo not to be called, got %d calls", repo.calls)
	}
}

func TestHandleGetCalendar_RejectsRangesLongerThan31Days(t *testing.T) {
	t.Parallel()

	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-01&end=2026-05-02", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if repo.calls != 0 {
		t.Fatalf("expected repo not to be called, got %d calls", repo.calls)
	}
}

func TestHandleGetCalendar_ReturnsEmptyEvents(t *testing.T) {
	t.Parallel()

	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp calendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 0 {
		t.Fatalf("events len = %d, want 0", len(resp.Events))
	}
}

func TestHandleGetCalendar_ExpandsRepoRangeAndGroupsByViewerLocalDate(t *testing.T) {
	t.Parallel()

	airTime := "00:30"
	airTimezone := "Asia/Tokyo"
	repo := &stubCalendarRepo{
		events: []catalog.CalendarEvent{
			{
				ContentID:   "episode-1",
				Type:        "episode",
				Title:       "Series",
				SeriesID:    ptrString("series-1"),
				AirDate:     time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
				AirTime:     &airTime,
				AirTimezone: &airTimezone,
			},
		},
	}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-01-01&end=2026-01-01&timezone=America/New_York", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := repo.last.Start.Format("2006-01-02"); got != "2025-12-30" {
		t.Fatalf("repo start = %s, want expanded 2025-12-30", got)
	}
	if got := repo.last.End.Format("2006-01-02"); got != "2026-01-03" {
		t.Fatalf("repo end = %s, want expanded 2026-01-03", got)
	}

	var resp calendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("days len = %d, want 1", len(resp.Events))
	}
	if resp.Events[0].Date != "2026-01-01" {
		t.Fatalf("day = %q, want viewer-local 2026-01-01", resp.Events[0].Date)
	}
	item := resp.Events[0].Items[0]
	if item.AirDate != "2026-01-02" {
		t.Fatalf("air_date = %q, want source date 2026-01-02", item.AirDate)
	}
	if item.LocalAirDate != "2026-01-01" {
		t.Fatalf("local_air_date = %q, want 2026-01-01", item.LocalAirDate)
	}
	if item.AirAt == nil || *item.AirAt != "2026-01-01T15:30:00Z" {
		t.Fatalf("air_at = %v, want 2026-01-01T15:30:00Z", item.AirAt)
	}
}

func TestHandleGetCalendar_GroupsEventsAndBatchResolvesCardPosters(t *testing.T) {
	t.Parallel()

	repo := &stubCalendarRepo{
		events: []catalog.CalendarEvent{
			{
				ContentID:       "episode-1",
				Type:            "episode",
				Title:           "Series",
				SeriesID:        ptrString("series-1"),
				SeasonNumber:    ptrInt(1),
				EpisodeNumber:   ptrInt(1),
				AirDate:         time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
				PosterPath:      "images/poster/original.jpg",
				PosterThumbhash: "thumb-a",
				IsPremiere:      true,
			},
			{
				ContentID:       "episode-2",
				Type:            "episode",
				Title:           "Series",
				SeriesID:        ptrString("series-1"),
				SeasonNumber:    ptrInt(1),
				EpisodeNumber:   ptrInt(2),
				AirDate:         time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
				PosterPath:      "images/poster/original.jpg",
				PosterThumbhash: "thumb-a",
				IsFinale:        true,
			},
		},
	}
	resolver := &stubCalendarImageResolver{
		urls: map[string]string{
			"images/poster/w300.jpg": "https://cdn.example/poster-card.jpg",
		},
	}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)

	handler := &CalendarHandler{repo: repo, detailSvc: detailSvc}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if resolver.resolveURLsCalls != 1 {
		t.Fatalf("ResolveImageURLs calls = %d, want 1", resolver.resolveURLsCalls)
	}
	if resolver.resolveURLCalls != 0 {
		t.Fatalf("ResolveImageURL calls = %d, want 0", resolver.resolveURLCalls)
	}
	if len(resolver.lastPaths) != 1 || resolver.lastPaths[0] != "images/poster/w300.jpg" {
		t.Fatalf("lastPaths = %#v, want [images/poster/w300.jpg]", resolver.lastPaths)
	}
	if resolver.lastVariant != "card" {
		t.Fatalf("variant = %q, want card", resolver.lastVariant)
	}

	var resp calendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("days len = %d, want 1", len(resp.Events))
	}
	if got := len(resp.Events[0].Items); got != 2 {
		t.Fatalf("items len = %d, want 2", got)
	}
	for i, item := range resp.Events[0].Items {
		if item.PosterURL != "https://cdn.example/poster-card.jpg" {
			t.Fatalf("item %d poster_url = %q, want resolved card URL", i, item.PosterURL)
		}
	}
}

func ptrString(value string) *string {
	return &value
}

func ptrInt(value int) *int {
	return &value
}
