package apiv2

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	catalogrepo "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/policy"
)

// AdminEpisodeOrderService reads, previews and saves a series' episode order
// (metadata.MetadataService).
type AdminEpisodeOrderService interface {
	EpisodeOrderState(ctx context.Context, seriesID string, withOrders bool) (metadata.EpisodeOrderState, error)
	PreviewEpisodeOrder(ctx context.Context, seriesID string, settings metadata.EpisodeOrderSettings) (metadata.EpisodeOrderPreview, error)
	SaveEpisodeOrder(ctx context.Context, seriesID string, settings metadata.EpisodeOrderSettings, revision *int64, userID int) (int64, error)
}

// episodeOrderLayoutCustom is the layout that uses the series' arranged seasons.
const episodeOrderLayoutCustom = "custom"

// EpisodeOrderChoice is how a series orders its episodes.
type EpisodeOrderChoice struct {
	Provider  string `json:"provider" pattern:"^(?:tvdb|tmdb)?$" doc:"Provider whose order the series follows; empty follows its library" example:"tmdb"`
	OrderID   string `json:"order_id" maxLength:"200" doc:"One of the provider's orders (the id of an EpisodeOrderOption); empty is its default order. Needs a provider" example:"5e2b0c1bfe5ee0001a5d5b58"`
	FileOrder string `json:"file_order" pattern:"^(?:tvdb|tmdb)?$" doc:"Provider whose default order the file names follow (S02E05 is that order's season 2, episode 5); empty means the series' provider" example:"tvdb"`
	Layout    string `json:"layout" enum:"seasons,absolute,custom" doc:"seasons keeps the order's seasons; absolute numbers every regular episode in one season, specials apart; custom uses the seasons arranged with setAdminCustomSeasons (a save answers 422 before there are any)" example:"seasons"`
}

// EpisodeOrderOption is one of a provider's episode orders for a series.
type EpisodeOrderOption struct {
	ID           string `json:"id" doc:"Sent back as order_id" example:"dvd"`
	Name         string `json:"name" example:"DVD Order"`
	Kind         string `json:"kind" doc:"Open lowercase vocabulary: aired, dvd, absolute, digital, story_arc, production, tv, alternate; treat an unknown kind like alternate" example:"dvd"`
	Description  string `json:"description,omitempty"`
	SeasonCount  int    `json:"season_count" doc:"Regular seasons; 0 when the provider does not say" example:"10"`
	EpisodeCount int    `json:"episode_count" doc:"0 when the provider does not say" example:"214"`
	IsDefault    bool   `json:"is_default" doc:"The order the provider serves without an order_id" example:"false"`
}

// EpisodeOrderProvider lists one provider's orders for the series.
type EpisodeOrderProvider struct {
	Provider  string               `json:"provider" example:"tvdb"`
	Supported bool                 `json:"supported" doc:"False when the provider's plugin serves only its default order" example:"true"`
	Failed    bool                 `json:"failed" doc:"Listing the orders failed; try again later" example:"false"`
	Orders    []EpisodeOrderOption `json:"orders"`
}

// AdminEpisodeOrder is a series' episode order and the choices it has.
type AdminEpisodeOrder struct {
	Inherited       bool                   `json:"inherited" doc:"True while the series follows its library; the choice then shows the defaults" example:"true"`
	LibraryProvider string                 `json:"library_provider" doc:"Provider the series follows without its own choice: its library's episode_order_provider, else the first remote provider of the episode chain" example:"tvdb"`
	Choice          EpisodeOrderChoice     `json:"choice"`
	OrderName       string                 `json:"order_name" doc:"Name of choice.order_id as the provider gave it when the choice was saved; empty for the default order" example:"DVD Order"`
	Providers       []EpisodeOrderProvider `json:"providers" doc:"The series' remote episode providers, in chain order"`
}

