package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

const adminTaskProgressUnit = "items"
const adminTaskJobSchemaRef = "#/components/schemas/AdminTaskJob"

type AdminTaskJobsService interface {
	ListAdminTaskJobs(context.Context, string, time.Time, string, int) ([]*models.AdminJob, error)
	GetAdminTaskJob(context.Context, string) (*models.AdminJob, error)
	AdminTaskJobDownload(context.Context, *models.AdminJob) (string, *time.Time)
	AdminTaskJobPublicLinkSupported() bool
}
type AdminTaskJobCatalogResult struct {
	FormatVersion        int `json:"format_version"`
	SchemaVersion        int `json:"schema_version"`
	LibrariesExported    int `json:"libraries_exported"`
	ItemsExported        int `json:"items_exported"`
	CastExported         int `json:"cast_exported"`
	CrewExported         int `json:"crew_exported"`
	SeasonsExported      int `json:"seasons_exported"`
	EpisodesExported     int `json:"episodes_exported"`
	FilesExported        int `json:"files_exported"`
	LibraryLinksExported int `json:"library_links_exported"`
	ItemsCreated         int `json:"items_created"`
	FilesCreated         int `json:"files_created"`
}
type AdminTaskJobItemResult struct {
	NewFiles               int  `json:"new_files"`
	ArtworkCacheIncomplete bool `json:"artwork_cache_incomplete"`
	RequestedContentID     ID   `json:"requested_content_id"`
	RefreshContentID       ID   `json:"refresh_content_id"`
	DetailContentID        ID   `json:"detail_content_id,omitempty"`
	MatchedFiles           int  `json:"matched_files"`
}
type AdminTaskJobLibraryResult struct {
	TotalItems           int  `json:"total_items"`
	ItemsWithIDs         int  `json:"items_with_ids"`
	ItemsWithoutIDs      int  `json:"items_without_ids"`
	RefreshedOK          int  `json:"refreshed_ok"`
	RefreshedFailed      int  `json:"refreshed_failed"`
	PipelineOK           int  `json:"pipeline_ok"`
	PipelineFailed       int  `json:"pipeline_failed"`
	DeletedMediaFiles    int  `json:"deleted_media_files"`
	DeletedOrphanedItems int  `json:"deleted_orphaned_items"`
	ImageCleanupQueued   bool `json:"image_cleanup_queued"`
	ImageCleanupDirs     int  `json:"image_cleanup_dirs"`
	DeletedPrefixes      int  `json:"deleted_prefixes"`
	DeletedS3Objects     int  `json:"deleted_s3_objects"`
}
type AdminTaskJobStorageTransitionResult struct {
	Phase                 string `json:"phase" enum:"queued,checking_target,copying,verifying,committing,restart_pending,completed,failed,canceled" doc:"Safe transition phase. Internal progress messages and storage locations are omitted."`
	VerifiedObjects       int    `json:"verified_objects" minimum:"0" doc:"Objects whose destination content was verified during the current copy pass."`
	FailureCategory       string `json:"failure_category,omitempty" enum:"preparation_failed,target_check_failed,copy_failed,verification_failed,commit_failed,unknown" doc:"Safe failure category; present only for failed transitions."`
	ManualRestartRequired bool   `json:"manual_restart_required"`
}
type AdminTaskJob struct {
	LibraryID     *ID                        `json:"library_id,omitempty"`
	LibraryName   string                     `json:"library_name,omitempty"`
	LibraryResult *AdminTaskJobLibraryResult `json:"library_result,omitempty"`
	AdminJob
	LibraryIDs              []ID                                 `json:"library_ids"`
	SourceLabel             string                               `json:"source_label,omitempty"`
	CatalogResult           *AdminTaskJobCatalogResult           `json:"catalog_result,omitempty"`
	ItemResult              *AdminTaskJobItemResult              `json:"item_result,omitempty"`
	StorageTransitionResult *AdminTaskJobStorageTransitionResult `json:"storage_transition_result,omitempty"`
	ArtifactSizeBytes       int64                                `json:"artifact_size_bytes"`
	DownloadURL             string                               `json:"download_url,omitempty"`
	DownloadExpiresAt       *Instant                             `json:"download_expires_at,omitempty"`
	PublicURL               string                               `json:"public_url,omitempty"`
	PublicLinkSupported     bool                                 `json:"public_link_supported" doc:"Whether this server can mint a shareable seven-day link. False when exports are stored locally, because only storage-side presigning produces a URL usable off this server."`
}
type AdminTaskJobsInput struct {
	Kind   string `query:"kind"`
	Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"20"`
	Cursor string `query:"cursor"`
}
type AdminTaskJobInput struct {
	ID string `path:"id"`
}
type AdminTaskJobsOutput struct{ Body Collection[AdminTaskJob] }
type AdminTaskJobOutput struct {
	Status     int
	RetryAfter string `header:"Retry-After"`
	Body       AdminTaskJob
}
type adminTaskJobPosition struct {
	Requested time.Time `json:"requested"`
	ID        string    `json:"id"`
}

func registerAdminTaskJobs(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/jobs", "listAdminJobs", "admin-tasks", "List retained administrator jobs with bounded cursor pagination."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, in *AdminTaskJobsInput) (*AdminTaskJobsOutput, error) {
		return reg.listAdminTaskJobs(ctx, cursors, in)
	})
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/jobs/{id}", "getAdminJob", "admin-tasks", "Read a retained job. Administrators may read all jobs; item refresh owners may read their own safe result."), Class: ClassAuthenticated, ServiceBacked: true}, reg.getAdminTaskJob)
	cancel := humaOp("POST", Prefix+"/admin/jobs/{id}/cancel", "cancelAdminJob", "admin-tasks", "Request cancellation of a cancellable administrator job. Completed effects and verified storage-copy checkpoints are retained.")
	cancel.DefaultStatus = 202
	cancel.Errors = []int{409}
	cancel.Responses = map[string]*huma.Response{"200": {
		Description: "The job was already canceled.",
		Content:     map[string]*huma.MediaType{mediaTypeJSON: {Schema: &huma.Schema{Ref: adminTaskJobSchemaRef}}},
	}}
	Register(reg, Operation{Operation: cancel, Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyCoalescing}, reg.cancelAdminTaskJob)
}

func (reg *Registry) cancelAdminTaskJob(ctx context.Context, in *AdminTaskJobInput) (*AdminTaskJobOutput, error) {
	if reg.deps.AdminTaskJobs == nil {
		return nil, unavailable("admin jobs")
	}
	job, err := reg.deps.AdminTaskJobs.GetAdminTaskJob(ctx, in.ID)
	if errors.Is(err, adminjob.ErrJobNotFound) {
		return nil, NewProblem(TypeNotFound, "Job not found")
	}
	if err != nil {
		return nil, serviceProblem(err)
	}
	if job.JobType != adminjob.JobTypeStorageTransition {
		return nil, NewProblem(TypeJobNotCancelable, "This job cannot be canceled from this endpoint")
	}
	canceller, ok := reg.deps.AdminTaskJobs.(interface {
		RequestAdminTaskJobCancellation(context.Context, string) (*models.AdminJob, error)
	})
	if !ok {
		return nil, unavailable("admin job cancellation")
	}
	job, err = canceller.RequestAdminTaskJobCancellation(ctx, in.ID)
	if errors.Is(err, adminjob.ErrJobNotCancellable) {
		return nil, NewProblem(TypeJobNotCancelable, "This job cannot be canceled")
	}
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := &AdminTaskJobOutput{Status: http.StatusAccepted, Body: reg.adminTaskJobOf(ctx, job, true)}
	if job.Status == adminjob.StatusCancelled {
		out.Status = http.StatusOK
	} else {
		out.RetryAfter = "5"
	}
	return out, nil
}
func (reg *Registry) adminTaskJobOf(ctx context.Context, job *models.AdminJob, admin bool) AdminTaskJob {
	out := AdminTaskJob{AdminJob: adminJobOf(job), LibraryIDs: []ID{}}
	var transitionResult *AdminTaskJobStorageTransitionResult
	if job.JobType == adminjob.JobTypeStorageTransition {
		transitionResult = storageTransitionResultOf(job)
	}
	if job.ProgressTotal > 0 && job.ProgressCurrent >= 0 && job.ProgressCurrent <= job.ProgressTotal &&
		(job.JobType != adminjob.JobTypeStorageTransition ||
			(job.Status != adminjob.StatusQueued && transitionResult.Phase != "checking_target")) {
		out.Progress = &JobProgress{Current: job.ProgressCurrent, Total: job.ProgressTotal, Unit: adminTaskProgressUnit}
	}
	if job.JobType == adminjob.JobTypeItemRefresh && job.Status == adminjob.StatusCompleted {
		var result adminjob.ItemRefreshResult
		if json.Unmarshal(job.ResultPayload, &result) == nil && result.RequestedContentID != "" && result.RefreshContentID != "" {
			safe := AdminTaskJobItemResult{RequestedContentID: ID(result.RequestedContentID), RefreshContentID: ID(result.RefreshContentID), DetailContentID: ID(result.DetailContentID), MatchedFiles: result.MatchedFiles, ArtworkCacheIncomplete: result.ArtworkCacheWarning != ""}
			if result.ScanResult != nil {
				safe.NewFiles = result.ScanResult.New
			}
			out.ItemResult = &safe
		}
	}
	if !admin {
		return out
	}
	if transitionResult != nil {
		out.StorageTransitionResult = transitionResult
	}
	if job.JobType == adminjob.JobTypeLibraryRefresh || job.JobType == adminjob.JobTypeDeleteLibrary || job.JobType == adminjob.JobTypeImageCacheCleanup {
		var request struct {
			LibraryID   int64  `json:"library_id"`
			LibraryName string `json:"library_name"`
		}
		if json.Unmarshal(job.RequestPayload, &request) == nil {
			if request.LibraryID > 0 {
				out.LibraryID = new(IDFromInt(request.LibraryID))
			}
			out.LibraryName = request.LibraryName
		}
		if job.Status == adminjob.StatusCompleted {
			var result AdminTaskJobLibraryResult
			if json.Unmarshal(job.ResultPayload, &result) == nil {
				out.LibraryResult = &result
			}
		}
	}

	if job.JobType == adminjob.JobTypeTemplateBundleApply {
		out.AdminJob = adminCollectionJobOf(job)
	}
	if job.JobType == adminjob.JobTypeCatalogExport || job.JobType == adminjob.JobTypeCatalogImport {
		var request struct {
			LibraryIDs  []int64 `json:"library_ids"`
			SourceLabel string  `json:"source_label"`
		}
		if json.Unmarshal(job.RequestPayload, &request) == nil {
			for _, id := range request.LibraryIDs {
				out.LibraryIDs = append(out.LibraryIDs, IDFromInt(id))
			}
			out.SourceLabel = request.SourceLabel
		}
		if job.Status == adminjob.StatusCompleted {
			var result AdminTaskJobCatalogResult
			if json.Unmarshal(job.ResultPayload, &result) == nil {
				out.CatalogResult = &result
			}
		}
		out.ArtifactSizeBytes = job.ArtifactSizeBytes
		url, expiry := reg.deps.AdminTaskJobs.AdminTaskJobDownload(ctx, job)
		out.DownloadURL = url
		out.DownloadExpiresAt = instantPtr(expiry)
		out.PublicURL = job.PublicURL
		out.PublicLinkSupported = reg.deps.AdminTaskJobs.AdminTaskJobPublicLinkSupported()
	}
	return out
}

func storageTransitionResultOf(job *models.AdminJob) *AdminTaskJobStorageTransitionResult {
	safe := notifications.SafeStorageTransitionJob(job)
	var result AdminTaskJobStorageTransitionResult
	_ = json.Unmarshal(safe.ResultPayload, &result)
	return &result
}
func (reg *Registry) getAdminTaskJob(ctx context.Context, in *AdminTaskJobInput) (*AdminTaskJobOutput, error) {
	if reg.deps.AdminTaskJobs == nil {
		return nil, unavailable("admin jobs")
	}
	job, err := reg.deps.AdminTaskJobs.GetAdminTaskJob(ctx, in.ID)
	if errors.Is(err, adminjob.ErrJobNotFound) {
		return nil, NewProblem(TypeNotFound, "Job not found")
	}
	if err != nil {
		return nil, serviceProblem(err)
	}
	claims := claimsFrom(ctx)
	admin := claims != nil && claims.Role == models.RoleAdmin
	if !admin && (claims == nil || job.JobType != adminjob.JobTypeItemRefresh || job.CreatedByUserID != claims.UserID) {
		return nil, NewProblem(TypeNotFound, "Job not found")
	}
	out := &AdminTaskJobOutput{Body: reg.adminTaskJobOf(ctx, job, admin)}
	if !out.Body.Terminal {
		out.RetryAfter = "5"
	}
	return out, nil
}
func (reg *Registry) listAdminTaskJobs(ctx context.Context, cursors *Cursors, in *AdminTaskJobsInput) (*AdminTaskJobsOutput, error) {
	if reg.deps.AdminTaskJobs == nil {
		return nil, unavailable("admin jobs")
	}
	scope := CursorScope{OperationID: "listAdminJobs", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: in.Kind, Sort: "-requested_at", Tiebreaker: "id"}
	var position adminTaskJobPosition
	if in.Cursor != "" {
		if p := cursors.Decode(scope, in.Cursor, &position); p != nil {
			return nil, p
		}
	}
	rows, err := reg.deps.AdminTaskJobs.ListAdminTaskJobs(ctx, in.Kind, position.Requested, position.ID, in.Limit+1)
	if err != nil {
		return nil, serviceProblem(err)
	}
	next := ""
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		next, err = cursors.Encode(scope, adminTaskJobPosition{Requested: last.RequestedAt, ID: last.ID})
		if err != nil {
			return nil, serviceProblem(err)
		}
	}
	items := make([]AdminTaskJob, 0, len(rows))
	for _, job := range rows {
		items = append(items, reg.adminTaskJobOf(ctx, job, true))
	}
	return &AdminTaskJobsOutput{Body: Paginated(items, next)}, nil
}
