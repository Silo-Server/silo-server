package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
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

// recordingCalendarArtworkResolver is target-aware, so the calendar handler
// signs real capabilities through it and each resolved URL names the surface
// and keys the handler chose for that event.
type recordingCalendarArtworkResolver struct {
	batchCalls int
	targets    []artworkurl.Target
	variant    string
}

func (*recordingCalendarArtworkResolver) ResolveImageURL(context.Context, string, string) string {
	return ""
}

func (*recordingCalendarArtworkResolver) ResolveImageURLs(context.Context, []string, string) map[string]string {
	return nil
}

func (r *recordingCalendarArtworkResolver) ResolveArtworkTargetsWithExpiry(
	_ context.Context,
	targets []artworkurl.Target,
	variant string,
) map[string]catalog.ResolvedImageURL {
	r.batchCalls++
	r.variant = variant
	r.targets = append(r.targets, targets...)
	resolved := make(map[string]catalog.ResolvedImageURL, len(targets))
	for _, target := range targets {
		resolved[target.CacheKey()] = catalog.ResolvedImageURL{
			URL: "resolved:" + target.Surface + ":" + strings.Join(target.Keys, ","),
		}
	}
	return resolved
}

func (r *recordingCalendarArtworkResolver) ResolveArtworkTargetRequestsWithExpiry(
	ctx context.Context,
	requests []artworkurl.TargetRequest,
) map[string]catalog.ResolvedImageURL {
	targets := make([]artworkurl.Target, 0, len(requests))
	variant := ""
	for _, request := range requests {
		targets = append(targets, request.Target)
		variant = request.Variant
	}
	resolved := r.ResolveArtworkTargetsWithExpiry(ctx, targets, variant)
	result := make(map[string]catalog.ResolvedImageURL, len(requests))
	for _, request := range requests {
		result[request.CacheKey()] = resolved[request.Target.CacheKey()]
	}
	return result
}

func calendarItemsByContentID(t *testing.T, rec *httptest.ResponseRecorder) map[string]calendarEventResponse {
	t.Helper()
	var resp calendarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items := map[string]calendarEventResponse{}
	for _, day := range resp.Events {
		for _, item := range day.Items {
			items[item.ContentID] = item
		}
	}
	return items
}

// A season premiere whose season carries its own poster must be signed against
// the season surface (keyed by seasons.content_id). Signing the parent series
// there would make delivery reload the series row and serve a poster that
// contradicts the season thumbhash shipped in the same response.
func TestHandleGetCalendar_MintsSeasonPosterTargetWhenSeasonPosterSelected(t *testing.T) {
	t.Parallel()

	airDate := time.Date(2026, time.April, 8, 0, 0, 0, 0, time.UTC)
	repo := &stubCalendarRepo{events: []catalog.CalendarEvent{
		{
			ContentID:      "season-1",
			Type:           "season_premiere",
			Title:          "Show One",
			SeriesID:       ptrString("series-1"),
			SeasonNumber:   ptrInt(2),
			AirDate:        airDate,
			PosterPath:     "images/season-1/original.jpg",
			PosterIsSeason: true,
			IsPremiere:     true,
		},
		{
			ContentID:    "season-2",
			Type:         "season_premiere",
			Title:        "Show Two",
			SeriesID:     ptrString("series-2"),
			SeasonNumber: ptrInt(3),
			AirDate:      airDate,
			// No season poster: the repo COALESCEd down to the series poster.
			PosterPath: "images/series-2/original.jpg",
			IsPremiere: true,
		},
		{
			ContentID:     "episode-1",
			Type:          "episode",
			Title:         "Show Three",
			SeriesID:      ptrString("series-3"),
			SeasonNumber:  ptrInt(1),
			EpisodeNumber: ptrInt(4),
			AirDate:       airDate,
			PosterPath:    "images/series-3/original.jpg",
		},
	}}
	resolver := &recordingCalendarArtworkResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)

	handler := &CalendarHandler{repo: repo, detailSvc: detailSvc}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if resolver.batchCalls != 1 {
		t.Fatalf("artwork batch calls = %d, want 1", resolver.batchCalls)
	}

	surfaces := map[string]string{}
	for _, target := range resolver.targets {
		surfaces[target.Surface+":"+strings.Join(target.Keys, ",")] = target.Slot
	}
	for _, want := range []string{
		artworkurl.SurfaceSeasonPosters + ":season-1",
		artworkurl.SurfaceItemPosters + ":series-2",
		artworkurl.SurfaceItemPosters + ":series-3",
	} {
		slot, ok := surfaces[want]
		if !ok {
			t.Fatalf("missing artwork target %q, got %v", want, surfaces)
		}
		if slot != artworkImagePoster {
			t.Fatalf("target %q slot = %q, want %q", want, slot, artworkImagePoster)
		}
	}

	items := calendarItemsByContentID(t, rec)
	for contentID, wantURL := range map[string]string{
		"season-1":  "resolved:" + artworkurl.SurfaceSeasonPosters + ":season-1",
		"season-2":  "resolved:" + artworkurl.SurfaceItemPosters + ":series-2",
		"episode-1": "resolved:" + artworkurl.SurfaceItemPosters + ":series-3",
	} {
		if got := items[contentID].PosterURL; got != wantURL {
			t.Fatalf("%s poster_url = %q, want %q", contentID, got, wantURL)
		}
	}
}