// EpisodeOrderPreviewSeason is one season of a previewed order.
type EpisodeOrderPreviewSeason struct {
	Number       int                          `json:"number" doc:"0 is Specials" example:"1"`
	Title        string                       `json:"title" example:"Season 1"`
	EpisodeCount int                          `json:"episode_count" example:"22"`
	FileCount    int                          `json:"file_count" doc:"Files the season would hold" example:"20"`
	Episodes     []EpisodeOrderPreviewEpisode `json:"episodes" doc:"The season's episodes in order"`
}

// EpisodeOrderPreviewEpisode is one episode of a previewed season.
type EpisodeOrderPreviewEpisode struct {
	Number      int    `json:"number" example:"5"`
	Title       string `json:"title" example:"The Broca Divide"`
	File        string `json:"file,omitempty" doc:"Name of the file the episode would link to; absent when none" example:"Stargate SG-1 - S01E05.mkv"`
	FileSeason  int    `json:"file_season" doc:"Season a file name gives the episode in the file order; 0 when that order lacks it" example:"1"`
	FileEpisode int    `json:"file_episode" doc:"Episode number a file name gives it in the file order; 0 when that order lacks it" example:"5"`
}

// AdminEpisodeOrderPreview is what the series would look like under a choice.
type AdminEpisodeOrderPreview struct {
	StructureProvider string                      `json:"structure_provider" doc:"Provider that numbered the preview: the chosen one, or the other when the chosen one has no episodes for the series; empty when neither has any" example:"tvdb"`
	EpisodeCount      int                         `json:"episode_count" example:"214"`
	Seasons           []EpisodeOrderPreviewSeason `json:"seasons" doc:"Regular seasons in order, then Specials"`
	FileCount         int                         `json:"file_count" doc:"The series' files named by season and episode" example:"63"`
	MatchedFiles      int                         `json:"matched_files" doc:"Of those, the files the order places" example:"62"`
	UnmatchedFiles    []string                    `json:"unmatched_files" doc:"File names the order does not place, at most 50"`
	FileOrderFits     []EpisodeOrderFileFit       `json:"file_order_fits" doc:"Files each file order would place with this order, for suggesting the order the files are named in"`
}

// EpisodeOrderFileFit is how many files a file order would place.
type EpisodeOrderFileFit struct {
	FileOrder    string `json:"file_order" doc:"As EpisodeOrderChoice.file_order" example:"tvdb"`
	MatchedFiles int    `json:"matched_files" example:"63"`
}

