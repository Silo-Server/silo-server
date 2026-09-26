package apiv2

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
)

// Section catalog-items, stage B: the viewer-facing item actions (trailer
// refresh, on-view description translation) with their capability documents,
// people, and literary works.

// --- inputs ---

// CatalogItemActionInput names the item an action applies to.
type CatalogItemActionInput struct {
	ID string `path:"id" doc:"Content id of a movie, series, season, or episode" example:"movie:heat-1995"`
}

// TranslateDescriptionInput is the translateCatalogItemDescription request.
type TranslateDescriptionInput struct {
	ID   string `path:"id" doc:"Content id of the item, season, or episode" example:"movie:heat-1995"`
	Body TranslateDescription
}

// TranslateDescription names the language the viewer wants.
type TranslateDescription struct {
	TargetLanguage string `json:"target_language" minLength:"1" maxLength:"16" doc:"BCP 47 tag of the wanted language, as the detail document's pending_translation_language reported it" example:"de"`
}

// PeopleSearchInput is the listPeople query.
type PeopleSearchInput struct {
	Q          string `query:"q" maxLength:"200" doc:"Name prefix or fragment; empty lists the first people"`
	Limit      int    `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"Most people to answer"`
	MediaScope string `query:"media_scope" enum:"video,movie,series,episode,audiobook,ebook,manga" doc:"Restrict people to accessible credits in this media scope; omitted searches all media scopes"`
}

// PersonInput names one person.
type PersonInput struct {
	ID ID `path:"id" doc:"Person identifier" example:"7"`
}

// PersonReadInput names one person to read.
type PersonReadInput struct {
	ID       ID   `path:"id" doc:"Person identifier" example:"7"`
	Prefetch bool `query:"prefetch" default:"false" doc:"Marks a speculative read, such as warming a cache for a cast list. The read does not queue a provider refresh."`
}

// LiteraryWorkInput names one literary work.
type LiteraryWorkInput struct {
	WorkID string `path:"work_id" doc:"Work identifier" example:"work:dune-1965"`
}

// --- outputs ---

// TrailersCapability reports whether this server offers the viewer-facing
// trailer fetch and what the action answers.
type TrailersCapability struct {
	Capability
	CooldownSeconds int      `json:"cooldown_seconds" doc:"Per-item window between viewer-triggered refreshes, in seconds; 0 when not configured" example:"86400"`
	Statuses        []string `json:"statuses" doc:"Every value refreshCatalogItemTrailers may answer in status; empty when not configured"`
	SupportedTypes  []string `json:"supported_types" doc:"Item types the action applies to; empty when not configured"`
}

// TrailersCapabilityOutput is the getTrailersCapability response.
type TrailersCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         TrailersCapability
}

// TrailerRefresh is the answer of a trailer refresh request.
type TrailerRefresh struct {
	Status        string   `json:"status" enum:"queued,cooldown,disabled" doc:"queued: a fetch was scheduled (202); cooldown: the item was refreshed recently and nothing was queued (200); disabled: an administrator turned trailers off (200)"`
	NextAllowedAt *Instant `json:"next_allowed_at,omitempty" doc:"When the item may be refreshed again; cooldown only"`
}

// TrailerRefreshOutput is the refreshCatalogItemTrailers response: 202 when a
// fetch was queued, 200 when the cooldown or disabled state answered instead.
type TrailerRefreshOutput struct {
	Status int
	Body   TrailerRefresh
}

// MetadataAICapability reports whether AI metadata translation is configured
// and how the viewer-facing on-view translation behaves.
type MetadataAICapability struct {
	Capability
	OnView string `json:"on_view" doc:"Viewer-triggered translation mode: off, button (the detail page offers the action), or auto (the server queues it on view)" example:"button"`
}

// MetadataAICapabilityOutput is the getMetadataAICapability response.
type MetadataAICapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         MetadataAICapability
}

// MetadataTranslationJob is one queued or running translation.
type MetadataTranslationJob struct {
	ID              ID      `json:"id" example:"42"`
	TargetKind      string  `json:"target_kind" doc:"item, season, or episode"`
	ContentID       string  `json:"content_id"`
	IncludeChildren bool    `json:"include_children"`
	SourceLanguage  string  `json:"source_language"`
	TargetLanguage  string  `json:"target_language"`
	Engine          string  `json:"engine"`
	Model           string  `json:"model"`
	Status          string  `json:"status" doc:"pending, running, completed, failed, or canceled"`
	Progress        float64 `json:"progress" doc:"0 to 1"`
	ProgressMessage string  `json:"progress_message"`
	FieldsDone      int     `json:"fields_done"`
	FieldsTotal     int     `json:"fields_total"`
	Force           bool    `json:"force"`
	ErrorMessage    string  `json:"error_message,omitempty"`
	CreatedAt       Instant `json:"created_at"`
	UpdatedAt       Instant `json:"updated_at"`
}

