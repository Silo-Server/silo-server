package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type calendarRepository interface {
	ListEvents(ctx context.Context, f catalog.CalendarFilter) ([]catalog.CalendarEvent, error)
}

// calendarPersonalRepo resolves per-profile id-sets and watched status.
type calendarPersonalRepo interface {
	ListFollowedItemIDs(ctx context.Context, userID int, profileID string) ([]string, error)
	ListFavoriteItemIDs(ctx context.Context, userID int, profileID string) ([]string, error)
	ListWatchlistItemIDs(ctx context.Context, userID int, profileID string) ([]string, error)
	ListWatchedItemIDs(ctx context.Context, userID int, profileID string, contentIDs []string) (map[string]bool, error)
}

// calendarPopularSource reads the cached server-wide popular id-set.
type calendarPopularSource interface {
	GetRecommendationCache(ctx context.Context, userID int, profileID, recType, sourceItemID string) ([]recommendations.ScoredItem, error)
}

// calendarTrendingSource reads the external-trending snapshot.
type calendarTrendingSource interface {
	Get(ctx context.Context, source, window string) (sections.TrendingSnapshot, bool, error)
}

const (
	calendarFilterAll        = "all"
	calendarFilterEverything = "everything"
	calendarFilterFollowing  = "following"
	calendarFilterFavorites  = "favorites"
	calendarFilterWatchlist  = "watchlist"
	calendarFilterPopular    = "popular"
	calendarFilterTrending   = "trending"
)

// The Trending preset reads this canonical external-trending snapshot
// (see internal/sections trending snapshots).
const (
	calendarTrendingSnapshotSource = "tmdb"
	calendarTrendingSnapshotWindow = "week"
)

// CalendarHandler handles the calendar endpoint.
type CalendarHandler struct {
	repo      calendarRepository
	detailSvc *catalog.DetailService
	personal  calendarPersonalRepo   // nil-tolerant (per-profile presets degrade to empty)
	popular   calendarPopularSource  // nil when recommendations disabled
	trending  calendarTrendingSource // nil when trending disabled
}

// NewCalendarHandler creates a new CalendarHandler. The repo doubles as the
// per-profile resolver since *catalog.CalendarRepository implements both.
func NewCalendarHandler(repo *catalog.CalendarRepository, detailSvc *catalog.DetailService, popular calendarPopularSource, trending calendarTrendingSource) *CalendarHandler {
	return &CalendarHandler{repo: repo, detailSvc: detailSvc, personal: repo, popular: popular, trending: trending}
}

// --- Response types ---

type calendarEventResponse struct {
	ContentID       string   `json:"content_id"`
	Type            string   `json:"type"`
	Title           string   `json:"title"`
	EpisodeTitle    *string  `json:"episode_title,omitempty"`
	SeriesID        *string  `json:"series_id,omitempty"`
	SeasonNumber    *int     `json:"season_number,omitempty"`
	EpisodeNumber   *int     `json:"episode_number,omitempty"`
	AirDate         string   `json:"air_date"`
	AirTime         *string  `json:"air_time,omitempty"`
	AirAt           *string  `json:"air_at,omitempty"`
	AirTimezone     *string  `json:"air_timezone,omitempty"`
	LocalAirDate    string   `json:"local_air_date"`
	PosterURL       string   `json:"poster_url,omitempty"`
	PosterThumbhash string   `json:"poster_thumbhash,omitempty"`
	Watched         bool     `json:"watched"`
	Badges          []string `json:"badges"`
}

type calendarDayResponse struct {
	Date  string                  `json:"date"`
	Items []calendarEventResponse `json:"items"`
}

type calendarResponse struct {
	Events []calendarDayResponse `json:"events"`
}