type AdminEpisodeOrderInput struct {
	ID     string `path:"id" minLength:"1" maxLength:"512"`
	Orders bool   `query:"orders" default:"true" doc:"List each provider's orders; false skips asking the providers and answers empty orders lists"`
}
type AdminEpisodeOrderOutput struct {
	ETag string `header:"ETag"`
	Body AdminEpisodeOrder
}
type AdminEpisodeOrderPreviewInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body EpisodeOrderChoice
}
type AdminEpisodeOrderPreviewOutput struct{ Body AdminEpisodeOrderPreview }
type AdminEpisodeOrderSaveInput struct {
	ID          string `path:"id" minLength:"1" maxLength:"512"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        EpisodeOrderChoice
}

func registerAdminEpisodeOrder(reg *Registry) {
	op := func(method, suffix, id, summary string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/admin/items/{id}/episode-order"+suffix, id, "admin-catalog", summary), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration, ServiceBacked: true, DemoRestricted: method == http.MethodPut, RetrySafety: RetrySafetyNonRetryable}
	}
	get := op(http.MethodGet, "", "getAdminEpisodeOrder", "Read a series' episode order and the orders its providers offer.")
	get.RetrySafety = ""
	Register(reg, get, reg.getAdminEpisodeOrder)
	preview := op(http.MethodPost, "/preview", "previewAdminEpisodeOrder", "Show how a series would be numbered under an episode order, without changing it.")
	preview.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, preview, reg.previewAdminEpisodeOrder)
	set := op(http.MethodPut, "", "setAdminEpisodeOrder", "Save a series' episode order and queue the refresh that renumbers it. Default values return the series to its library's order.")
	set.Guarded, set.GuardedReceipt, set.DefaultStatus = true, true, http.StatusAccepted
	Register(reg, set, reg.setAdminEpisodeOrder)
}

func episodeOrderProblem(err error) *Problem {
	switch {
	case errors.Is(err, catalogrepo.ErrItemNotFound):
		return NewProblem(TypeNotFound, "Series not found")
	case errors.Is(err, metadata.ErrEpisodeOrderNotSeries):
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "path.id", Code: codeInvalid, Detail: "only a series has an episode order"})
	case errors.Is(err, metadata.ErrEpisodeOrderInvalid):
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: err.Error()})
	}
	return serviceProblem(err)
}

func episodeOrderSettingsOf(c EpisodeOrderChoice) metadata.EpisodeOrderSettings {
	layout := c.Layout
	if layout == "" {
		layout = "seasons"
	}
	return metadata.EpisodeOrderSettings{Provider: c.Provider, OrderID: c.OrderID, FileOrder: c.FileOrder, Layout: layout}
}

func (reg *Registry) getAdminEpisodeOrder(ctx context.Context, in *AdminEpisodeOrderInput) (*AdminEpisodeOrderOutput, error) {
	if reg.deps.AdminEpisodeOrders == nil {
		return nil, unavailable("episode orders")
	}
	state, err := reg.deps.AdminEpisodeOrders.EpisodeOrderState(ctx, in.ID, in.Orders)
	if err != nil {
		return nil, episodeOrderProblem(err)
	}
	out := AdminEpisodeOrder{
		Inherited:       !state.Override,
		LibraryProvider: state.LibraryProvider,
		OrderName:       state.OrderName,
		Choice: EpisodeOrderChoice{
			Provider: state.Settings.Provider, OrderID: state.Settings.OrderID,
			FileOrder: state.Settings.FileOrder, Layout: state.Settings.Layout,
		},
		Providers: make([]EpisodeOrderProvider, 0, len(state.Providers)),
	}
	for _, p := range state.Providers {
		entry := EpisodeOrderProvider{Provider: p.Provider, Supported: p.Supported, Failed: p.Failed, Orders: make([]EpisodeOrderOption, 0, len(p.Orders))}
		for _, o := range p.Orders {
			entry.Orders = append(entry.Orders, EpisodeOrderOption{
				ID: o.ID, Name: o.Name, Kind: o.Kind, Description: o.Description,
				SeasonCount: o.SeasonCount, EpisodeCount: o.EpisodeCount, IsDefault: o.IsDefault,
			})
		}
		out.Providers = append(out.Providers, entry)
	}
	return &AdminEpisodeOrderOutput{ETag: episodeOrderTag(in.ID, state.Revision).String(), Body: out}, nil
}

func (reg *Registry) previewAdminEpisodeOrder(ctx context.Context, in *AdminEpisodeOrderPreviewInput) (*AdminEpisodeOrderPreviewOutput, error) {
	if reg.deps.AdminEpisodeOrders == nil {
		return nil, unavailable("episode orders")
	}
	preview, err := reg.deps.AdminEpisodeOrders.PreviewEpisodeOrder(ctx, in.ID, episodeOrderSettingsOf(in.Body))
	if err != nil {
		return nil, episodeOrderProblem(err)
	}
	out := AdminEpisodeOrderPreview{
		StructureProvider: preview.StructureProvider,
		EpisodeCount:      preview.EpisodeCount,
		Seasons:           make([]EpisodeOrderPreviewSeason, 0, len(preview.Seasons)),
		FileCount:         preview.FileCount,
		MatchedFiles:      preview.MatchedFiles,
		UnmatchedFiles:    NonNil(preview.UnmatchedFiles),
	}
	out.FileOrderFits = make([]EpisodeOrderFileFit, 0, len(preview.FileOrderFits))
	for _, fit := range preview.FileOrderFits {
		out.FileOrderFits = append(out.FileOrderFits, EpisodeOrderFileFit{FileOrder: fit.FileOrder, MatchedFiles: fit.MatchedFiles})
	}
	for _, s := range preview.Seasons {
		season := EpisodeOrderPreviewSeason{Number: s.Number, Title: s.Title, EpisodeCount: s.EpisodeCount, FileCount: s.FileCount,
			Episodes: make([]EpisodeOrderPreviewEpisode, 0, len(s.Episodes))}
		for _, e := range s.Episodes {
			season.Episodes = append(season.Episodes, EpisodeOrderPreviewEpisode{Number: e.Number, Title: e.Title, File: e.File, FileSeason: e.FileSeason, FileEpisode: e.FileEpisode})
		}
		out.Seasons = append(out.Seasons, season)
	}
	return &AdminEpisodeOrderPreviewOutput{Body: out}, nil
}

func (reg *Registry) setAdminEpisodeOrder(ctx context.Context, in *AdminEpisodeOrderSaveInput) (*AdminCatalogJobAcceptedOutput, error) {
	if reg.deps.AdminEpisodeOrders == nil {
		return nil, unavailable("episode orders")
	}
	return reg.saveEpisodeOrderGuarded(ctx, in.ID, in.IfMatch, in.IfNoneMatch, func(revision *int64, userID int) (int64, error) {
		return reg.deps.AdminEpisodeOrders.SaveEpisodeOrder(ctx, in.ID, episodeOrderSettingsOf(in.Body), revision, userID)
	})
}

// episodeOrderTag is the validator of a series' episode order: the order
// row's revision, which an arrangement save advances too.
func episodeOrderTag(id string, revision int64) EntityTag {
	return RenderETag("admin-episode-order", id, revision)
}

// saveEpisodeOrderGuarded runs a guarded save of the series' episode order
// (If-Match on the order's revision; 412 when stale, 428 when missing) and
// queues the refresh that applies it: 202 with the job. The order is stored
// before the job is queued, so a failed enqueue answers a problem and the
// series' next refresh applies the order.
func (reg *Registry) saveEpisodeOrderGuarded(ctx context.Context, id, ifMatch, ifNoneMatch string, save func(revision *int64, userID int) (int64, error)) (*AdminCatalogJobAcceptedOutput, error) {
	state, err := reg.deps.AdminEpisodeOrders.EpisodeOrderState(ctx, id, false)
	if err != nil {
		return nil, episodeOrderProblem(err)
	}
	current := episodeOrderTag(id, state.Revision)
	if p := EvaluateGuardedPreconditions(ifMatch, ifNoneMatch, current); p != nil {
		return nil, p
	}
	expected := &state.Revision
	if ifMatch == "*" {
		expected = nil
	}
	claims := claimsFrom(ctx)
	if _, err := save(expected, claims.UserID); err != nil {
		if errors.Is(err, metadata.ErrEpisodeOrderConflict) {
			if latest, readErr := reg.deps.AdminEpisodeOrders.EpisodeOrderState(ctx, id, false); readErr == nil {
				current = episodeOrderTag(id, latest.Revision)
			}
			return nil, StaleVersionProblem(current)
		}
		return nil, episodeOrderProblem(err)
	}
	if reg.deps.AdminItemMetadata == nil {
		return nil, unavailable("item metadata")
	}
	job, err := reg.deps.AdminItemMetadata.CreateItemMetadataRefresh(ctx, id, adminjob.ItemRefreshModeQuick, claims.UserID)
	if err != nil {
		return nil, collectionProblem(err)
	}
	actingAdmin, _ := apimw.ActingAdmin(ctx)
	return &AdminCatalogJobAcceptedOutput{Location: Prefix + "/admin/jobs/" + job.ID, RetryAfter: "5", Body: reg.adminTaskJobOf(ctx, job, actingAdmin)}, nil
}

// AdminCustomSeasonsService reads and saves a series' arranged seasons.
type AdminCustomSeasonsService interface {
	CustomSeasons(ctx context.Context, seriesID string, base *metadata.EpisodeOrderSettings) (metadata.CustomSeasons, error)
	SaveCustomSeasons(ctx context.Context, seriesID string, save metadata.CustomSeasonsSave, revision *int64, userID int) (int64, error)
	UploadCustomSeasonPoster(ctx context.Context, seriesID string, data []byte) (metadata.CustomSeasonPoster, error)
}

// AdminImagePresigner turns stored and provider image paths into URLs a
// browser can load (catalog.DetailService).
type AdminImagePresigner interface {
	PresignImageURLs(ctx context.Context, paths []string, imageType, size string) map[string]string
}

// CustomSeasonPoster is a poster uploaded for an arranged season. Send it
// back unchanged as the season's poster.
type CustomSeasonPoster struct {
	Path      string `json:"path" maxLength:"1024" doc:"Artwork storage path"`
	Source    string `json:"source" maxLength:"1024" pattern:"^upload://" doc:"The upload:// source of the image"`
	Thumbhash string `json:"thumbhash" maxLength:"256"`
}

// CustomSeasonsEpisode is one episode in the season arranger.
type CustomSeasonsEpisode struct {
	Key                 string `json:"key" doc:"Identifies the episode across refreshes; send it back in its season's episodes" example:"tvdb:85173"`
	Title               string `json:"title" example:"Children of the Gods"`
	ProviderSeason      int    `json:"provider_season" doc:"Season in the base order" example:"1"`
	ProviderEpisode     int    `json:"provider_episode" doc:"Episode number in the base order" example:"1"`
	AirDate             string `json:"air_date,omitempty" example:"1997-07-27"`
	StillURL            string `json:"still_url,omitempty" format:"uri-reference" doc:"The provider's still for the episode; absent when it has none"`
	File                string `json:"file,omitempty" doc:"Name of the file the episode links to; absent when none" example:"Stargate SG-1 - S01E01.mkv"`
	PlacedAutomatically bool   `json:"placed_automatically" doc:"The saved arrangement did not place this episode (it is new since); it went to the end of the last season" example:"false"`
}

// CustomSeasonsSeason is one season in the arranger; number 0 is Specials.
type CustomSeasonsSeason struct {
	Number            int                    `json:"number" minimum:"0" example:"1"`
	Title             string                 `json:"title" maxLength:"500" doc:"The admin's title; empty shows provider_title" example:""`
	Overview          string                 `json:"overview" maxLength:"20000" doc:"The admin's description; empty shows provider_overview" example:""`
	ProviderTitle     string                 `json:"provider_title" doc:"Title of the base order's season with this number" example:"Season 1"`
	ProviderOverview  string                 `json:"provider_overview" doc:"Description of the base order's season with this number"`
	Poster            *CustomSeasonPoster    `json:"poster,omitempty" doc:"The admin's uploaded poster; absent shows provider_poster_url"`
	PosterURL         string                 `json:"poster_url,omitempty" format:"uri-reference" doc:"URL of poster"`
	ProviderPosterURL string                 `json:"provider_poster_url,omitempty" format:"uri-reference" doc:"Poster of the base order's season with this number"`
	Episodes          []CustomSeasonsEpisode `json:"episodes" doc:"In order"`
}

// AdminCustomSeasons is a series' arranged seasons, or a starting point taken
// from its base order.
type AdminCustomSeasons struct {
	Saved             bool                  `json:"saved" doc:"False when the series has no arrangement yet and this one starts from the base order" example:"true"`
	Choice            EpisodeOrderChoice    `json:"choice" doc:"The base order; layout is custom"`
	StructureProvider string                `json:"structure_provider" doc:"Provider that numbered the base order; empty when no provider has episodes for the series" example:"tvdb"`
	Seasons           []CustomSeasonsSeason `json:"seasons" doc:"Regular seasons in order, then Specials"`
}

type AdminCustomSeasonsInput struct {
	ID        string `path:"id" minLength:"1" maxLength:"512"`
	StartOver bool   `query:"start_over" doc:"Start from the seasons of the order given by provider, order_id and file_order instead of the stored arrangement"`
	Provider  string `query:"provider" pattern:"^(?:tvdb|tmdb)?$" doc:"With start_over: the base order's provider; empty follows the library"`
	OrderID   string `query:"order_id" maxLength:"200" doc:"With start_over: one of the provider's orders; empty is its default"`
	FileOrder string `query:"file_order" pattern:"^(?:tvdb|tmdb)?$" doc:"With start_over: the order the file names follow"`
}
type AdminCustomSeasonsOutput struct {
	ETag string `header:"ETag"`
	Body AdminCustomSeasons
}
type AdminCustomSeasonsSaveInput struct {
	ID          string `path:"id" minLength:"1" maxLength:"512"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Provider  string `json:"provider" pattern:"^(?:tvdb|tmdb)?$" doc:"The base order's provider; empty follows the library"`
		OrderID   string `json:"order_id" maxLength:"200" doc:"One of the provider's orders; empty is its default"`
		FileOrder string `json:"file_order" pattern:"^(?:tvdb|tmdb)?$" doc:"The order the file names follow"`
		Seasons   []struct {
			Number   int                 `json:"number" minimum:"0" doc:"Regular seasons are numbered 1 to N without gaps; 0 holds the specials"`
			Title    string              `json:"title" maxLength:"500" doc:"Empty shows the base order's title for this number"`
			Overview string              `json:"overview" maxLength:"20000" doc:"Empty shows the base order's description for this number"`
			Poster   *CustomSeasonPoster `json:"poster,omitempty" doc:"A poster from uploadAdminCustomSeasonPoster; absent shows the base order's poster for this number"`
			Episodes []string            `json:"episodes" maxItems:"10000" doc:"Episode keys in order"`
		} `json:"seasons" minItems:"1" maxItems:"500"`
	}
}

