package handlers

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type calendarRepository interface {
	ListEvents(ctx context.Context, f catalog.CalendarFilter) ([]catalog.CalendarEvent, error)
}

// CalendarHandler handles the calendar endpoint.
type CalendarHandler struct {
	repo      calendarRepository
	detailSvc *catalog.DetailService
}

// NewCalendarHandler creates a new CalendarHandler.
func NewCalendarHandler(repo *catalog.CalendarRepository, detailSvc *catalog.DetailService) *CalendarHandler {
	return &CalendarHandler{repo: repo, detailSvc: detailSvc}
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
		filter = "all"
	}
	switch filter {
	case "all", "favorites", "watchlist":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "filter must be all, favorites, or watchlist")
		return
	}

	af := requestAccessFilter(r)

	if (filter == "favorites" || filter == "watchlist") && af.ProfileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "profile required for favorites/watchlist filter")
		return
	}

	viewerLocation := catalog.CalendarLocation(q.Get("timezone"))

	cf := catalog.CalendarFilter{
		Start:              start.AddDate(0, 0, -2),
		End:                end.AddDate(0, 0, 2),
		Filter:             filter,
		AllowedLibraryIDs:  af.AllowedLibraryIDs,
		DisabledLibraryIDs: af.DisabledLibraryIDs,
		MaxContentRating:   af.MaxContentRating,
		UserID:             af.UserID,
		ProfileID:          af.ProfileID,
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

	// Group events by date and build response.
	days := groupEventsByDate(events, r, h.detailSvc, start, end, viewerLocation)
	writeJSON(w, http.StatusOK, calendarResponse{Events: days})
}

func groupEventsByDate(events []catalog.CalendarEvent, r *http.Request, detailSvc *catalog.DetailService, start, end time.Time, viewerLocation *time.Location) []calendarDayResponse {
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
		airAt       *time.Time
		airAtString *string
	}

	prepared := make([]preparedCalendarEvent, 0, len(events))
	for _, ev := range events {
		airAt := catalog.CalendarEventAirAt(ev.AirDate, ev.AirTime, ev.AirTimezone)
		localDateTime := ev.AirDate
		if airAt != nil {
			localDateTime = airAt.In(viewerLocation)
		}
		localDate := localDateTime.Format("2006-01-02")
		if localDate < start.Format("2006-01-02") || localDate > end.Format("2006-01-02") {
			continue
		}
		var airAtString *string
		if airAt != nil {
			formatted := airAt.Format(time.RFC3339)
			airAtString = &formatted
		}
		prepared = append(prepared, preparedCalendarEvent{
			event:       ev,
			localDate:   localDate,
			sourceDate:  ev.AirDate.Format("2006-01-02"),
			airAt:       airAt,
			airAtString: airAtString,
		})
	}
	if len(prepared) == 0 {
		return []calendarDayResponse{}
	}

	sort.SliceStable(prepared, func(i, j int) bool {
		left, right := prepared[i], prepared[j]
		if left.localDate != right.localDate {
			return left.localDate < right.localDate
		}
		if left.airAt != nil && right.airAt != nil && !left.airAt.Equal(*right.airAt) {
			return left.airAt.Before(*right.airAt)
		}
		if (left.airAt != nil) != (right.airAt != nil) {
			return left.airAt != nil
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
