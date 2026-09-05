package apiv2

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// The home domain: the profile-scoped home page (layout, sections, cards),
// the airing calendar, and the per-profile dismissals that hide a card from
// Continue Watching or Next Up. Home is the same shape as a library page
// (library_views.go) with scope=home, so it reuses those types.

// CalendarInput is the getCalendar query: an inclusive window of the
// viewer's local days, at most 31 days wide.
type CalendarInput struct {
	Start     string `query:"start" required:"true" format:"date" doc:"First local day of the window, YYYY-MM-DD" example:"2026-01-05"`
	End       string `query:"end" required:"true" format:"date" doc:"Last local day of the window, YYYY-MM-DD; at most 30 days after start" example:"2026-01-11"`
	Filter    string `query:"filter" enum:"all,everything,following,favorites,watchlist,popular,trending" doc:"Which items to include; absent is all" example:"all"`
	Timezone  string `query:"timezone" doc:"IANA zone the days are reckoned in; absent is UTC" example:"America/New_York"`
	LibraryID ID     `query:"library_id" doc:"Restrict to one library" example:"1"`
}

// CalendarEvent is one airing or release on the calendar.
type CalendarEvent struct {
	ContentID       string   `json:"content_id" example:"episode:severance-s02e01"`
	Type            string   `json:"type" doc:"movie, episode, or season_premiere" example:"episode"`
	Title           string   `json:"title" doc:"The movie or series title" example:"Severance"`
	EpisodeTitle    *string  `json:"episode_title,omitempty"`
	SeriesID        *string  `json:"series_id,omitempty" example:"series:severance"`
	SeasonNumber    *int     `json:"season_number,omitempty" example:"2"`
	EpisodeNumber   *int     `json:"episode_number,omitempty" example:"1"`
	AirDate         string   `json:"air_date" doc:"The source air date, YYYY-MM-DD" example:"2026-01-09"`
	AirTime         *string  `json:"air_time,omitempty" doc:"Wall-clock time in the network's zone, HH:MM" example:"21:00"`
	AirAt           *Instant `json:"air_at,omitempty" doc:"The airing as an instant, when the network's time and zone are known"`
	AirTimezone     *string  `json:"air_timezone,omitempty" doc:"The network's IANA zone" example:"America/New_York"`
	LocalAirDate    string   `json:"local_air_date" doc:"The air date in the viewer's zone, YYYY-MM-DD; the day this event is listed under" example:"2026-01-10"`
	PosterURL       string   `json:"poster_url,omitempty" example:"https://s3.example.test/poster.jpg"`
	PosterThumbhash string   `json:"poster_thumbhash,omitempty"`
	Watched         bool     `json:"watched" example:"false"`
	Badges          []string `json:"badges" doc:"series_premiere, season_premiere, finale; empty, never null"`
}

// CalendarDay is one local day with its events in airing order.
type CalendarDay struct {
	Date  string          `json:"date" doc:"YYYY-MM-DD in the viewer's zone" example:"2026-01-10"`
	Items []CalendarEvent `json:"items" doc:"Empty, never null"`
}

// Calendar is the window's days, ascending; days without events are absent.
type Calendar struct {
	Events []CalendarDay `json:"events" doc:"Empty, never null"`
}

// CalendarOutput is the getCalendar response.
type CalendarOutput struct {
	Body Calendar
}

// HomeDismissalInput names one dismissal: the home surface and the item.
type HomeDismissalInput struct {
	Surface string `path:"surface" enum:"continue_watching,next_up" doc:"The home surface the item is hidden from" example:"continue_watching"`
	ItemID  string `path:"item_id" minLength:"1" doc:"Content identifier of the card" example:"movie:heat-1995"`
}

// HomeDismissal is what a dismissal is anchored to, by surface.
type HomeDismissal struct {
	SeriesID          string   `json:"series_id,omitempty" doc:"Required for next_up: the series the episode card belongs to" example:"series:severance"`
	ProgressUpdatedAt *Instant `json:"progress_updated_at,omitempty" doc:"Required for continue_watching: the card's progress_updated_at; the dismissal holds until the item is played again"`
}

// HomeDismissalUpsertInput is the dismissHomeItem request.
type HomeDismissalUpsertInput struct {
	HomeDismissalInput
	Body HomeDismissal
}

// homeOperationIDs is every operation the catalog-home section registers.
var homeOperationIDs = []string{"getCalendar", "dismissHomeItem", "undismissHomeItem", "getHomeLayout"}

func registerHome(reg *Registry) {
	viewer := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/calendar", "getCalendar", "home",
		"Upcoming and recent airings and releases in a window of the viewer's local days, grouped by day.")), reg.getCalendar)

	dismiss := humaOp(http.MethodPut, Prefix+"/home/dismissals/{surface}/{item_id}", "dismissHomeItem", "home",
		"Hide a card from Continue Watching or Next Up for the acting profile; repeating it refreshes the dismissal.")
	dismiss.DefaultStatus = http.StatusNoContent
	Register(reg, viewer(dismiss), reg.dismissHomeItem)

	undismiss := humaOp(http.MethodDelete, Prefix+"/home/dismissals/{surface}/{item_id}", "undismissHomeItem", "home",
		"Show a dismissed card again; an item that was not dismissed is left as is.")
	undismiss.DefaultStatus = http.StatusNoContent
	Register(reg, viewer(undismiss), reg.undismissHomeItem)

	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/home/layout", "getHomeLayout", "home",
		"The home page's section layout for the acting profile, without items.")), reg.getHomeLayout)
}