func registerAdminCustomSeasons(reg *Registry) {
	op := func(method, id, summary string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/admin/items/{id}/custom-seasons", id, "admin-catalog", summary), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration, ServiceBacked: true, DemoRestricted: method == http.MethodPut, RetrySafety: RetrySafetyNonRetryable}
	}
	get := op(http.MethodGet, "getAdminCustomSeasons", "Read a series' arranged seasons, or a starting arrangement from an episode order.")
	get.RetrySafety = ""
	Register(reg, get, reg.getAdminCustomSeasons)
	set := op(http.MethodPut, "setAdminCustomSeasons", "Save a series' arranged seasons, switch it to them and queue the refresh that renumbers it. Guarded by the series' episode order ETag.")
	set.Guarded, set.GuardedReceipt, set.DefaultStatus = true, true, http.StatusAccepted
	Register(reg, set, reg.setAdminCustomSeasons)
	upload := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/items/{id}/custom-seasons/posters", "uploadAdminCustomSeasonPoster", "admin-catalog",
		"Store an uploaded poster for an arranged season without publishing it; saving an arrangement that names it puts it on that season."),
		Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration, ServiceBacked: true, DemoRestricted: true,
		RetrySafety: RetrySafetyNaturalIdempotent, MaxBodyBytes: maxPosterBytes + posterFormOverhead}
	Register(reg, upload, reg.uploadAdminCustomSeasonPoster)
}

