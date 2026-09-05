package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
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

// HomeSectionsInput is the listHomeSections query.
type HomeSectionsInput struct {
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
}

// HomeSectionItemsInput is the getHomeSectionItems query.
type HomeSectionItemsInput struct {
	ID        string `path:"id" minLength:"1" doc:"Section identifier from the home layout" example:"continue_watching"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
}

// SectionConfig is a recipe's configuration document. Its keys are fixed by
// the recipe named in section_type, not by this contract.
type SectionConfig map[string]any

// Schema declares the config as a named extension bag.
func (SectionConfig) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:                 "object",
		Description:          "A recipe's configuration document; its keys are fixed by the recipe named in section_type.",
		AdditionalProperties: true,
		Extensions:           map[string]any{extExtensionBag: "section-config"},
	}
}

// RecipePreset is one gallery preset: a recipe plus the config it starts from.
type RecipePreset struct {
	Key              string        `json:"key" example:"new_this_week"`
	DisplayName      string        `json:"display_name" example:"New this week"`
	Icon             string        `json:"icon" example:"sparkles"`
	DescriptionShort string        `json:"description_short" example:"Items added in the last seven days"`
	DescriptionLong  string        `json:"description_long" doc:"Empty when the preset has none"`
	DefaultParams    SectionConfig `json:"default_params" doc:"The config the preset starts from; empty, never null"`
}

// RecipeDefinition is one recipe as the gallery shows it.
type RecipeDefinition struct {
	Type             string         `json:"type" example:"recently_added"`
	Category         string         `json:"category" example:"library_staples"`
	Presets          []RecipePreset `json:"presets" doc:"Empty, never null"`
	AvoidDuplicates  bool           `json:"avoid_duplicates" example:"true"`
	SupportsRotation bool           `json:"supports_rotation" example:"false"`
	AdminOnly        bool           `json:"admin_only" example:"false"`
}

// RecipeCategory is one gallery category with its recipes.
type RecipeCategory struct {
	Category string             `json:"category" example:"library_staples"`
	Recipes  []RecipeDefinition `json:"recipes" doc:"Empty, never null"`
}

// RecipeCatalog is the listSectionRecipes response: the visible recipes
// grouped by category, categories in key order.
type RecipeCatalog struct {
	Categories []RecipeCategory `json:"categories" doc:"Empty, never null"`
}

// RecipeCatalogOutput is the listSectionRecipes response.
type RecipeCatalogOutput struct {
	Body RecipeCatalog
}

// RecipeCandidatesInput names the recipe type whose candidates are listed.
type RecipeCandidatesInput struct {
	Type string `path:"type" minLength:"1" doc:"Recipe type from the catalog" example:"custom_filter"`
}

// RecipeCandidate is one UI-pickable value for a parameterized recipe.
type RecipeCandidate struct {
	Value       string `json:"value" example:"action"`
	DisplayName string `json:"display_name" example:"Action"`
	Subtitle    string `json:"subtitle" doc:"Empty when the candidate has none"`
}

// RecipeCandidateCollection is the listSectionRecipeCandidates response, a
// bounded list.
type RecipeCandidateCollection struct {
	Candidates []RecipeCandidate `json:"candidates" doc:"Empty, never null"`
}

// RecipeCandidateCollectionOutput is the listSectionRecipeCandidates response.
type RecipeCandidateCollectionOutput struct {
	Body RecipeCandidateCollection
}

// The section's operation ids, shared with the fixture cases.
const (
	opGetCalendar                 = "getCalendar"
	opDismissHomeItem             = "dismissHomeItem"
	opUndismissHomeItem           = "undismissHomeItem"
	opGetHomeLayout               = "getHomeLayout"
	opListHomeSections            = "listHomeSections"
	opGetHomeSectionItems         = "getHomeSectionItems"
	opListSectionRecipes          = "listSectionRecipes"
	opListSectionRecipeCandidates = "listSectionRecipeCandidates"
)

// homeOperationIDs is every operation the catalog-home section registers.
var homeOperationIDs = []string{opGetCalendar, opDismissHomeItem, opUndismissHomeItem, opGetHomeLayout, opListHomeSections, opGetHomeSectionItems, opListSectionRecipes, opListSectionRecipeCandidates}

func registerHome(reg *Registry) {
	viewer := func(op huma.Operation) Operation {
		return Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}
	}
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/calendar", opGetCalendar, "home",
		"Upcoming and recent airings and releases in a window of the viewer's local days, grouped by day.")), reg.getCalendar)

	dismiss := humaOp(http.MethodPut, Prefix+"/home/dismissals/{surface}/{item_id}", opDismissHomeItem, "home",
		"Hide a card from Continue Watching or Next Up for the acting profile; repeating it refreshes the dismissal.")
	dismiss.DefaultStatus = http.StatusNoContent
	Register(reg, viewer(dismiss), reg.dismissHomeItem)

	undismiss := humaOp(http.MethodDelete, Prefix+"/home/dismissals/{surface}/{item_id}", opUndismissHomeItem, "home",
		"Show a dismissed card again; an item that was not dismissed is left as is.")
	undismiss.DefaultStatus = http.StatusNoContent
	Register(reg, viewer(undismiss), reg.undismissHomeItem)

	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/home/layout", opGetHomeLayout, "home",
		"The home page's section layout for the acting profile, without items.")), reg.getHomeLayout)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/home/sections", opListHomeSections, "home",
		"The home page's sections with their cards, as the acting profile sees them.")), reg.listHomeSections)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/home/sections/{id}/items", opGetHomeSectionItems, "home",
		"One section of the home page with its cards.")), reg.getHomeSectionItems)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/sections/recipes", opListSectionRecipes, "home",
		"The section recipe gallery: every visible recipe with its presets, grouped by category.")), reg.listSectionRecipes)
	Register(reg, viewer(humaOp(http.MethodGet, Prefix+"/sections/recipes/{type}/candidates", opListSectionRecipeCandidates, "home",
		"The values a parameterized recipe offers for its parameter.")), reg.listSectionRecipeCandidates)
}

func (reg *Registry) recipes() (RecipeService, *Problem) {
	if reg.deps.Recipes == nil {
		return nil, unavailable("section recipe")
	}
	return reg.deps.Recipes, nil
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

func (reg *Registry) listHomeSections(ctx context.Context, in *HomeSectionsInput) (*SectionCollectionOutput, error) {
	svc, p := reg.homeSections()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	view, err := svc.HomeSections(ctx, sectionViewer(ctx, in.ImageSize))
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := SectionCollection{Sections: make([]Section, 0, len(view.Sections))}
	for _, s := range view.Sections {
		out.Sections = append(out.Sections, sectionOf(s))
	}
	return &SectionCollectionOutput{Body: out}, nil
}

func (reg *Registry) getHomeSectionItems(ctx context.Context, in *HomeSectionItemsInput) (*SectionOutput, error) {
	svc, p := reg.homeSections()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	view, err := svc.HomeSectionItems(ctx, in.ID, sectionViewer(ctx, in.ImageSize))
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &SectionOutput{Body: sectionOf(view)}, nil
}

// sectionConfigOf decodes a recipe's raw config document; anything that is
// not a JSON object (including null and empty) is the empty bag.
func sectionConfigOf(raw json.RawMessage) SectionConfig {
	out := SectionConfig{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = SectionConfig{}
	}
	return out
}

func recipeDefinitionOf(def recipes.RecipeDefinition) RecipeDefinition {
	presets := make([]RecipePreset, 0, len(def.Presets))
	for _, p := range def.Presets {
		presets = append(presets, RecipePreset{Key: p.Key, DisplayName: p.DisplayName, Icon: p.Icon, DescriptionShort: p.DescriptionShort, DescriptionLong: p.DescriptionLong, DefaultParams: sectionConfigOf(p.DefaultParams)})
	}
	return RecipeDefinition{Type: def.Type, Category: string(def.Category), Presets: presets, AvoidDuplicates: def.AvoidDuplicates, SupportsRotation: def.SupportsRotation, AdminOnly: def.AdminOnly}
}

func (reg *Registry) listSectionRecipes(ctx context.Context, _ *struct{}) (*RecipeCatalogOutput, error) {
	svc, p := reg.recipes()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	out := RecipeCatalog{Categories: []RecipeCategory{}}
	for _, cat := range svc.Recipes() {
		defs := make([]RecipeDefinition, 0, len(cat.Recipes))
		for _, def := range cat.Recipes {
			defs = append(defs, recipeDefinitionOf(def))
		}
		out.Categories = append(out.Categories, RecipeCategory{Category: cat.Category, Recipes: defs})
	}
	return &RecipeCatalogOutput{Body: out}, nil
}

func (reg *Registry) listSectionRecipeCandidates(ctx context.Context, in *RecipeCandidatesInput) (*RecipeCandidateCollectionOutput, error) {
	svc, p := reg.recipes()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	cands, err := svc.RecipeCandidates(ctx, in.Type)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := RecipeCandidateCollection{Candidates: make([]RecipeCandidate, 0, len(cands))}
	for _, c := range cands {
		out.Candidates = append(out.Candidates, RecipeCandidate{Value: c.Value, DisplayName: c.DisplayName, Subtitle: c.Subtitle})
	}
	return &RecipeCandidateCollectionOutput{Body: out}, nil
}