// HandleGetCalendar returns calendar events for a date range.
func (h *CalendarHandler) HandleGetCalendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	start, err := time.Parse("2006-01-02", q.Get("start"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "start must be a valid date (YYYY-MM-DD)")
		return
	}
	end, err := time.Parse("2006-01-02", q.Get("end"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "end must be a valid date (YYYY-MM-DD)")
		return
	}
	if end.Before(start) {
		writeError(w, http.StatusBadRequest, "bad_request", "end must not be before start")
		return
	}
	if end.Sub(start) > 30*24*time.Hour {
		writeError(w, http.StatusBadRequest, "bad_request", "date range cannot exceed 31 days")
		return
	}

	filter := q.Get("filter")
	if filter == "" {
		filter = calendarFilterAll
	}
	switch filter {
	case calendarFilterAll, calendarFilterEverything, calendarFilterFollowing,
		calendarFilterFavorites, calendarFilterWatchlist, calendarFilterPopular, calendarFilterTrending:
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "invalid filter")
		return
	}

	af := requestAccessFilter(r)

	viewerLocation := catalog.CalendarLocation(q.Get("timezone"))

	restrict, ids, err := h.resolveCalendarRestriction(r.Context(), filter, af)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to resolve calendar filter")
		return
	}
	// A restricting preset with no ids can never match — skip the windowed query.
	if restrict && len(ids) == 0 {
		writeJSON(w, http.StatusOK, calendarResponse{Events: []calendarDayResponse{}})
		return
	}

	cf := catalog.CalendarFilter{
		Start:              start.AddDate(0, 0, -2),
		End:                end.AddDate(0, 0, 2),
		AllowedLibraryIDs:  af.AllowedLibraryIDs,
		DisabledLibraryIDs: af.DisabledLibraryIDs,
		MaxContentRating:   af.MaxContentRating,
		RestrictByIDs:      restrict,
		RestrictToIDs:      ids,
	}

	if v := q.Get("library_id"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "library_id must be a positive integer")
			return
		}
		cf.LibraryID = &id
	}

	events, err := h.repo.ListEvents(r.Context(), cf)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to fetch calendar events")
		return
	}

	watched := h.resolveWatched(r.Context(), af, events)

	// Group events by date and build response.
	days := groupEventsByDate(events, r, h.detailSvc, start, end, viewerLocation, watched)
	writeJSON(w, http.StatusOK, calendarResponse{Events: days})
}

// resolveCalendarRestriction maps a preset to an id-set restriction. restrict=false
// means no restriction (everything/all). A nil/missing source degrades that preset
// to an empty id-set (which the caller renders as an empty calendar).
func (h *CalendarHandler) resolveCalendarRestriction(ctx context.Context, filter string, af catalog.AccessFilter) (restrict bool, ids []string, err error) {
	switch filter {
	case calendarFilterAll, calendarFilterEverything:
		return false, nil, nil
	case calendarFilterFollowing, calendarFilterFavorites, calendarFilterWatchlist:
		if h.personal == nil {
			return true, nil, nil
		}
		switch filter {
		case calendarFilterFavorites:
			ids, err = h.personal.ListFavoriteItemIDs(ctx, af.UserID, af.ProfileID)
		case calendarFilterWatchlist:
			ids, err = h.personal.ListWatchlistItemIDs(ctx, af.UserID, af.ProfileID)
		default: // following
			ids, err = h.personal.ListFollowedItemIDs(ctx, af.UserID, af.ProfileID)
		}
		return true, ids, err
	case calendarFilterPopular:
		if h.popular == nil {
			return true, nil, nil
		}
		items, err := h.popular.GetRecommendationCache(ctx, recommendations.GlobalCacheUserID, recommendations.GlobalCacheProfileID, recommendations.RecTypePopular, "")
		if err != nil {
			return true, nil, err
		}
		ids = make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.MediaItemID)
		}
		return true, ids, nil
	case calendarFilterTrending:
		if h.trending == nil {
			return true, nil, nil
		}
		snap, ok, err := h.trending.Get(ctx, calendarTrendingSnapshotSource, calendarTrendingSnapshotWindow)
		if err != nil {
			return true, nil, err
		}
		if !ok {
			return true, nil, nil
		}
		return true, snap.ContentIDs, nil
	default:
		return false, nil, nil
	}
}

// resolveWatched decorates events with the profile's completed status. Best-effort:
// a lookup failure logs and returns no watched marks rather than failing the page.
func (h *CalendarHandler) resolveWatched(ctx context.Context, af catalog.AccessFilter, events []catalog.CalendarEvent) map[string]bool {
	if h.personal == nil || len(events) == 0 {
		return map[string]bool{}
	}
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ContentID)
	}
	watched, err := h.personal.ListWatchedItemIDs(ctx, af.UserID, af.ProfileID, ids)
	if err != nil {
		slog.WarnContext(ctx, "calendar watched overlay failed", "error", err)
		return map[string]bool{}
	}
	return watched
}