// MetadataTranslationJobOutput is the translateCatalogItemDescription response.
type MetadataTranslationJobOutput struct {
	Body MetadataTranslationJob
}

// Person is a cast or crew member, author, or narrator.
type Person struct {
	ID             ID      `json:"id" example:"7"`
	Name           string  `json:"name" example:"Al Pacino"`
	Bio            string  `json:"bio,omitempty"`
	BirthDate      *string `json:"birth_date,omitempty" doc:"Calendar date, YYYY-MM-DD" example:"1940-04-25"`
	DeathDate      *string `json:"death_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	Birthplace     string  `json:"birthplace,omitempty"`
	Homepage       string  `json:"homepage,omitempty"`
	PhotoURL       string  `json:"photo_url,omitempty" doc:"Presigned photo URL; absent when the person has no photo"`
	PhotoThumbhash string  `json:"photo_thumbhash,omitempty"`
	TmdbID         string  `json:"tmdb_id,omitempty"`
	ImdbID         string  `json:"imdb_id,omitempty"`
	TvdbID         string  `json:"tvdb_id,omitempty"`
	PlexGUID       string  `json:"plex_guid,omitempty"`
}

// PersonCollection is a bounded list of people.
type PersonCollection struct {
	Collection[Person]
}

// PersonCollectionOutput is the listPeople response.
type PersonCollectionOutput struct {
	Body PersonCollection
}

// PersonOutput is the getPerson response.
type PersonOutput struct {
	Body Person
}

// PersonRefresh acknowledges a queued provider refresh.
type PersonRefresh struct {
	Status   string `json:"status" enum:"queued"`
	PersonID ID     `json:"person_id" example:"7"`
}

// PersonRefreshOutput is the refreshPerson response.
type PersonRefreshOutput struct {
	Body PersonRefresh
}

// LiteraryWork groups the ebook and audiobook editions of one title.
type LiteraryWork struct {
	WorkID          string               `json:"work_id"`
	WorkTitle       string               `json:"work_title" example:"Dune"`
	Authors         []LiteraryWorkAuthor `json:"authors" doc:"Empty, never null"`
	Formats         []LiteraryWorkFormat `json:"formats" doc:"The editions the viewer can see; empty, never null"`
	PrimaryCoverURL string               `json:"primary_cover_url,omitempty"`
	Metadata        LiteraryWorkMetadata `json:"metadata"`
}

// LiteraryWorkAuthor is one credited author.
type LiteraryWorkAuthor struct {
	PersonID ID     `json:"person_id,omitempty"`
	Name     string `json:"name"`
}

// LiteraryWorkFormat is one edition of the work.
type LiteraryWorkFormat struct {
	Type           string                `json:"type" doc:"ebook or audiobook"`
	ContentID      string                `json:"content_id"`
	LibraryID      ID                    `json:"library_id,omitempty"`
	AvailableFiles []LiteraryWorkFile    `json:"available_files" doc:"Empty, never null"`
	Progress       *LiteraryWorkProgress `json:"progress,omitempty" doc:"The viewer's progress in this edition"`
}

// LiteraryWorkFile is one file of an edition.
type LiteraryWorkFile struct {
	FileID           ID      `json:"file_id"`
	OriginalFilename string  `json:"original_filename"`
	Format           string  `json:"format"`
	MIMEType         string  `json:"mime_type,omitempty"`
	Size             int64   `json:"size,omitempty"`
	DurationSeconds  float64 `json:"duration_seconds,omitempty"`
}

// LiteraryWorkProgress is the viewer's position in an edition.
type LiteraryWorkProgress struct {
	Kind            string   `json:"kind" doc:"reading or listening"`
	Progress        *float64 `json:"progress,omitempty" doc:"0 to 1"`
	PositionSeconds *float64 `json:"position_seconds,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	UpdatedAt       *Instant `json:"updated_at,omitempty"`
}

// LiteraryWorkMetadata is the work's shared descriptive metadata.
type LiteraryWorkMetadata struct {
	Description   string              `json:"description,omitempty"`
	Series        *LiteraryWorkSeries `json:"series,omitempty"`
	Genres        []string            `json:"genres" doc:"Empty, never null"`
	PublishedDate string              `json:"published_date,omitempty" doc:"Calendar date, YYYY-MM-DD"`
	Publisher     string              `json:"publisher,omitempty"`
}