func (reg *Registry) getAdminCustomSeasons(ctx context.Context, in *AdminCustomSeasonsInput) (*AdminCustomSeasonsOutput, error) {
	if reg.deps.AdminCustomSeasons == nil {
		return nil, unavailable("custom seasons")
	}
	var base *metadata.EpisodeOrderSettings
	if in.StartOver {
		base = &metadata.EpisodeOrderSettings{Provider: in.Provider, OrderID: in.OrderID, FileOrder: in.FileOrder, Layout: episodeOrderLayoutCustom}
	}
	custom, err := reg.deps.AdminCustomSeasons.CustomSeasons(ctx, in.ID, base)
	if err != nil {
		return nil, episodeOrderProblem(err)
	}
	out := AdminCustomSeasons{
		Saved: custom.Saved, StructureProvider: custom.StructureProvider,
		Choice:  EpisodeOrderChoice{Provider: custom.Settings.Provider, OrderID: custom.Settings.OrderID, FileOrder: custom.Settings.FileOrder, Layout: episodeOrderLayoutCustom},
		Seasons: make([]CustomSeasonsSeason, 0, len(custom.Seasons)),
	}
	posters, stills := reg.customSeasonImages(ctx, custom.Seasons)
	for _, s := range custom.Seasons {
		season := CustomSeasonsSeason{Number: s.Number, Title: s.Title, Overview: s.Overview, ProviderTitle: s.ProviderTitle, ProviderOverview: s.ProviderOverview,
			ProviderPosterURL: posters[s.ProviderPosterPath],
			Episodes:          make([]CustomSeasonsEpisode, 0, len(s.Episodes))}
		for _, e := range s.Episodes {
			season.Episodes = append(season.Episodes, CustomSeasonsEpisode{Key: e.Key, Title: e.Title, ProviderSeason: e.ProviderSeason, ProviderEpisode: e.ProviderEpisode,
				AirDate: e.AirDate, File: e.File, PlacedAutomatically: e.PlacedAutomatically, StillURL: stills[e.StillPath]})
		}
		if s.Poster.Path != "" {
			season.Poster = &CustomSeasonPoster{Path: s.Poster.Path, Source: s.Poster.Source, Thumbhash: s.Poster.Thumbhash}
			season.PosterURL = posters[s.Poster.Path]
		}
		out.Seasons = append(out.Seasons, season)
	}
	return &AdminCustomSeasonsOutput{ETag: episodeOrderTag(in.ID, custom.Revision).String(), Body: out}, nil
}

