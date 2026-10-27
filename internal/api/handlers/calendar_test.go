package handlers

import (
	"context"
	"slices"
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

func ptrString(value string) *string {
	return &value
}

func ptrInt(value int) *int {
	return &value
}

type stubCalendarPersonal struct {
	followed       []string
	favorites      []string
	watchlist      []string
	watched        map[string]bool
	lastWatchedIDs []string
}

func (s *stubCalendarPersonal) ListFollowedItemIDs(_ context.Context, _ int, _ string) ([]string, error) {
	return s.followed, nil
}
func (s *stubCalendarPersonal) ListFavoriteItemIDs(_ context.Context, _ int, _ string) ([]string, error) {
	return s.favorites, nil
}
func (s *stubCalendarPersonal) ListWatchlistItemIDs(_ context.Context, _ int, _ string) ([]string, error) {
	return s.watchlist, nil
}
func (s *stubCalendarPersonal) ListWatchedItemIDs(_ context.Context, _ int, _ string, ids []string) (map[string]bool, error) {
	s.lastWatchedIDs = ids
	return s.watched, nil
}

func calendarDay(t *testing.T, value string) time.Time {
	t.Helper()
	day, err := time.Parse(time.DateOnly, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return day
}

func calendarContentIDs(view CalendarView) [][]string {
	days := make([][]string, 0, len(view.Events))
	for _, day := range view.Events {
		ids := make([]string, 0, len(day.Items))
		for _, item := range day.Items {
			ids = append(ids, item.ContentID)
		}
		days = append(days, ids)
	}
	return days
}

// TestCalendarOrdersEventsByViewerLocalTime covers how airings land on the
// viewer's local days and in what order: zoned airings convert to the viewer's
// zone, unzoned ones keep their wall clock, and date-only entries sort last.
func TestCalendarOrdersEventsByViewerLocalTime(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	jan1 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	episode := func(id, title string, airTime, zone *string) catalog.CalendarEvent {
		return catalog.CalendarEvent{ContentID: id, Type: "episode", Title: title, SeriesID: ptrString("series-" + id),
			AirDate: jan1, AirTime: airTime, AirTimezone: zone}
	}
	cases := []struct {
		name   string
		events []catalog.CalendarEvent
		want   []string
	}{
		{
			name: "unzoned airings order by wall clock, not title",
			events: []catalog.CalendarEvent{
				episode("ep-late", "Aaa Show", ptrString("22:00"), nil),
				episode("ep-early", "Zzz Show", ptrString("10:00"), nil),
			},
			want: []string{"ep-early", "ep-late"},
		},
		{
			name: "zoned and unzoned interleave by viewer-local time",
			events: []catalog.CalendarEvent{
				episode("alpha-8pm", "Alpha", ptrString("20:00"), nil),
				// 21:00 London is 16:00 in New York.
				episode("bravo-4pm", "Bravo", ptrString("21:00"), ptrString("Europe/London")),
				episode("charlie-6pm", "Charlie", ptrString("18:00"), nil),
			},
			want: []string{"bravo-4pm", "charlie-6pm", "alpha-8pm"},
		},
		{
			name: "date-only entries sort after timed entries",
			events: []catalog.CalendarEvent{
				{ContentID: "movie-1", Type: "movie", Title: "Aaa Movie", AirDate: jan1},
				episode("ep-noon", "Zzz Show", ptrString("12:00"), nil),
			},
			want: []string{"ep-noon", "movie-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &CalendarHandler{repo: &stubCalendarRepo{events: tc.events}}
			view, err := h.Calendar(context.Background(), CalendarQuery{
				Start: calendarDay(t, "2026-01-01"), End: calendarDay(t, "2026-01-01"), Location: newYork,
			}, catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("Calendar: %v", err)
			}
			got := calendarContentIDs(view)
			if len(got) != 1 || !slices.Equal(got[0], tc.want) {
				t.Fatalf("days = %v, want one day %v", got, tc.want)
			}
		})
	}
}

// An airing whose source date differs from the viewer's local date is found by
// the widened repo window and grouped under the viewer's day.
func TestCalendarGroupsAiringsUnderTheViewerLocalDay(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	repo := &stubCalendarRepo{events: []catalog.CalendarEvent{{
		ContentID: "episode-1", Type: "episode", Title: "Series", SeriesID: ptrString("series-1"),
		AirDate:     time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
		AirTime:     ptrString("00:30"),
		AirTimezone: ptrString("Asia/Tokyo"),
	}}}
	h := &CalendarHandler{repo: repo}

	view, err := h.Calendar(context.Background(), CalendarQuery{
		Start: calendarDay(t, "2026-01-01"), End: calendarDay(t, "2026-01-01"), Location: newYork,
	}, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if got, want := repo.last.Start.Format(time.DateOnly)+".."+repo.last.End.Format(time.DateOnly), "2025-12-30..2026-01-03"; got != want {
		t.Fatalf("repo window = %s, want %s", got, want)
	}
	if len(view.Events) != 1 || view.Events[0].Date != "2026-01-01" || len(view.Events[0].Items) != 1 {
		t.Fatalf("days = %+v, want one viewer-local day 2026-01-01", view.Events)
	}
	item := view.Events[0].Items[0]
	if item.AirDate != "2026-01-02" || item.LocalAirDate != "2026-01-01" {
		t.Fatalf("air_date/local_air_date = %q/%q, want 2026-01-02/2026-01-01", item.AirDate, item.LocalAirDate)
	}
	if item.AirAt == nil || *item.AirAt != "2026-01-01T15:30:00Z" {
		t.Fatalf("air_at = %v, want 2026-01-01T15:30:00Z", item.AirAt)
	}
}

// Shared posters resolve in one batch at the card variant.
func TestCalendarBatchResolvesCardPosters(t *testing.T) {
	apr6 := time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC)
	repo := &stubCalendarRepo{events: []catalog.CalendarEvent{
		{ContentID: "episode-1", Type: "episode", Title: "Series", SeriesID: ptrString("series-1"),
			SeasonNumber: ptrInt(1), EpisodeNumber: ptrInt(1), AirDate: apr6, PosterPath: "images/poster/original.jpg"},
		{ContentID: "episode-2", Type: "episode", Title: "Series", SeriesID: ptrString("series-1"),
			SeasonNumber: ptrInt(1), EpisodeNumber: ptrInt(2), AirDate: apr6, PosterPath: "images/poster/original.jpg"},
	}}
	resolver := &stubCalendarImageResolver{urls: map[string]string{"images/poster/w300.jpg": "https://cdn.example/poster-card.jpg"}}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	h := &CalendarHandler{repo: repo, detailSvc: detailSvc}

	view, err := h.Calendar(context.Background(), CalendarQuery{
		Start: calendarDay(t, "2026-04-06"), End: calendarDay(t, "2026-04-12"), Location: time.UTC,
	}, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if resolver.resolveURLsCalls != 1 || resolver.resolveURLCalls != 0 {
		t.Fatalf("resolver calls batch=%d single=%d, want 1/0", resolver.resolveURLsCalls, resolver.resolveURLCalls)
	}
	if !slices.Equal(resolver.lastPaths, []string{"images/poster/w300.jpg"}) || resolver.lastVariant != "card" {
		t.Fatalf("resolved paths %v variant %q, want [images/poster/w300.jpg] card", resolver.lastPaths, resolver.lastVariant)
	}
	if len(view.Events) != 1 || len(view.Events[0].Items) != 2 {
		t.Fatalf("days = %+v, want one day with two items", view.Events)
	}
	for _, item := range view.Events[0].Items {
		if item.PosterURL != "https://cdn.example/poster-card.jpg" {
			t.Fatalf("%s poster_url = %q, want the card URL", item.ContentID, item.PosterURL)
		}
	}
}

// TestCalendarPresetScopeAndWatchedOverlay covers the per-profile presets: a
// restricting preset passes its ids to the query, an empty one skips the query,
// and the watched overlay marks only the profile's completed items.
func TestCalendarPresetScopeAndWatchedOverlay(t *testing.T) {
	apr8 := time.Date(2026, time.April, 8, 0, 0, 0, 0, time.UTC)
	query := func(filter string) CalendarQuery {
		return CalendarQuery{Start: calendarDay(t, "2026-04-06"), End: calendarDay(t, "2026-04-12"), Filter: filter, Location: time.UTC}
	}

	t.Run("following restricts to followed ids", func(t *testing.T) {
		repo := &stubCalendarRepo{}
		h := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{followed: []string{"s1", "s2"}}}
		if _, err := h.Calendar(context.Background(), query(calendarFilterFollowing), catalog.AccessFilter{}); err != nil {
			t.Fatalf("Calendar: %v", err)
		}
		if !repo.last.RestrictByIDs || !slices.Equal(repo.last.RestrictToIDs, []string{"s1", "s2"}) {
			t.Fatalf("restriction = %v %v, want true [s1 s2]", repo.last.RestrictByIDs, repo.last.RestrictToIDs)
		}
	})

	t.Run("empty followed set skips the query", func(t *testing.T) {
		repo := &stubCalendarRepo{events: []catalog.CalendarEvent{{ContentID: "ep-1", Type: "episode", AirDate: apr8}}}
		h := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{}}
		view, err := h.Calendar(context.Background(), query(calendarFilterFollowing), catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("Calendar: %v", err)
		}
		if repo.calls != 0 || view.Events == nil || len(view.Events) != 0 {
			t.Fatalf("calls = %d events = %#v, want no query and an empty (non-nil) calendar", repo.calls, view.Events)
		}
	})

	t.Run("watched overlay", func(t *testing.T) {
		repo := &stubCalendarRepo{events: []catalog.CalendarEvent{
			{ContentID: "ep-1", Type: "episode", Title: "Show", AirDate: apr8},
			{ContentID: "ep-2", Type: "episode", Title: "Show", AirDate: apr8},
		}}
		h := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{watched: map[string]bool{"ep-1": true}}}
		view, err := h.Calendar(context.Background(), query(calendarFilterEverything), catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("Calendar: %v", err)
		}
		watched := map[string]bool{}
		for _, day := range view.Events {
			for _, item := range day.Items {
				watched[item.ContentID] = item.Watched
			}
		}
		if len(watched) != 2 || !watched["ep-1"] || watched["ep-2"] {
			t.Fatalf("watched = %v, want ep-1 true, ep-2 false", watched)
		}
	})
}
