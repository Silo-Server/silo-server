package apiv2

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type AdminMetadataTranslationService interface {
	TranslateAdminMetadata(context.Context, string, handlers.TranslateMetadataRequest, int) (*translation.Job, error)
	ListAdminMetadataTranslationJobs(context.Context, string) ([]translation.Job, error)
	CancelAdminMetadataTranslation(context.Context, string, int64) error
}
type AdminTranslateMetadataInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body struct {
		TargetLanguage  string `json:"target_language" minLength:"1" maxLength:"16"`
		IncludeChildren *bool  `json:"include_children,omitempty" nullable:"true" doc:"Defaults true for item targets; ignored for season and episode targets."`
		Force           bool   `json:"force,omitempty"`
	}
}
type AdminTranslationItemInput struct {
	ID string `path:"id" minLength:"1" maxLength:"512"`
}
type AdminTranslationCancelInput struct {
	ID    string `path:"id" minLength:"1" maxLength:"512"`
	JobID ID     `path:"job_id"`
}
type AdminMetadataTranslationJobs struct {
	Jobs []MetadataTranslationJob `json:"jobs" doc:"The newest 50 jobs for this content ID; empty, never null."`
}
type AdminMetadataTranslationJobsOutput struct{ Body AdminMetadataTranslationJobs }
type AdminMetadataTranslationAcceptedOutput struct {
	Location string `header:"Location"`
	Body     MetadataTranslationJob
}

func registerAdminCatalogTranslation(reg *Registry) {
	op := func(method, suffix, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/items/{id}/metadata-translation"+suffix, id, "admin-catalog", summary), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	start := op(http.MethodPost, "", "translateAdminItemMetadata", "Persist or reuse an active translation job for an authorized item.")
	start.DefaultStatus = http.StatusAccepted
	Register(reg, start, reg.translateAdminItemMetadata)
	Register(reg, op(http.MethodGet, "/jobs", "listAdminMetadataTranslationJobs", "Read the latest 50 translation jobs for an authorized item."), reg.listAdminMetadataTranslationJobs)
	cancel := op(http.MethodPost, "/jobs/{job_id}/cancel", "cancelAdminMetadataTranslation", "Request cancellation only for a job belonging to the authorized item.")
	cancel.DefaultStatus = http.StatusNoContent
	Register(reg, cancel, reg.cancelAdminMetadataTranslation)
}
func (reg *Registry) translateAdminItemMetadata(ctx context.Context, in *AdminTranslateMetadataInput) (*AdminMetadataTranslationAcceptedOutput, error) {
	if reg.deps.AdminMetadataTranslation == nil {
		return nil, unavailable("metadata translation")
	}
	b := in.Body
	job, err := reg.deps.AdminMetadataTranslation.TranslateAdminMetadata(ctx, in.ID, handlers.TranslateMetadataRequest{TargetLanguage: b.TargetLanguage, IncludeChildren: b.IncludeChildren, Force: b.Force}, claimsFrom(ctx).UserID)
	if err != nil {
		return nil, collectionProblem(err)
	}
	return &AdminMetadataTranslationAcceptedOutput{Location: Prefix + "/admin/items/" + url.PathEscape(in.ID) + "/metadata-translation/jobs", Body: translationJobOf(job)}, nil
}
func (reg *Registry) listAdminMetadataTranslationJobs(ctx context.Context, in *AdminTranslationItemInput) (*AdminMetadataTranslationJobsOutput, error) {
	if reg.deps.AdminMetadataTranslation == nil {
		return nil, unavailable("metadata translation")
	}
	jobs, err := reg.deps.AdminMetadataTranslation.ListAdminMetadataTranslationJobs(ctx, in.ID)
	if err != nil {
		return nil, collectionProblem(err)
	}
	out := &AdminMetadataTranslationJobsOutput{Body: AdminMetadataTranslationJobs{Jobs: make([]MetadataTranslationJob, 0, len(jobs))}}
	for i := range jobs {
		out.Body.Jobs = append(out.Body.Jobs, translationJobOf(&jobs[i]))
	}
	return out, nil
}
func (reg *Registry) cancelAdminMetadataTranslation(ctx context.Context, in *AdminTranslationCancelInput) (*struct{}, error) {
	if reg.deps.AdminMetadataTranslation == nil {
		return nil, unavailable("metadata translation")
	}
	id, p := in.JobID.positive("path.job_id")
	if p != nil {
		return nil, p
	}
	if err := reg.deps.AdminMetadataTranslation.CancelAdminMetadataTranslation(ctx, in.ID, int64(id)); err != nil {
		return nil, collectionProblem(err)
	}
	return nil, nil
}