func (reg *Registry) setAdminCustomSeasons(ctx context.Context, in *AdminCustomSeasonsSaveInput) (*AdminCatalogJobAcceptedOutput, error) {
	if reg.deps.AdminCustomSeasons == nil || reg.deps.AdminEpisodeOrders == nil {
		return nil, unavailable("custom seasons")
	}
	save := metadata.CustomSeasonsSave{
		Settings: metadata.EpisodeOrderSettings{Provider: in.Body.Provider, OrderID: in.Body.OrderID, FileOrder: in.Body.FileOrder, Layout: episodeOrderLayoutCustom},
	}
	for _, s := range in.Body.Seasons {
		season := metadata.CustomSeasonsSeason{Number: s.Number, Title: s.Title, Overview: s.Overview}
		if s.Poster != nil && s.Poster.Path != "" {
			season.Poster = metadata.CustomSeasonPoster{Path: s.Poster.Path, Source: s.Poster.Source, Thumbhash: s.Poster.Thumbhash}
		}
		for _, key := range s.Episodes {
			season.Episodes = append(season.Episodes, metadata.CustomSeasonsEpisode{Key: key})
		}
		save.Seasons = append(save.Seasons, season)
	}
	return reg.saveEpisodeOrderGuarded(ctx, in.ID, in.IfMatch, in.IfNoneMatch, func(revision *int64, userID int) (int64, error) {
		return reg.deps.AdminCustomSeasons.SaveCustomSeasons(ctx, in.ID, save, revision, userID)
	})
}