// LiteraryWorkSeries places the work in a book series.
type LiteraryWorkSeries struct {
	Name  string   `json:"name"`
	Index *float64 `json:"index,omitempty"`
}

// LiteraryWorkOutput is the getLiteraryWork response.
type LiteraryWorkOutput struct {
	Body LiteraryWork
}

// --- registration ---

const (
	cacheControlPrivateNoCache = "private, no-cache"
	trailersDomain             = "trailers"
	metadataAIDomain           = "metadata AI translation"
	trailerStatusQueued        = "queued"
	metadataAIOnViewOff        = "off"
)

func registerCatalogActions(reg *Registry) {
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/capabilities/trailers", "getTrailersCapability", "catalog",
		"Whether this server offers the viewer-facing trailer fetch, its cooldown, and the statuses the action answers.")), reg.getTrailersCapability)
	refresh := humaOp(http.MethodPost, Prefix+"/catalog/items/{id}/trailers/refresh", "refreshCatalogItemTrailers", "catalog",
		"Ask the server to fetch a movie's or series' remote trailers; answers 202 when queued, 200 with the cooldown or disabled state otherwise.")
	refresh.DefaultStatus = http.StatusAccepted
	refresh.Responses = map[string]*huma.Response{
		"200": {Description: "The item is inside its cooldown window or trailers are disabled; nothing was queued.",
			Content: map[string]*huma.MediaType{mediaTypeJSON: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(TrailerRefresh{}), true, "")}}},
	}
	refresh.Errors = []int{http.StatusConflict, http.StatusTooManyRequests}
	refreshOperation := viewerOperation(refresh)
	refreshOperation.RetrySafety = RetrySafetyNonRetryable
	Register(reg, refreshOperation, reg.refreshCatalogItemTrailers)

	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/capabilities/metadata-ai", "getMetadataAICapability", "catalog",
		"Whether AI metadata translation is configured and how the viewer-facing on-view translation behaves.")), reg.getMetadataAICapability)
	translate := humaOp(http.MethodPost, Prefix+"/catalog/items/{id}/translate-description", "translateCatalogItemDescription", "catalog",
		"Queue a translation of the item's descriptions into the language the detail document reported missing; answers 202 with the job.")
	translate.DefaultStatus = http.StatusAccepted
	translate.Errors = []int{http.StatusConflict}
	translateOperation := viewerOperation(translate)
	translateOperation.RetrySafety = RetrySafetyCoalescing
	Register(reg, translateOperation, reg.translateCatalogItemDescription)

	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/people", "listPeople", "catalog",
		"Search people by name, with exact matches first and optional media scope.")), reg.listPeople)
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/people/{id}", "getPerson", "catalog",
		"One person; viewing queues a provider refresh when one is due, unless the read is a prefetch.")), reg.getPerson)
	refreshPerson := humaOp(http.MethodPost, Prefix+"/catalog/people/{id}/refresh", "refreshPerson", "catalog",
		"Queue a provider refresh of the person; answers 202 once queued.")
	refreshPerson.DefaultStatus = http.StatusAccepted
	refreshPerson.Errors = []int{http.StatusTooManyRequests}
	refreshPersonOperation := viewerOperation(refreshPerson)
	refreshPersonOperation.RetrySafety = RetrySafetyNonRetryable
	Register(reg, refreshPersonOperation, reg.refreshPerson)

	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/catalog/works/{work_id}", "getLiteraryWork", "catalog",
		"A literary work with the ebook and audiobook editions the viewer can see and their progress.")), reg.getLiteraryWork)
}

// --- helpers ---

// catalogActionProblem maps an action seam failure: the per-user limiter's
// hint becomes Retry-After, an unsupported target is 422 at path.id, an
// unconfigured feature is the capability problem, the rest follow the status.
func catalogActionProblem(err error) *Problem {
	var apiErr *handlers.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusBadRequest && apiErr.Code == "unsupported_type":
			return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationPathID, Code: codeInvalid, Detail: apiErr.Message})
		case apiErr.Status == http.StatusBadRequest && apiErr.Field != "":
			return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "body." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
		case apiErr.Status == http.StatusBadRequest:
			return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: apiErr.Message})
		case apiErr.Status == http.StatusServiceUnavailable && (apiErr.Code == "not_configured" || apiErr.Message == "Trailer refresh is not configured"):
			return NewProblem(TypeCapabilityNotConfigured, apiErr.Message)
		}
	}
	return serviceProblem(err)
}