func groupEventsByDate(events []catalog.CalendarEvent, r *http.Request, detailSvc *catalog.DetailService, start, end time.Time, viewerLocation *time.Location, watched map[string]bool) []calendarDayResponse {
	if len(events) == 0 {
		return []calendarDayResponse{}
	}

	posterURLs := map[string]string{}
	if detailSvc != nil {
		posterPaths := make([]string, 0, len(events))
		seenPosterPaths := make(map[string]struct{}, len(events))
		for _, ev := range events {
			if ev.PosterPath == "" {
				continue
			}
			if _, ok := seenPosterPaths[ev.PosterPath]; ok {
				continue
			}
			seenPosterPaths[ev.PosterPath] = struct{}{}
			posterPaths = append(posterPaths, ev.PosterPath)
		}
		posterURLs = detailSvc.PresignImageURLs(r.Context(), posterPaths, "poster", "small")
	}

	type preparedCalendarEvent struct {
		event       catalog.CalendarEvent
		localDate   string
		sourceDate  string
		localTime   time.Time
		hasTime     bool
		airAtString *string
	}

	startDate := start.Format("2006-01-02")
	endDate := end.Format("2006-01-02")

	prepared := make([]preparedCalendarEvent, 0, len(events))
	for _, ev := range events {
		localTime, hasTime := catalog.CalendarEventLocalTime(ev.AirDate, ev.AirTime, ev.AirTimezone, viewerLocation)
		localDate := localTime.Format("2006-01-02")
		if localDate < startDate || localDate > endDate {
			continue
		}
		var airAtString *string
		if airAt := catalog.CalendarEventAirAt(ev.AirDate, ev.AirTime, ev.AirTimezone); airAt != nil {
			formatted := airAt.Format(time.RFC3339)
			airAtString = &formatted
		}
		prepared = append(prepared, preparedCalendarEvent{
			event:       ev,
			localDate:   localDate,
			sourceDate:  ev.AirDate.Format("2006-01-02"),
			localTime:   localTime,
			hasTime:     hasTime,
			airAtString: airAtString,
		})
	}
	if len(prepared) == 0 {
		return []calendarDayResponse{}
	}

	// Order each local day by the wall-clock time the viewer actually sees,
	// then place date-only entries (no air_time) after timed entries.
	sort.SliceStable(prepared, func(i, j int) bool {
		left, right := prepared[i], prepared[j]
		if left.localDate != right.localDate {
			return left.localDate < right.localDate
		}
		if left.hasTime != right.hasTime {
			return left.hasTime
		}
		if left.hasTime && !left.localTime.Equal(right.localTime) {
			return left.localTime.Before(right.localTime)
		}
		if left.event.Title != right.event.Title {
			return left.event.Title < right.event.Title
		}
		if left.event.SeasonNumber != nil && right.event.SeasonNumber != nil && *left.event.SeasonNumber != *right.event.SeasonNumber {
			return *left.event.SeasonNumber < *right.event.SeasonNumber
		}
		if left.event.EpisodeNumber != nil && right.event.EpisodeNumber != nil && *left.event.EpisodeNumber != *right.event.EpisodeNumber {
			return *left.event.EpisodeNumber < *right.event.EpisodeNumber
		}
		return left.event.ContentID < right.event.ContentID
	})

	var days []calendarDayResponse
	var currentDay *calendarDayResponse

	for _, item := range prepared {
		ev := item.event
		if currentDay == nil || currentDay.Date != item.localDate {
			if currentDay != nil {
				days = append(days, *currentDay)
			}
			currentDay = &calendarDayResponse{Date: item.localDate}
		}

		badges := buildBadges(ev)

		currentDay.Items = append(currentDay.Items, calendarEventResponse{
			ContentID:       ev.ContentID,
			Type:            ev.Type,
			Title:           ev.Title,
			EpisodeTitle:    ev.EpisodeTitle,
			SeriesID:        ev.SeriesID,
			SeasonNumber:    ev.SeasonNumber,
			EpisodeNumber:   ev.EpisodeNumber,
			AirDate:         item.sourceDate,
			AirTime:         ev.AirTime,
			AirAt:           item.airAtString,
			AirTimezone:     ev.AirTimezone,
			LocalAirDate:    item.localDate,
			PosterURL:       posterURLs[ev.PosterPath],
			PosterThumbhash: ev.PosterThumbhash,
			Watched:         watched[ev.ContentID],
			Badges:          badges,
		})
	}

	if currentDay != nil {
		days = append(days, *currentDay)
	}

	return days
}

func buildBadges(ev catalog.CalendarEvent) []string {
	var badges []string
	if ev.IsPremiere {
		if ev.SeasonNumber != nil && *ev.SeasonNumber == 1 {
			badges = append(badges, "series_premiere")
		} else {
			badges = append(badges, "season_premiere")
		}
	}
	if ev.IsFinale {
		badges = append(badges, "finale")
	}
	if badges == nil {
		badges = []string{}
	}
	return badges
}
