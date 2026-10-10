package apiv2

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type AdminMetadataTranslationService interface {
	TranslateAdminMetadata(context.Context, string, handlers.TranslateMetadataRequest, int) (*translation.Job, error)
	ListAdminMetadataTranslationJobs(context.Context, string) ([]translation.Job, error)
	CancelAdminMetadataTranslation(context.Context, string, int64) error
	TranslateLibraryMetadata(context.Context, int, string, int) (*translation.Job, error)
	ListLibraryMetadataTranslationJobs(context.Context, int) ([]translation.Job, error)
	CancelLibraryMetadataTranslation(context.Context, int, int64) error
}

// AdminTranslateLibraryInput starts a library prewarm.
type AdminTranslateLibraryInput struct {
	ID   ID `path:"library_id" doc:"The library" example:"1"`
	Body struct {
		TargetLanguage string `json:"target_language" minLength:"1" maxLength:"16" doc:"BCP 47 tag of the language to fill in" example:"de"`
	}
}

// AdminLibraryTranslationJobsInput names the library whose prewarm jobs to list.
type AdminLibraryTranslationJobsInput struct {
	ID ID `path:"library_id" doc:"The library" example:"1"`
}

// AdminLibraryTranslationCancelInput names a library prewarm job.
type AdminLibraryTranslationCancelInput struct {
	ID    ID `path:"library_id" doc:"The library" example:"1"`
	JobID ID `path:"job_id"`
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

	libraryOp := func(method, suffix, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/libraries/{library_id}/metadata-translation"+suffix, id, "admin-catalog", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: isMutatingMethod(method)}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	startLibrary := libraryOp(http.MethodPost, "", "translateLibraryMetadata", "Prewarm a library: persist or reuse an active job that translates the missing descriptions of every item in the library, with their seasons and episodes, into a language. Existing provider, manual and AI text is kept. Progress counts items.")
	startLibrary.DefaultStatus = http.StatusAccepted
	Register(reg, startLibrary, reg.translateLibraryMetadata)
	Register(reg, libraryOp(http.MethodGet, "/jobs", "listLibraryMetadataTranslationJobs", "Read the latest 50 prewarm jobs of a library."), reg.listLibraryMetadataTranslationJobs)
	cancelLibrary := libraryOp(http.MethodPost, "/jobs/{job_id}/cancel", "cancelLibraryMetadataTranslation", "Request cancellation only for a prewarm job of the library.")
	cancelLibrary.DefaultStatus = http.StatusNoContent
	Register(reg, cancelLibrary, reg.cancelLibraryMetadataTranslation)
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

func (reg *Registry) translateLibraryMetadata(ctx context.Context, in *AdminTranslateLibraryInput) (*AdminMetadataTranslationAcceptedOutput, error) {
	if reg.deps.AdminMetadataTranslation == nil {
		return nil, unavailable("metadata translation")
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	job, err := reg.deps.AdminMetadataTranslation.TranslateLibraryMetadata(ctx, id, in.Body.TargetLanguage, claimsFrom(ctx).UserID)
	if err != nil {
		return nil, collectionProblem(err)
	}
	return &AdminMetadataTranslationAcceptedOutput{Location: Prefix + "/admin/libraries/" + strconv.Itoa(id) + "/metadata-translation/jobs", Body: translationJobOf(job)}, nil
}

func (reg *Registry) listLibraryMetadataTranslationJobs(ctx context.Context, in *AdminLibraryTranslationJobsInput) (*AdminMetadataTranslationJobsOutput, error) {
	if reg.deps.AdminMetadataTranslation == nil {
		return nil, unavailable("metadata translation")
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	jobs, err := reg.deps.AdminMetadataTranslation.ListLibraryMetadataTranslationJobs(ctx, id)
	if err != nil {
		return nil, collectionProblem(err)
	}
	out := &AdminMetadataTranslationJobsOutput{Body: AdminMetadataTranslationJobs{Jobs: make([]MetadataTranslationJob, 0, len(jobs))}}
	for i := range jobs {
		out.Body.Jobs = append(out.Body.Jobs, translationJobOf(&jobs[i]))
	}
	return out, nil
}

func (reg *Registry) cancelLibraryMetadataTranslation(ctx context.Context, in *AdminLibraryTranslationCancelInput) (*struct{}, error) {
	if reg.deps.AdminMetadataTranslation == nil {
		return nil, unavailable("metadata translation")
	}
	id, p := libraryID(in.ID)
	if p != nil {
		return nil, p
	}
	jobID, p := in.JobID.positive("path.job_id")
	if p != nil {
		return nil, p
	}
	if err := reg.deps.AdminMetadataTranslation.CancelLibraryMetadataTranslation(ctx, id, int64(jobID)); err != nil {
		return nil, collectionProblem(err)
	}
	return nil, nil
}