func (reg *Registry) getTrailersCapability(_ context.Context, _ *CapabilityInput) (*TrailersCapabilityOutput, error) {
	view := handlers.TrailerRefreshCapabilityView{Statuses: []string{}, SupportedTypes: []string{}}
	if reg.deps.CatalogTrailers != nil {
		view = reg.deps.CatalogTrailers.TrailerRefreshCapability()
	}
	state := StateNotConfigured
	if view.Enabled {
		state = StateAvailable
	}
	doc := TrailersCapability{Capability: Capability{State: state}, CooldownSeconds: view.CooldownSeconds, Statuses: view.Statuses, SupportedTypes: view.SupportedTypes}
	return &TrailersCapabilityOutput{CacheControl: cacheControlPrivateNoCache, Body: doc}, nil
}

func (reg *Registry) refreshCatalogItemTrailers(ctx context.Context, in *CatalogItemActionInput) (*TrailerRefreshOutput, error) {
	if reg.deps.CatalogTrailers == nil || reg.deps.CatalogAccess == nil {
		return nil, CapabilityProblem(StateNotConfigured, trailersDomain)
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.CatalogTrailers.RequestTrailersRefresh(ctx, userID, strings.TrimSpace(in.ID), func() (catalogpkg.AccessFilter, error) {
		return reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	})
	if err != nil {
		return nil, catalogActionProblem(err)
	}
	out := &TrailerRefreshOutput{Status: http.StatusOK, Body: TrailerRefresh{Status: view.Status, NextAllowedAt: instantPtr(view.NextAllowedAt)}}
	if view.Status == trailerStatusQueued {
		out.Status = http.StatusAccepted
	}
	return out, nil
}

func (reg *Registry) getMetadataAICapability(_ context.Context, _ *CapabilityInput) (*MetadataAICapabilityOutput, error) {
	view := handlers.MetadataAIStatusView{OnView: metadataAIOnViewOff}
	if reg.deps.MetadataAI != nil {
		view = reg.deps.MetadataAI.Status()
	}
	state := StateNotConfigured
	if view.Enabled {
		state = StateAvailable
	}
	doc := MetadataAICapability{Capability: Capability{State: state}, OnView: view.OnView}
	return &MetadataAICapabilityOutput{CacheControl: cacheControlPrivateNoCache, Body: doc}, nil
}

func (reg *Registry) translateCatalogItemDescription(ctx context.Context, in *TranslateDescriptionInput) (*MetadataTranslationJobOutput, error) {
	if reg.deps.MetadataAI == nil {
		return nil, CapabilityProblem(StateNotConfigured, metadataAIDomain)
	}
	userID, _, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	var requestedBy *int
	if userID != 0 {
		requestedBy = &userID
	}
	viewer, p := reg.itemViewer(ctx, "", "", "")
	if p != nil {
		return nil, p
	}
	job, err := reg.deps.MetadataAI.TranslateOnView(ctx, viewer.Access, in.ID, in.Body.TargetLanguage, requestedBy)
	if err != nil {
		return nil, catalogActionProblem(err)
	}
	return &MetadataTranslationJobOutput{Body: translationJobOf(job)}, nil
}

func (reg *Registry) people() (PeopleService, *Problem) {
	if reg.deps.People == nil {
		return nil, unavailable("people")
	}
	return reg.deps.People, nil
}

func (reg *Registry) listPeople(ctx context.Context, in *PeopleSearchInput) (*PersonCollectionOutput, error) {
	svc, p := reg.people()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	if reg.deps.CatalogAccess == nil {
		return nil, unavailable("catalog access")
	}
	filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	people, err := svc.SearchPeopleScoped(ctx, in.Q, in.Limit, in.MediaScope, filter)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]Person, 0, len(people))
	for _, v := range people {
		items = append(items, personOf(v))
	}
	return &PersonCollectionOutput{Body: PersonCollection{Collection: NewCollection(items)}}, nil
}