// Two owners can select the same stored revision. Indexing the resolved batch
// by poster path collapsed them onto whichever capability was signed last, so
// the response has to look each event's URL up by its own target cache key.
func TestHandleGetCalendar_KeysResolvedPostersByTargetNotPath(t *testing.T) {
	t.Parallel()

	airDate := time.Date(2026, time.April, 8, 0, 0, 0, 0, time.UTC)
	sharedPoster := "images/shared/original.jpg"
	repo := &stubCalendarRepo{events: []catalog.CalendarEvent{
		{
			ContentID:      "season-1",
			Type:           "season_premiere",
			Title:          "Show One",
			SeriesID:       ptrString("series-1"),
			SeasonNumber:   ptrInt(2),
			AirDate:        airDate,
			PosterPath:     sharedPoster,
			PosterIsSeason: true,
			IsPremiere:     true,
		},
		{
			ContentID:     "episode-1",
			Type:          "episode",
			Title:         "Show Two",
			SeriesID:      ptrString("series-2"),
			SeasonNumber:  ptrInt(1),
			EpisodeNumber: ptrInt(1),
			AirDate:       airDate,
			PosterPath:    sharedPoster,
		},
	}}
	resolver := &recordingCalendarArtworkResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)

	handler := &CalendarHandler{repo: repo, detailSvc: detailSvc}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	items := calendarItemsByContentID(t, rec)
	seasonURL := "resolved:" + artworkurl.SurfaceSeasonPosters + ":season-1"
	seriesURL := "resolved:" + artworkurl.SurfaceItemPosters + ":series-2"
	if got := items["season-1"].PosterURL; got != seasonURL {
		t.Fatalf("season-1 poster_url = %q, want %q", got, seasonURL)
	}
	if got := items["episode-1"].PosterURL; got != seriesURL {
		t.Fatalf("episode-1 poster_url = %q, want %q", got, seriesURL)
	}
}

