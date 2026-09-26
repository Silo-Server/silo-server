package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/markers"
	"net/http"
	"strconv"
)

type AdminMarkerContributionsService interface {
	ContributeAdminMarkers(context.Context, catalogsvc.AccessFilter, int, handlers.MarkerContributionRequest) ([]handlers.MarkerContributionOutcomeView, error)
	ListAdminMarkerContributions(context.Context, catalogsvc.AccessFilter, int, int, markers.ContributionPagePosition) ([]handlers.MarkerContributionView, bool, error)
}
type AdminMarkerContributionInput struct {
	FileID ID `path:"fileId"`
}
type AdminMarkerContributionRequest struct {
	Provider string   `json:"provider,omitempty" maxLength:"512"`
	Segments []string `json:"segments,omitempty" maxItems:"4" enum:"intro,credits,recap,preview"`
}
type AdminMarkerContributeInput struct {
	AdminMarkerContributionInput
	Body *AdminMarkerContributionRequest `required:"false"`
}
type AdminMarkerContributionListInput struct {
	AdminMarkerContributionInput
	LimitParam
	Cursor string `query:"cursor"`
}
type AdminMarkerContributionOutcome struct {
	Provider          string `json:"provider"`
	Segment           string `json:"segment"`
	Status            string `json:"status"`
	SubmissionID      string `json:"submission_id,omitempty"`
	Reason            string `json:"reason,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitzero"`
}
type AdminMarkerContributionOutcomes struct {
	Outcomes []AdminMarkerContributionOutcome `json:"outcomes"`
}
type AdminMarkerContributeOutput struct {
	Body AdminMarkerContributionOutcomes
}
type AdminMarkerContribution struct {
	ID               string   `json:"id"`
	MediaFileID      ID       `json:"media_file_id"`
	Provider         string   `json:"provider"`
	Segment          string   `json:"segment"`
	Source           string   `json:"source"`
	SubmittedStartMS *int64   `json:"submitted_start_ms,omitempty"`
	SubmittedEndMS   *int64   `json:"submitted_end_ms,omitempty"`
	VideoDurationMS  *int64   `json:"video_duration_ms,omitempty"`
	ContentHash      string   `json:"content_hash"`
	SubmissionID     *string  `json:"submission_id,omitempty"`
	Status           string   `json:"status"`
	HTTPStatus       *int     `json:"http_status,omitempty"`
	Error            *string  `json:"error,omitempty"`
	SubmittedAt      *Instant `json:"submitted_at,omitempty"`
	UpdatedAt        *Instant `json:"updated_at,omitempty"`
}
type AdminMarkerContributionListOutput struct {
	Body Collection[AdminMarkerContribution]
}

func (reg *Registry) adminContributionAccess(ctx context.Context) (catalogsvc.AccessFilter, error) {
	if reg.deps.CatalogAccess == nil {
		return catalogsvc.AccessFilter{}, unavailable("catalog access")
	}
	filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return filter, collectionProblem(err)
	}
	return filter, nil
}
func registerAdminCatalogMarkerContributions(reg *Registry) {
	op := func(method, path, id string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/files/{fileId}/"+path, id, "admin-catalog", "Manage marker contributions for an authorized file."), Class: ClassActingAdmin, ServiceBacked: true}
		if method == http.MethodPost {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodPost, "contribute", "contributeAdminFileMarkers"), func(ctx context.Context, in *AdminMarkerContributeInput) (*AdminMarkerContributeOutput, error) {
		if reg.deps.AdminMarkerContributions == nil {
			return nil, unavailable("marker contributions")
		}
		id, p := in.FileID.positive("path.fileId")
		if p != nil {
			return nil, p
		}
		access, err := reg.adminContributionAccess(ctx)
		if err != nil {
			return nil, err
		}
		body := handlers.MarkerContributionRequest{}
		if in.Body != nil {
			body.Provider = in.Body.Provider
			body.Segments = in.Body.Segments
		}
		rows, err := reg.deps.AdminMarkerContributions.ContributeAdminMarkers(ctx, access, id, body)
		if err != nil {
			return nil, collectionProblem(err)
		}
		out := &AdminMarkerContributeOutput{Body: AdminMarkerContributionOutcomes{Outcomes: make([]AdminMarkerContributionOutcome, 0, len(rows))}}
		for _, r := range rows {
			out.Body.Outcomes = append(out.Body.Outcomes, AdminMarkerContributionOutcome(r))
		}
		return out, nil
	})
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, "contributions", "listAdminFileMarkerContributions"), func(ctx context.Context, in *AdminMarkerContributionListInput) (*AdminMarkerContributionListOutput, error) {
		if reg.deps.AdminMarkerContributions == nil {
			return nil, unavailable("marker contributions")
		}
		id, p := in.FileID.positive("path.fileId")
		if p != nil {
			return nil, p
		}
		access, err := reg.adminContributionAccess(ctx)
		if err != nil {
			return nil, err
		}
		scope := adminPolicyListScope(ctx, "listAdminFileMarkerContributions", strconv.Itoa(id)+"/"+strconv.Itoa(in.Limit), "-updated_at,-id", "id")
		scope.Security += "/" + viewerScopeDigest(ctx)
		var pos markers.ContributionPagePosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
				return nil, p
			}
		}
		rows, more, err := reg.deps.AdminMarkerContributions.ListAdminMarkerContributions(ctx, access, id, in.Limit, pos)
		if err != nil {
			return nil, collectionProblem(err)
		}
		items := make([]AdminMarkerContribution, 0, len(rows))
		for _, r := range rows {
			item := AdminMarkerContribution{ID: r.ID, MediaFileID: IDFromInt(int64(r.MediaFileID)), Provider: r.Provider, Segment: r.Segment, Source: r.Source, SubmittedStartMS: r.SubmittedStartMS, SubmittedEndMS: r.SubmittedEndMS, VideoDurationMS: r.VideoDurationMS, ContentHash: r.ContentHash, SubmissionID: r.SubmissionID, Status: r.Status, HTTPStatus: r.HTTPStatus, Error: r.Error}
			if r.SubmittedAt != nil {
				item.SubmittedAt = new(NewInstant(*r.SubmittedAt))
			}
			if r.UpdatedAt != nil {
				item.UpdatedAt = new(NewInstant(*r.UpdatedAt))
			}
			items = append(items, item)
		}
		next := ""
		if more && len(rows) > 0 {
			last := rows[len(rows)-1]
			if last.UpdatedAt == nil {
				return nil, NewProblem(TypeInternalError, "Contribution cursor is unavailable.")
			}
			next, err = cursors.Encode(scope, markers.ContributionPagePosition{UpdatedAt: *last.UpdatedAt, ID: last.ID})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminMarkerContributionListOutput{Body: Paginated(items, next)}, nil
	})
}