func (reg *Registry) getPerson(ctx context.Context, in *PersonReadInput) (*PersonOutput, error) {
	svc, p := reg.people()
	if p != nil {
		return nil, p
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	id, p := in.ID.positive(locationPathID)
	if p != nil {
		return nil, p
	}
	person, err := svc.Person(ctx, int64(id), !in.Prefetch)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &PersonOutput{Body: personOf(person)}, nil
}

func (reg *Registry) refreshPerson(ctx context.Context, in *PersonInput) (*PersonRefreshOutput, error) {
	svc, p := reg.people()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	id, p := in.ID.positive(locationPathID)
	if p != nil {
		return nil, p
	}
	if err := svc.RefreshPerson(ctx, userID, int64(id)); err != nil {
		return nil, catalogActionProblem(err)
	}
	return &PersonRefreshOutput{Body: PersonRefresh{Status: trailerStatusQueued, PersonID: in.ID}}, nil
}

func (reg *Registry) getLiteraryWork(ctx context.Context, in *LiteraryWorkInput) (*LiteraryWorkOutput, error) {
	if reg.deps.LiteraryWorks == nil || reg.deps.CatalogAccess == nil {
		return nil, unavailable("literary works")
	}
	if _, _, p := viewerIdentity(ctx); p != nil {
		return nil, p
	}
	workID := strings.TrimSpace(in.WorkID)
	if workID == "" {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "path.work_id", Code: codeRequired, Detail: "work_id is required"})
	}
	filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	work, err := reg.deps.LiteraryWorks.Work(ctx, workID, filter)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &LiteraryWorkOutput{Body: literaryWorkOf(work)}, nil
}

// --- renderers ---

func translationJobOf(j *translation.Job) MetadataTranslationJob {
	if j == nil {
		return MetadataTranslationJob{}
	}
	return MetadataTranslationJob{ID: IDFromInt(j.ID), TargetKind: string(j.TargetKind), ContentID: j.ContentID, IncludeChildren: j.IncludeChildren,
		SourceLanguage: j.SourceLanguage, TargetLanguage: j.TargetLanguage, Engine: j.Engine, Model: j.Model, Status: string(j.Status),
		Progress: j.Progress, ProgressMessage: j.ProgressMessage, FieldsDone: j.FieldsDone, FieldsTotal: j.FieldsTotal, Force: j.Force,
		ErrorMessage: j.ErrorMessage, CreatedAt: NewInstant(j.CreatedAt), UpdatedAt: NewInstant(j.UpdatedAt)}
}

func personOf(v handlers.PersonView) Person {
	return Person{ID: IDFromInt(v.ID), Name: v.Name, Bio: v.Bio, BirthDate: v.BirthDate, DeathDate: v.DeathDate, Birthplace: v.Birthplace, Homepage: v.Homepage,
		PhotoURL: v.PhotoURL, PhotoThumbhash: v.PhotoThumbhash, TmdbID: v.TmdbID, ImdbID: v.ImdbID, TvdbID: v.TvdbID, PlexGUID: v.PlexGUID}
}

func literaryWorkOf(w *literaryworks.DetailResponse) LiteraryWork {
	out := LiteraryWork{WorkID: w.WorkID, WorkTitle: w.WorkTitle, PrimaryCoverURL: w.PrimaryCoverURL, Authors: []LiteraryWorkAuthor{}, Formats: []LiteraryWorkFormat{},
		Metadata: LiteraryWorkMetadata{Description: w.Metadata.Description, Genres: w.Metadata.Genres, PublishedDate: w.Metadata.PublishedDate, Publisher: w.Metadata.Publisher}}
	if out.Metadata.Genres == nil {
		out.Metadata.Genres = []string{}
	}
	if s := w.Metadata.Series; s != nil {
		out.Metadata.Series = &LiteraryWorkSeries{Name: s.Name, Index: s.Index}
	}
	for _, a := range w.Authors {
		out.Authors = append(out.Authors, LiteraryWorkAuthor{PersonID: ID(a.PersonID), Name: a.Name})
	}
	for _, f := range w.Formats {
		format := LiteraryWorkFormat{Type: f.Type, ContentID: f.ContentID, LibraryID: idOfPositive(f.LibraryID), AvailableFiles: []LiteraryWorkFile{}}
		for _, file := range f.AvailableFiles {
			format.AvailableFiles = append(format.AvailableFiles, LiteraryWorkFile{FileID: IDFromInt(int64(file.FileID)), OriginalFilename: file.OriginalName, Format: file.Format,
				MIMEType: file.MIMEType, Size: file.Size, DurationSeconds: file.DurationSeconds})
		}
		if p := f.Progress; p != nil {
			progress := &LiteraryWorkProgress{Kind: p.Kind, Progress: p.Progress, PositionSeconds: p.PositionSeconds, DurationSeconds: p.DurationSeconds}
			if t, err := time.Parse(time.RFC3339, p.UpdatedAt); err == nil {
				progress.UpdatedAt = instantPtr(&t)
			}
			format.Progress = progress
		}
		out.Formats = append(out.Formats, format)
	}
	return out
}