func TestHandleGetCalendar_OrdersEventsWithoutTimezoneByWallClock(t *testing.T) {
	t.Parallel()

	late := "22:00"
	early := "10:00"
	repo := &stubCalendarRepo{
		events: []catalog.CalendarEvent{
			{
				ContentID: "ep-late",
				Type:      "episode",
				Title:     "Aaa Show", // alphabetically first, but airs late
				SeriesID:  ptrString("series-late"),
				AirDate:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
				AirTime:   &late,
				// no AirTimezone -> air_at is nil
			},
			{
				ContentID: "ep-early",
				Type:      "episode",
				Title:     "Zzz Show", // alphabetically last, but airs early
				SeriesID:  ptrString("series-early"),
				AirDate:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
				AirTime:   &early,
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

	var resp calendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("days len = %d, want 1", len(resp.Events))
	}
	items := resp.Events[0].Items
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if items[0].ContentID != "ep-early" || items[1].ContentID != "ep-late" {
		t.Fatalf("order = [%s, %s], want [ep-early, ep-late] (ascending by air_time)",
			items[0].ContentID, items[1].ContentID)
	}
}

func TestHandleGetCalendar_InterleavesZonedAndUnzonedByViewerLocalTime(t *testing.T) {
	t.Parallel()

	eightPM := "20:00"
	sixPM := "18:00"
	londonNine := "21:00"
	london := "Europe/London"
	repo := &stubCalendarRepo{
		events: []catalog.CalendarEvent{
			{
				ContentID: "alpha-8pm",
				Type:      "episode",
				Title:     "Alpha", // would sort first alphabetically
				SeriesID:  ptrString("series-alpha"),
				AirDate:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
				AirTime:   &eightPM, // displays 8:00 PM ET
			},
			{
				ContentID:   "bravo-4pm",
				Type:        "episode",
				Title:       "Bravo",
				SeriesID:    ptrString("series-bravo"),
				AirDate:     time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
				AirTime:     &londonNine, // 21:00 GMT -> 16:00 ET (4:00 PM)
				AirTimezone: &london,
			},
			{
				ContentID: "charlie-6pm",
				Type:      "episode",
				Title:     "Charlie",
				SeriesID:  ptrString("series-charlie"),
				AirDate:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
				AirTime:   &sixPM, // displays 6:00 PM ET
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

	var resp calendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("days len = %d, want 1", len(resp.Events))
	}
	got := make([]string, 0, len(resp.Events[0].Items))
	for _, item := range resp.Events[0].Items {
		got = append(got, item.ContentID)
	}
	want := []string{"bravo-4pm", "charlie-6pm", "alpha-8pm"}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (ascending by viewer-local time)", got, want)
		}
	}
}

func TestHandleGetCalendar_DateOnlyEntriesSortAfterTimedEntries(t *testing.T) {
	t.Parallel()

	noon := "12:00"
	repo := &stubCalendarRepo{
		events: []catalog.CalendarEvent{
			{
				ContentID: "movie-1",
				Type:      "movie",
				Title:     "Aaa Movie", // alphabetically first, but has no air_time
				AirDate:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			},
			{
				ContentID: "ep-noon",
				Type:      "episode",
				Title:     "Zzz Show",
				SeriesID:  ptrString("series-z"),
				AirDate:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
				AirTime:   &noon,
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

	var resp calendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("days len = %d, want 1", len(resp.Events))
	}
	items := resp.Events[0].Items
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if items[0].ContentID != "ep-noon" || items[1].ContentID != "movie-1" {
		t.Fatalf("order = [%s, %s], want [ep-noon, movie-1] (timed before date-only)",
			items[0].ContentID, items[1].ContentID)
	}
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

type stubPopularSource struct{ items []recommendations.ScoredItem }

func (s *stubPopularSource) GetRecommendationCache(_ context.Context, _ int, _, _, _ string) ([]recommendations.ScoredItem, error) {
	return s.items, nil
}

type stubTrendingSource struct {
	snap sections.TrendingSnapshot
	ok   bool
}

func (s *stubTrendingSource) Get(_ context.Context, _, _ string) (sections.TrendingSnapshot, bool, error) {
	return s.snap, s.ok, nil
}

func TestHandleGetCalendar_FollowingPassesFollowedIDs(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{followed: []string{"s1", "s2"}}}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=following", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !repo.last.RestrictByIDs {
		t.Fatalf("expected RestrictByIDs true")
	}
	if got := repo.last.RestrictToIDs; len(got) != 2 || got[0] != "s1" || got[1] != "s2" {
		t.Fatalf("RestrictToIDs = %v, want [s1 s2]", got)
	}
}

func TestHandleGetCalendar_PopularReadsCache(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{
		repo:    repo,
		popular: &stubPopularSource{items: []recommendations.ScoredItem{{MediaItemID: "p1"}, {MediaItemID: "p2"}}},
	}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=popular", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if !repo.last.RestrictByIDs || len(repo.last.RestrictToIDs) != 2 || repo.last.RestrictToIDs[0] != "p1" {
		t.Fatalf("RestrictToIDs = %v, want [p1 p2]", repo.last.RestrictToIDs)
	}
}

func TestHandleGetCalendar_TrendingReadsSnapshot(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{
		repo:     repo,
		trending: &stubTrendingSource{ok: true, snap: sections.TrendingSnapshot{ContentIDs: []string{"t1"}}},
	}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=trending", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if !repo.last.RestrictByIDs || len(repo.last.RestrictToIDs) != 1 || repo.last.RestrictToIDs[0] != "t1" {
		t.Fatalf("RestrictToIDs = %v, want [t1]", repo.last.RestrictToIDs)
	}
}

func TestHandleGetCalendar_EverythingHasNoRestriction(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=everything", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if repo.last.RestrictByIDs {
		t.Fatalf("expected no restriction for everything")
	}
}

func TestHandleGetCalendar_RejectsUnknownFilter(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=bogus", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if repo.calls != 0 {
		t.Fatalf("expected repo not called, got %d", repo.calls)
	}
}

func TestHandleGetCalendar_EmptyFollowedShortCircuits(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{followed: nil}}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=following", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if repo.calls != 0 {
		t.Fatalf("expected ListEvents skipped for empty followed set, got %d calls", repo.calls)
	}
}

func TestHandleGetCalendar_MarksWatchedItems(t *testing.T) {
	repo := &stubCalendarRepo{events: []catalog.CalendarEvent{
		{ContentID: "ep-1", Type: "episode", Title: "Show", AirDate: time.Date(2026, time.April, 8, 0, 0, 0, 0, time.UTC)},
		{ContentID: "ep-2", Type: "episode", Title: "Show", AirDate: time.Date(2026, time.April, 8, 0, 0, 0, 0, time.UTC)},
	}}
	handler := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{watched: map[string]bool{"ep-1": true}}}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=everything", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	var resp calendarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	watched := map[string]bool{}
	for _, day := range resp.Events {
		for _, item := range day.Items {
			watched[item.ContentID] = item.Watched
		}
	}
	if !watched["ep-1"] || watched["ep-2"] {
		t.Fatalf("watched flags = %v, want ep-1 true / ep-2 false", watched)
	}
}