// customSeasonImages resolves the arranger's posters and stills to URLs,
// small sizes; an image that does not resolve is left out.
func (reg *Registry) customSeasonImages(ctx context.Context, seasons []metadata.CustomSeasonsSeason) (map[string]string, map[string]string) {
	if reg.deps.AdminImagePresigner == nil {
		return map[string]string{}, map[string]string{}
	}
	var posters, stills []string
	for _, s := range seasons {
		for _, path := range []string{s.Poster.Path, s.ProviderPosterPath} {
			if path != "" {
				posters = append(posters, path)
			}
		}
		for _, e := range s.Episodes {
			if e.StillPath != "" {
				stills = append(stills, e.StillPath)
			}
		}
	}
	return reg.deps.AdminImagePresigner.PresignImageURLs(ctx, posters, "poster", "small"),
		reg.deps.AdminImagePresigner.PresignImageURLs(ctx, stills, "still", "small")
}

// AdminCustomSeasonPosterUploadForm is the poster upload's form.
type AdminCustomSeasonPosterUploadForm struct {
	Image huma.FormFile `form:"image" contentType:"image/jpeg,image/png,image/webp,application/octet-stream" required:"true" doc:"The poster: JPEG, PNG or WebP, at most 10 MiB; its bytes decide the format"`
}

