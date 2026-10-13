package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type AdminItemMetadataService interface {
	CreateItemMetadataRefresh(context.Context, string, adminjob.ItemRefreshMode, int) (*models.AdminJob, error)
	UpdateCatalogItemMetadata(context.Context, string, handlers.UpdateItemMetadataRequest, []string) (*catalogsvc.ItemDetail, error)
}
type AdminItemMetadataUpdate struct {
	Title            *string   `json:"title,omitempty" nullable:"true"`
	SortTitle        *string   `json:"sort_title,omitempty" nullable:"true"`
	OriginalTitle    *string   `json:"original_title,omitempty" nullable:"true"`
	Overview         *string   `json:"overview,omitempty" nullable:"true"`
	Tagline          *string   `json:"tagline,omitempty" nullable:"true"`
	ContentRating    *string   `json:"content_rating,omitempty" nullable:"true"`
	Year             *int      `json:"year,omitempty" nullable:"true"`
	Runtime          *int      `json:"runtime,omitempty" nullable:"true"`
	Genres           *[]string `json:"genres,omitempty" nullable:"true"`
	Studios          *[]string `json:"studios,omitempty" nullable:"true"`
	Networks         *[]string `json:"networks,omitempty" nullable:"true"`
	Countries        *[]string `json:"countries,omitempty" nullable:"true"`
	ReleaseDate      *string   `json:"release_date,omitempty" nullable:"true" doc:"Null clears the stored date"`
	FirstAirDate     *string   `json:"first_air_date,omitempty" nullable:"true" doc:"Null clears the stored date"`
	LastAirDate      *string   `json:"last_air_date,omitempty" nullable:"true" doc:"Null clears the stored date"`
	AirTime          *string   `json:"air_time,omitempty" nullable:"true" doc:"Null clears the stored time"`
	AirTimezone      *string   `json:"air_timezone,omitempty" nullable:"true" doc:"IANA time zone; null clears it"`
	AirDate          *string   `json:"air_date,omitempty" nullable:"true" doc:"Season or episode air date; null clears it"`
	Status           *string   `json:"status,omitempty" nullable:"true"`
	RatingIMDB       *float64  `json:"rating_imdb,omitempty" nullable:"true" doc:"Null clears it, along with any IMDb score a metadata plugin stored"`
	RatingTMDB       *float64  `json:"rating_tmdb,omitempty" nullable:"true" doc:"Null clears it, along with any TMDB score a metadata plugin stored"`
	RatingRTCritic   *int      `json:"rating_rt_critic,omitempty" nullable:"true" doc:"Null clears it, along with any Rotten Tomatoes critic score a metadata plugin stored"`
	RatingRTAudience *int      `json:"rating_rt_audience,omitempty" nullable:"true" doc:"Null clears it, along with any Rotten Tomatoes audience score a metadata plugin stored"`
	ImdbID           *string   `json:"imdb_id,omitempty" nullable:"true"`
	TmdbID           *string   `json:"tmdb_id,omitempty" nullable:"true"`
	TvdbID           *string   `json:"tvdb_id,omitempty" nullable:"true"`
	SeasonNumber     *int      `json:"season_number,omitempty" nullable:"true"`
	EpisodeNumber    *int      `json:"episode_number,omitempty" nullable:"true"`
	LockedFields     *[]int    `json:"locked_fields,omitempty" nullable:"true"`
}
type AdminItemMetadataUpdateInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body AdminItemMetadataUpdate
	// RawBody tells an explicit null from an omitted member, which Body
	// cannot: Huma decodes both to nil.
	RawBody []byte
}
type AdminItemMetadataRefreshInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body struct {
		Mode string `json:"mode,omitempty" enum:"quick,complete" default:"quick"`
	}
}

func registerAdminCatalogItemMetadata(reg *Registry) {
	op := func(method, suffix, id, summary string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/admin/items/{id}"+suffix, id, "admin-catalog", summary), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration, ServiceBacked: true, DemoRestricted: isMutatingMethod(method), RetrySafety: RetrySafetyNonRetryable}
	}
	refresh := op(http.MethodPost, "/refresh-metadata", "refreshAdminItemMetadata", "Persist an item metadata refresh job for an authorized item.")
	refresh.DefaultStatus = http.StatusAccepted
	Register(reg, refresh, reg.refreshAdminItemMetadata)
	Register(reg, op(http.MethodPatch, "/metadata", "updateAdminItemMetadata", "Apply partial metadata edits and return refreshed catalog detail."), reg.updateAdminItemMetadata)
}
func (reg *Registry) refreshAdminItemMetadata(ctx context.Context, in *AdminItemMetadataRefreshInput) (*AdminCatalogJobAcceptedOutput, error) {
	if reg.deps.AdminItemMetadata == nil {
		return nil, unavailable("item metadata")
	}
	claims := claimsFrom(ctx)
	job, err := reg.deps.AdminItemMetadata.CreateItemMetadataRefresh(ctx, in.ID, adminjob.ItemRefreshMode(in.Body.Mode), claims.UserID)
	if err != nil {
		return nil, collectionProblem(err)
	}
	// The job already exists; a failed acting-admin lookup only narrows what
	// the response shows.
	actingAdmin, _ := apimw.ActingAdmin(ctx)
	return &AdminCatalogJobAcceptedOutput{Location: Prefix + "/admin/jobs/" + job.ID, RetryAfter: "5", Body: reg.adminTaskJobOf(ctx, job, actingAdmin)}, nil
}
func (reg *Registry) updateAdminItemMetadata(ctx context.Context, in *AdminItemMetadataUpdateInput) (*CatalogItemDetailOutput, error) {
	if reg.deps.AdminItemMetadata == nil {
		return nil, unavailable("item metadata")
	}
	detail, err := reg.deps.AdminItemMetadata.UpdateCatalogItemMetadata(ctx, in.ID, handlers.UpdateItemMetadataRequest(in.Body), clearedItemMetadataFields(in.RawBody))
	if err != nil {
		return nil, collectionProblem(err)
	}
	// The operation is gated on metadata curation, so the editor gets every
	// stored rating back.
	detail.ViewerCurates = true
	return &CatalogItemDetailOutput{Body: catalogItemDetailOf(detail, reg.ratingSelection(ctx))}, nil
}

// clearedItemMetadataFields lists the clearable members the body sets to
// null. A null on any other member leaves it unchanged, like omission.
func clearedItemMetadataFields(raw []byte) []string {
	// Huma already validated the body; one that does not decode here has no
	// members to clear.
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	var cleared []string
	for _, field := range catalogsvc.ClearableMetadataFields {
		if value, ok := members[field]; ok && bytes.Equal(bytes.TrimSpace(value), jsonNull) {
			cleared = append(cleared, field)
		}
	}
	return cleared
}
