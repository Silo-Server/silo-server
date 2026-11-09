package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type AdminCatalogMatchService interface {
	SearchAdminItemMatches(context.Context, string, handlers.AdminMatchSearchRequest) (handlers.AdminMatchSearchResult, error)
	ApplyAdminItemMatch(context.Context, string, handlers.AdminMatchApplyRequest) (handlers.AdminMatchApplyResult, error)
	AdminItemMatchHints(context.Context, string, *int) (handlers.AdminMatchHints, error)
}
type AdminMatchHintsInput struct {
	ID        string `path:"id" minLength:"1" maxLength:"512"`
	LibraryID ID     `query:"library_id" doc:"Library to read; required for an item in several libraries."`
}
type AdminMatchHints struct {
	Title       string            `json:"title" doc:"Title the scanner parsed from the folder or file name; empty when it found none."`
	Year        int               `json:"year" doc:"Year the scanner parsed from the folder or file name; 0 when it found none."`
	ProviderIDs map[string]string `json:"provider_ids" doc:"Provider IDs tagged in the folder or file name, keyed by provider (tmdb, imdb, tvdb); empty, never null."`
}
type AdminMatchHintsOutput struct{ Body AdminMatchHints }
type AdminMatchSearchBody struct {
	Title       string            `json:"title,omitempty" maxLength:"2000"`
	Year        int               `json:"year,omitzero"`
	ImdbID      string            `json:"imdb_id,omitempty" maxLength:"512"`
	TmdbID      string            `json:"tmdb_id,omitempty" maxLength:"512"`
	TvdbID      string            `json:"tvdb_id,omitempty" maxLength:"512"`
	ProviderIDs map[string]string `json:"provider_ids,omitempty"`
	LibraryID   ID                `json:"library_id,omitempty"`
	Limit       int               `json:"limit,omitempty" default:"100" minimum:"1" maximum:"500"`
}
type AdminMatchSearchInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body AdminMatchSearchBody
}
type AdminMatchCandidates struct {
	Candidates []metadata.MatchCandidate `json:"candidates" doc:"Bounded provider-ranked candidates; empty, never null."`
	Truncated  bool                      `json:"truncated" doc:"More candidates were returned by providers; refine the search to narrow results."`
}
type AdminMatchSearchOutput struct{ Body AdminMatchCandidates }
type AdminMatchApplyInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body struct {
		ProviderIDs map[string]string `json:"provider_ids"`
		LibraryID   ID                `json:"library_id,omitempty"`
	}
}
type AdminMatchApplied struct {
	ContentID string `json:"content_id"`
	Updated   bool   `json:"updated"`
}
type AdminMatchApplyOutput struct{ Body AdminMatchApplied }

func matchLibraryID(id ID, location string) (*int, *Problem) {
	if id == "" {
		return nil, nil
	}
	value, p := id.positive(location)
	if p != nil {
		return nil, p
	}
	return new(value), nil
}
func registerAdminCatalogMatch(reg *Registry) {
	op := func(suffix, id string) Operation {
		return Operation{Operation: humaOp("POST", Prefix+"/admin/items/{id}/match/"+suffix, id, "admin-catalog", "Search or apply provider matches for an authorized catalog item."), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}
	}
	hints := Operation{Operation: humaOp("GET", Prefix+"/admin/items/{id}/match/hints", "getAdminItemMatchHints", "admin-catalog", "Read the title, year, and provider IDs the scanner parsed from an authorized catalog item's folder and file names."), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration, ServiceBacked: true}
	Register(reg, hints, func(ctx context.Context, in *AdminMatchHintsInput) (*AdminMatchHintsOutput, error) {
		if reg.deps.AdminCatalogMatch == nil {
			return nil, unavailable("item matching")
		}
		library, p := matchLibraryID(in.LibraryID, "query.library_id")
		if p != nil {
			return nil, p
		}
		result, err := reg.deps.AdminCatalogMatch.AdminItemMatchHints(ctx, in.ID, library)
		if err != nil {
			return nil, catalogProblem(err, "query.library_id")
		}
		return &AdminMatchHintsOutput{Body: AdminMatchHints{Title: result.Title, Year: result.Year, ProviderIDs: NonNilMap(result.ProviderIDs)}}, nil
	})
	Register(reg, op("search", "searchAdminItemMatches"), func(ctx context.Context, in *AdminMatchSearchInput) (*AdminMatchSearchOutput, error) {
		if reg.deps.AdminCatalogMatch == nil {
			return nil, unavailable("item matching")
		}
		b := in.Body
		library, p := matchLibraryID(b.LibraryID, "body.library_id")
		if p != nil {
			return nil, p
		}
		result, err := reg.deps.AdminCatalogMatch.SearchAdminItemMatches(ctx, in.ID, handlers.AdminMatchSearchRequest{Title: b.Title, Year: b.Year, ImdbID: b.ImdbID, TmdbID: b.TmdbID, TvdbID: b.TvdbID, ProviderIDs: b.ProviderIDs, LibraryID: library})
		if err != nil {
			return nil, collectionProblem(err)
		}
		limit := b.Limit
		truncated := len(result.Candidates) > limit
		rows := append([]metadata.MatchCandidate{}, result.Candidates[:min(len(result.Candidates), limit)]...)
		for i := range rows {
			if rows[i].ProviderIDs == nil {
				rows[i].ProviderIDs = map[string]string{}
			}
			if rows[i].Sources == nil {
				rows[i].Sources = []string{}
			}
			if rows[i].AgreementHints == nil {
				rows[i].AgreementHints = []string{}
			}
		}
		return &AdminMatchSearchOutput{Body: AdminMatchCandidates{Candidates: rows, Truncated: truncated}}, nil
	})
	Register(reg, op("apply", "applyAdminItemMatch"), func(ctx context.Context, in *AdminMatchApplyInput) (*AdminMatchApplyOutput, error) {
		if reg.deps.AdminCatalogMatch == nil {
			return nil, unavailable("item matching")
		}
		library, p := matchLibraryID(in.Body.LibraryID, "body.library_id")
		if p != nil {
			return nil, p
		}
		result, err := reg.deps.AdminCatalogMatch.ApplyAdminItemMatch(ctx, in.ID, handlers.AdminMatchApplyRequest{ProviderIDs: in.Body.ProviderIDs, LibraryID: library})
		if err != nil {
			return nil, collectionProblem(err)
		}
		return &AdminMatchApplyOutput{Body: AdminMatchApplied{ContentID: result.ContentID, Updated: result.Updated}}, nil
	})
}