func (reg *Registry) calendar() (CalendarService, *Problem) {
	if reg.deps.Calendar == nil {
		return nil, unavailable("calendar")
	}
	return reg.deps.Calendar, nil
}

func (reg *Registry) homeDismissals() (HomeDismissalService, *Problem) {
	if reg.deps.HomeDismissals == nil {
		return nil, unavailable("home dismissal")
	}
	return reg.deps.HomeDismissals, nil
}

func (reg *Registry) homeSections() (HomeSectionService, *Problem) {
	if reg.deps.HomeSections == nil {
		return nil, unavailable("home sections")
	}
	return reg.deps.HomeSections, nil
}

const (
	locationQueryStart    = "query.start"
	locationQueryEnd      = "query.end"
	locationQueryTimezone = "query.timezone"
	// maxCalendarWindow is v1's limit: end at most 30 days after start, so
	// a window spans at most 31 local days.
	maxCalendarWindow = 30 * 24 * time.Hour
)

func validationProblem(location, code, detail string) *Problem {
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
		WithErrors(ProblemError{Location: location, Code: code, Detail: detail})
}

// calendarQuery turns the validated input into the seam's query. The
// schema has already checked the date format and the filter enum.
func calendarQuery(in *CalendarInput) (handlers.CalendarQuery, *Problem) {
	start, err := time.Parse(time.DateOnly, in.Start)
	if err != nil {
		return handlers.CalendarQuery{}, validationProblem(locationQueryStart, codeInvalid, "expected a calendar date, YYYY-MM-DD")
	}
	end, err := time.Parse(time.DateOnly, in.End)
	if err != nil {
		return handlers.CalendarQuery{}, validationProblem(locationQueryEnd, codeInvalid, "expected a calendar date, YYYY-MM-DD")
	}
	if end.Before(start) {
		return handlers.CalendarQuery{}, validationProblem(locationQueryEnd, codeInvalid, "end must not be before start")
	}
	if end.Sub(start) > maxCalendarWindow {
		return handlers.CalendarQuery{}, validationProblem(locationQueryEnd, codeInvalid, "the window cannot exceed 31 days")
	}
	q := handlers.CalendarQuery{Start: start, End: end, Filter: in.Filter, Location: time.UTC}
	if q.Filter == "" {
		q.Filter = "all"
	}
	if in.Timezone != "" {
		loc, err := time.LoadLocation(in.Timezone)
		if err != nil {
			return handlers.CalendarQuery{}, validationProblem(locationQueryTimezone, codeInvalid, "expected an IANA time zone name")
		}
		q.Location = loc
	}
	if in.LibraryID != "" {
		id, err := intOfID(in.LibraryID)
		if err != nil || id <= 0 {
			return handlers.CalendarQuery{}, validationProblem(locationQueryLibraryID, codeInvalid, detailNotLibraryID)
		}
		q.LibraryID = &id
	}
	return q, nil
}

func calendarEventOf(v handlers.CalendarEventView) CalendarEvent {
	return CalendarEvent{
		ContentID: v.ContentID, Type: v.Type, Title: v.Title, EpisodeTitle: v.EpisodeTitle, SeriesID: v.SeriesID,
		SeasonNumber: v.SeasonNumber, EpisodeNumber: v.EpisodeNumber, AirDate: v.AirDate, AirTime: v.AirTime,
		AirAt: instantOfRFC3339(v.AirAt), AirTimezone: v.AirTimezone, LocalAirDate: v.LocalAirDate,
		PosterURL: v.PosterURL, PosterThumbhash: v.PosterThumbhash, Watched: v.Watched, Badges: NonNil(v.Badges),
	}
}

func (reg *Registry) getCalendar(ctx context.Context, in *CalendarInput) (*CalendarOutput, error) {
	svc, p := reg.calendar()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	q, p := calendarQuery(in)
	if p != nil {
		return nil, p
	}
	view, err := svc.Calendar(ctx, q, handlers.ViewerAccessFilter(ctx, ""))
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := Calendar{Events: make([]CalendarDay, 0, len(view.Events))}
	for _, day := range view.Events {
		items := make([]CalendarEvent, 0, len(day.Items))
		for _, ev := range day.Items {
			items = append(items, calendarEventOf(ev))
		}
		out.Events = append(out.Events, CalendarDay{Date: day.Date, Items: items})
	}
	return &CalendarOutput{Body: out}, nil
}

func (reg *Registry) dismissHomeItem(ctx context.Context, in *HomeDismissalUpsertInput) (*struct{}, error) {
	svc, p := reg.homeDismissals()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	cmd := handlers.HomeDismissalCommand{UserID: userID, ProfileID: profileID, Surface: in.Surface, ItemID: in.ItemID, SeriesID: in.Body.SeriesID}
	if in.Body.ProgressUpdatedAt != nil {
		// The store compares the stamp byte-for-byte with the progress
		// row's RFC 3339 second-precision string, so the instant is
		// rendered the way the store renders it, not as a wire instant.
		cmd.ProgressUpdatedAt = in.Body.ProgressUpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	if err := svc.DismissHomeItem(ctx, cmd); err != nil {
		return nil, libraryProblem(err)
	}
	return nil, nil
}

func (reg *Registry) undismissHomeItem(ctx context.Context, in *HomeDismissalInput) (*struct{}, error) {
	svc, p := reg.homeDismissals()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if err := svc.UndismissHomeItem(ctx, userID, profileID, in.Surface, in.ItemID); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) getHomeLayout(ctx context.Context, _ *struct{}) (*SectionLayoutOutput, error) {
	svc, p := reg.homeSections()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	view, err := svc.HomeLayout(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &SectionLayoutOutput{Body: sectionLayoutOf(view)}, nil
}