type AdminCustomSeasonPosterUploadInput struct {
	ID      string `path:"id" minLength:"1" maxLength:"512"`
	RawBody huma.MultipartFormFiles[AdminCustomSeasonPosterUploadForm]
}

// AdminCustomSeasonPosterUploaded is a stored poster for an arranged season.
type AdminCustomSeasonPosterUploaded struct {
	Poster    CustomSeasonPoster `json:"poster" doc:"Send it as a season's poster in setAdminCustomSeasons"`
	PosterURL string             `json:"poster_url,omitempty" format:"uri-reference"`
}

type AdminCustomSeasonPosterUploadOutput struct {
	Body AdminCustomSeasonPosterUploaded
}

func (reg *Registry) uploadAdminCustomSeasonPoster(ctx context.Context, in *AdminCustomSeasonPosterUploadInput) (*AdminCustomSeasonPosterUploadOutput, error) {
	if reg.deps.AdminCustomSeasons == nil {
		return nil, unavailable("custom seasons")
	}
	form := in.RawBody.Data()
	if form == nil || !form.Image.IsSet {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "body.image", Code: codeRequired, Detail: detailImageFileRequired})
	}
	defer func() { _ = form.Image.Close() }()
	tooLarge := NewProblem(TypePayloadTooLarge, "The image exceeds 10 MiB.")
	if form.Image.Size > metadata.MaxUploadedImageBytes {
		return nil, tooLarge
	}
	data, err := io.ReadAll(io.LimitReader(form.Image, metadata.MaxUploadedImageBytes+1))
	if err != nil {
		return nil, NewProblem(TypeInternalError, "Could not read the image.")
	}
	if len(data) > metadata.MaxUploadedImageBytes {
		return nil, tooLarge
	}
	poster, err := reg.deps.AdminCustomSeasons.UploadCustomSeasonPoster(ctx, in.ID, data)
	switch {
	case errors.Is(err, metadata.ErrUnsupportedUploadedImage):
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "body.image", Code: codeInvalid, Detail: "the file is not a JPEG, PNG or WebP image"})
	case errors.Is(err, metadata.ErrImageUploadUnavailable):
		return nil, NewProblem(TypeCapabilityNotConfigured, "Uploading images needs artwork storage")
	case err != nil:
		return nil, episodeOrderProblem(err)
	}
	out := AdminCustomSeasonPosterUploaded{Poster: CustomSeasonPoster{Path: poster.Path, Source: poster.Source, Thumbhash: poster.Thumbhash}}
	if reg.deps.AdminImagePresigner != nil {
		out.PosterURL = reg.deps.AdminImagePresigner.PresignImageURLs(ctx, []string{poster.Path}, "poster", "small")[poster.Path]
	}
	return &AdminCustomSeasonPosterUploadOutput{Body: out}, nil
}
