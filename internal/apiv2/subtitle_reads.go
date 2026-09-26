package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type SubtitleReadService interface {
	ListStoredSubtitles(context.Context, catalogpkg.AccessFilter, int) ([]subtitles.DownloadedSubtitle, error)
	SearchSubtitles(context.Context, catalogpkg.AccessFilter, int, []string) (*subtitles.SearchResponse, error)
}

type StoredSubtitle struct {
	ID              ID      `json:"id"`
	MediaFileID     ID      `json:"media_file_id"`
	Provider        string  `json:"provider"`
	Language        string  `json:"language"`
	Format          string  `json:"format"`
	ReleaseName     string  `json:"release_name"`
	Score           float64 `json:"score"`
	HearingImpaired bool    `json:"hearing_impaired"`
	CreatedAt       Instant `json:"created_at"`
}

type StoredSubtitles struct {
	Subtitles []StoredSubtitle `json:"subtitles"`
}
type StoredSubtitlesOutput struct{ Body StoredSubtitles }
type StoredSubtitlesInput struct {
	MediaFileID ID `path:"media_file_id"`
}

type SubtitleSearchResult struct {
	ID              ID       `json:"id"`
	Provider        string   `json:"provider"`
	Language        string   `json:"language"`
	ReleaseName     string   `json:"release_name"`
	Format          string   `json:"format"`
	Score           float64  `json:"score"`
	Downloads       int      `json:"downloads"`
	HearingImpaired bool     `json:"hearing_impaired"`
	UploadDate      *Instant `json:"upload_date,omitempty"`
}

type SubtitleSearchResults struct {
	Results  []SubtitleSearchResult `json:"results"`
	Warnings []string               `json:"warnings"`
}
type SubtitleSearchBody struct {
	MediaFileID ID       `json:"media_file_id"`
	Languages   []string `json:"languages" maxItems:"100"`
}
type SubtitleSearchInput struct{ Body SubtitleSearchBody }
type SubtitleSearchOutput struct{ Body SubtitleSearchResults }

func registerSubtitleReads(reg *Registry) {
	op := func(method, path, id string) Operation {
		return Operation{Operation: humaOp(method, Prefix+path, id, "subtitles", "Read subtitles for an accessible media file."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}
	}
	Register(reg, op(http.MethodGet, "/subtitles/{media_file_id}", "listStoredSubtitles"), func(ctx context.Context, in *StoredSubtitlesInput) (*StoredSubtitlesOutput, error) {
		id, p := in.MediaFileID.positive("path.media_file_id")
		if p != nil {
			return nil, p
		}
		access, p := reg.subtitleReadAccess(ctx)
		if p != nil {
			return nil, p
		}
		rows, err := reg.deps.SubtitleReads.ListStoredSubtitles(ctx, access, id)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := StoredSubtitles{Subtitles: make([]StoredSubtitle, 0, len(rows))}
		for _, row := range rows {
			if _, ok := subtitles.CanonicalProviderLanguage(row.Provider, row.Language); !ok {
				continue
			}
			out.Subtitles = append(out.Subtitles, storedSubtitleView(row))
		}
		return &StoredSubtitlesOutput{Body: out}, nil
	})
	search := op(http.MethodPost, "/subtitles/search", "searchSubtitles")
	search.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, search, func(ctx context.Context, in *SubtitleSearchInput) (*SubtitleSearchOutput, error) {
		id, p := in.Body.MediaFileID.positive("body.media_file_id")
		if p != nil {
			return nil, p
		}
		access, p := reg.subtitleReadAccess(ctx)
		if p != nil {
			return nil, p
		}
		languages, err := subtitles.NormalizeSearchLanguages(in.Body.Languages)
		if err != nil {
			return nil, NewProblem(TypeValidationFailed, err.Error())
		}
		view, err := reg.deps.SubtitleReads.SearchSubtitles(ctx, access, id, languages)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := SubtitleSearchResults{Results: []SubtitleSearchResult{}, Warnings: []string{}}
		if view == nil {
			return nil, NewProblem(TypeInternalError, "Subtitle search returned no result.")
		}
		for _, row := range view.Results {
			language, ok := subtitles.CanonicalProviderLanguage(row.Provider, row.Language)
			if !ok {
				continue
			}
			result := SubtitleSearchResult{ID: ID(row.ID), Provider: row.Provider, Language: language, Format: string(row.Format), ReleaseName: row.ReleaseName, Score: row.Score, Downloads: row.Downloads, HearingImpaired: row.HearingImpaired}
			if !row.UploadDate.IsZero() {
				result.UploadDate = new(NewInstant(row.UploadDate))
			}
			out.Results = append(out.Results, result)
		}
		// Provider errors can contain credential-bearing upstream URLs. The
		// bridge preserves its old warnings; v2 exposes only a bounded summary.
		if len(view.Warnings) > 0 {
			out.Warnings = append(out.Warnings, "One or more subtitle providers could not complete the search.")
		}
		return &SubtitleSearchOutput{Body: out}, nil
	})
}

func (reg *Registry) subtitleReadAccess(ctx context.Context) (catalogpkg.AccessFilter, *Problem) {
	if reg.deps.SubtitleReads == nil || reg.deps.CatalogAccess == nil {
		return catalogpkg.AccessFilter{}, NewProblem(TypeDependencyUnavailable, "Subtitle access is not configured.")
	}
	access, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return catalogpkg.AccessFilter{}, catalogProblem(err, "body")
	}
	return access, nil
}

func storedSubtitleView(row subtitles.DownloadedSubtitle) StoredSubtitle {
	language, _ := subtitles.CanonicalProviderLanguage(row.Provider, row.Language)
	return StoredSubtitle{ID: ID(strconv.Itoa(row.ID)), MediaFileID: ID(strconv.Itoa(row.MediaFileID)), Provider: row.Provider, Language: language, Format: string(row.Format), ReleaseName: row.ReleaseName, Score: row.Score, HearingImpaired: row.HearingImpaired, CreatedAt: NewInstant(row.CreatedAt)}
}
