package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/danielgtaylor/huma/v2"
)

type AdminCollectionExtrasService interface {
	PreviewAdminCollection(context.Context, handlers.AdminCollectionPreviewRequest) (handlers.AdminCollectionPreview, error)
	SyncAdminCollection(context.Context, string) (*models.LibraryCollectionSyncRun, error)
	ImportAdminMDBList(context.Context, handlers.AdminCollectionImportMDBList) (handlers.AdminCollectionImportResult, error)
	ImportAdminTMDB(context.Context, handlers.AdminCollectionImportTMDB) (handlers.AdminCollectionImportResult, error)
	ImportAdminTrakt(context.Context, handlers.AdminCollectionImportTrakt) (handlers.AdminCollectionImportResult, error)
	ListAdminCollectionTemplates(context.Context) (templates.BundleCatalog, error)
	AdminCollectionTemplateCatalog(context.Context) templates.Catalog
	ApplyAdminCollectionTemplate(context.Context, string, handlers.AdminCollectionTemplateApply) (handlers.AdminCollectionTemplateResult, error)
	QueueAdminCollectionTemplate(context.Context, string, handlers.AdminCollectionTemplateApply, int) (*models.AdminJob, error)
	UploadAdminCollectionArtwork(context.Context, string, string, []byte) error
	SetAdminCollectionArtworkSource(context.Context, string, string, string) error
	DeleteAdminCollectionArtwork(context.Context, string, string) error
}
type AdminCollectionMember struct {
	Title        string `json:"title,omitempty" doc:"Catalog title when the item is available."`
	CollectionID ID     `json:"collection_id"`
	MediaItemID  ID     `json:"media_item_id"`
	Position     int    `json:"position"`
}
type AdminCollectionMembersOutput struct {
	Body Collection[AdminCollectionMember]
}
type AdminCollectionMemberInput struct {
	ID     ID `path:"id"`
	ItemID ID `path:"item_id"`
	Body   struct {
		Position int `json:"position"`
	}
}
type AdminCollectionImageInput struct {
	ID   ID     `path:"id"`
	Type string `query:"type" enum:"poster,backdrop" required:"true"`
}
type AdminCollectionArtworkForm struct {
	Image     huma.FormFile `form:"image" contentType:"image/jpeg,image/png,image/webp" required:"false"`
	SourceURL string        `form:"source_url" required:"false"`
}
type AdminCollectionArtworkInput struct {
	ID      ID `path:"id"`
	RawBody huma.MultipartFormFiles[AdminCollectionArtworkForm]
}
type AdminCollectionBackdropForm AdminCollectionArtworkForm
type AdminCollectionBackdropInput struct {
	ID      ID `path:"id"`
	RawBody huma.MultipartFormFiles[AdminCollectionBackdropForm]
}
type AdminMDBListInput struct {
	Body    AdminMDBListImport
	RawBody []byte
}
type AdminTMDBInput struct {
	Body    AdminTMDBImport
	RawBody []byte
}
type AdminTraktInput struct {
	Body    AdminTraktImport
	RawBody []byte
}
type AdminCollectionImportOutput struct {
	Location string `header:"Location"`
	Body     AdminCollectionImportResult
}
type AdminCollectionSyncOutput struct{ Body AdminCollectionSyncRun }
type AdminTemplateCatalogOutput struct{ Body templates.Catalog }
type AdminBundleCatalogOutput struct{ Body templates.BundleCatalog }
type AdminTemplateInput struct {
	BundleID string `path:"bundle_id"`
	Body     AdminTemplateApply
	RawBody  []byte
}
type AdminTemplateOutput struct{ Body AdminTemplateResult }
type AdminCollectionPreviewItem struct {
	ContentID ID     `json:"content_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	PosterURL string `json:"poster_url,omitempty"`
}
type AdminCollectionPreviewOutput struct {
	Body struct {
		Collection[AdminCollectionPreviewItem]
		Total int `json:"total"`
	}
}

func (reg *Registry) adminCollectionExtras() (AdminCollectionExtrasService, *Problem) {
	s, ok := reg.deps.AdminCollections.(AdminCollectionExtrasService)
	if !ok {
		return nil, unavailable("admin collections")
	}
	return s, nil
}
func registerAdminCollectionExtras(reg *Registry) {
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/{id}/items", "getAdminCollectionItems", "Page manual membership with durable signed continuation.", false), reg.getAdminCollectionItems)
	add := adminCollectionOperation(http.MethodPut, "/admin/collections/{id}/items/{item_id}", "addAdminCollectionItem", "Add a catalog item if absent; existing positions are preserved.", false)
	add.DefaultStatus = http.StatusNoContent
	Register(reg, add, reg.addAdminCollectionItem)
	Register(reg, adminCollectionOperation(http.MethodDelete, "/admin/collections/{id}/items/{item_id}", "removeAdminCollectionItem", "Remove a manual membership.", false), reg.removeAdminCollectionItem)
	Register(reg, adminCollectionOperation(http.MethodDelete, "/admin/collections/{id}/image", "deleteAdminCollectionImage", "Clear poster or backdrop artwork.", false), reg.deleteAdminCollectionImage)
	poster := adminCollectionOperation(http.MethodPut, "/admin/collections/{id}/poster", "uploadAdminCollectionPoster", "Upload bounded poster artwork or download its source after definition save.", false)
	poster.MaxBodyBytes = maxPosterBytes + posterFormOverhead
	Register(reg, poster, func(ctx context.Context, in *AdminCollectionArtworkInput) (*AdminCollectionOutput, error) {
		return reg.uploadAdminCollectionArtwork(ctx, in.ID, in.RawBody.Data(), "poster")
	})
	backdrop := adminCollectionOperation(http.MethodPut, "/admin/collections/{id}/backdrop", "uploadAdminCollectionBackdrop", "Upload bounded backdrop artwork or download its source after definition save.", false)
	backdrop.MaxBodyBytes = maxPosterBytes + posterFormOverhead
	Register(reg, backdrop, func(ctx context.Context, in *AdminCollectionBackdropInput) (*AdminCollectionOutput, error) {
		return reg.uploadAdminCollectionArtwork(ctx, in.ID, (*AdminCollectionArtworkForm)(in.RawBody.Data()), "backdrop")
	})
	preview := adminCollectionOperation(http.MethodPost, "/admin/collections/preview", "previewAdminCollection", "Preview an administrator smart collection query.", false)
	preview.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, preview, reg.previewAdminCollection)
	Register(reg, adminCollectionOperation(http.MethodPost, "/admin/collections/{id}/sync", "syncAdminCollection", "Synchronize an imported collection once. Automatic retry is unsafe.", false), reg.syncAdminCollection)
	mdb := adminCollectionOperation(http.MethodPost, "/admin/collections/import/mdblist", "importAdminMDBList", "Import an MDBList collection.", false)
	mdb.DefaultStatus = http.StatusCreated
	Register(reg, mdb, reg.importAdminMDBList)
	tmdb := adminCollectionOperation(http.MethodPost, "/admin/collections/import/tmdb", "importAdminTMDB", "Import a TMDB collection.", false)
	tmdb.DefaultStatus = http.StatusCreated
	Register(reg, tmdb, reg.importAdminTMDB)
	trakt := adminCollectionOperation(http.MethodPost, "/admin/collections/import/trakt", "importAdminTrakt", "Import a Trakt collection.", false)
	trakt.DefaultStatus = http.StatusCreated
	Register(reg, trakt, reg.importAdminTrakt)
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/templates", "listAdminCollectionTemplates", "List supported collection templates.", false), func(ctx context.Context, _ *struct{}) (*AdminTemplateCatalogOutput, error) {
		s, p := reg.adminCollectionExtras()
		if p != nil {
			return nil, p
		}
		return &AdminTemplateCatalogOutput{Body: s.AdminCollectionTemplateCatalog(ctx)}, nil
	})
	Register(reg, adminCollectionOperation(http.MethodGet, "/admin/collections/template-bundles", "listAdminCollectionTemplateBundles", "List collection template bundles.", false), func(ctx context.Context, _ *struct{}) (*AdminBundleCatalogOutput, error) {
		s, p := reg.adminCollectionExtras()
		if p != nil {
			return nil, p
		}
		v, e := s.ListAdminCollectionTemplates(ctx)
		if e != nil {
			return nil, adminCollectionError(e)
		}
		return &AdminBundleCatalogOutput{Body: v}, nil
	})
	Register(reg, adminCollectionOperation(http.MethodPost, "/admin/collections/template-bundles/{bundle_id}/apply", "applyAdminCollectionTemplateBundle", "Preview or synchronously apply a template bundle, preserving featured-section behavior.", false), reg.applyAdminTemplate)
	job := adminCollectionOperation(http.MethodPost, "/admin/collections/template-bundles/{bundle_id}/apply-job", "startAdminCollectionTemplateBundleJob", "Queue durable template application; poll its collection-job Location.", false)
	job.DefaultStatus = http.StatusAccepted
	Register(reg, job, reg.queueAdminTemplate)
	poll := adminCollectionOperation(http.MethodGet, "/admin/collection-jobs/{job_id}", "getAdminCollectionJob", "Poll a template-application job. Completed jobs are retained for at least 24 hours.", false)
	poll.Conditional = true
	Register(reg, poll, reg.getAdminCollectionJob)
}
func (reg *Registry) getAdminCollectionItems(ctx context.Context, in *PersonalCollectionItemsInput) (*AdminCollectionMembersOutput, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	c := NewCursors(reg.deps.CursorSecret)
	scope := collectionPageScope(ctx, "getAdminCollectionItems", string(in.ID))
	opts, p := collectionPageOptions(c, scope, in.Cursor, in.Limit)
	if p != nil {
		return nil, p
	}
	v, e := reg.deps.AdminCollections.AdminCollectionItemsPage(ctx, string(in.ID), opts)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	items := make([]AdminCollectionMember, 0, len(v.Items))
	for _, item := range v.Items {
		items = append(items, AdminCollectionMember{CollectionID: ID(item.CollectionID), MediaItemID: ID(item.MediaItemID), Title: v.Titles[item.MediaItemID], Position: item.Position})
	}
	next := ""
	if v.HasMore && len(v.Items) > 0 {
		last := v.Items[len(v.Items)-1]
		next, e = collectionPageNext(c, scope, true, v.Revision, &userstore.CollectionItemPosition{Position: last.Position, MediaItemID: last.MediaItemID}, nil)
		if e != nil {
			return nil, e
		}
	}
	return &AdminCollectionMembersOutput{Body: Paginated(items, next)}, nil
}
func (reg *Registry) addAdminCollectionItem(ctx context.Context, in *AdminCollectionMemberInput) (*struct{}, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	if e := reg.deps.AdminCollections.AddAdminCollectionItem(ctx, string(in.ID), string(in.ItemID), in.Body.Position); e != nil {
		return nil, adminCollectionError(e)
	}
	return nil, nil
}
func (reg *Registry) removeAdminCollectionItem(ctx context.Context, in *PersonalCollectionItemInput) (*struct{}, error) {
	if reg.deps.AdminCollections == nil {
		return nil, unavailable("admin collections")
	}
	if e := reg.deps.AdminCollections.RemoveAdminCollectionItem(ctx, string(in.ID), string(in.ItemID)); e != nil {
		return nil, adminCollectionError(e)
	}
	return nil, nil
}
func (reg *Registry) deleteAdminCollectionImage(ctx context.Context, in *AdminCollectionImageInput) (*struct{}, error) {
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	if e := s.DeleteAdminCollectionArtwork(ctx, string(in.ID), in.Type); e != nil {
		return nil, adminCollectionError(e)
	}
	return nil, nil
}
func (reg *Registry) uploadAdminCollectionArtwork(ctx context.Context, id ID, form *AdminCollectionArtworkForm, kind string) (*AdminCollectionOutput, error) {
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	if form == nil || (!form.Image.IsSet && form.SourceURL == "") {
		return nil, NewProblem(TypeValidationFailed, "Supply an image or source_url.")
	}
	if form.Image.IsSet && form.SourceURL != "" {
		return nil, NewProblem(TypeValidationFailed, "Supply one artwork source.")
	}
	var e error
	if form.SourceURL != "" {
		e = s.SetAdminCollectionArtworkSource(ctx, string(id), kind, form.SourceURL)
	} else {
		if form.Image.Size > maxPosterBytes {
			return nil, NewProblem(TypePayloadTooLarge, "Artwork exceeds 10 MiB.")
		}
		data, err := io.ReadAll(io.LimitReader(form.Image, maxPosterBytes+1))
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Could not read artwork.")
		}
		if len(data) > maxPosterBytes {
			return nil, NewProblem(TypePayloadTooLarge, "Artwork exceeds 10 MiB.")
		}
		e = s.UploadAdminCollectionArtwork(ctx, string(id), kind, data)
	}
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return reg.getAdminCollection(ctx, &AdminCollectionIDInput{ID: id})
}
func (reg *Registry) previewAdminCollection(ctx context.Context, in *PersonalCollectionPreviewInput) (*AdminCollectionPreviewOutput, error) {
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	v, e := s.PreviewAdminCollection(ctx, handlers.AdminCollectionPreviewRequest{QueryDefinition: in.Body.QueryDefinition, Limit: in.Body.Limit})
	if e != nil {
		return nil, adminCollectionError(e)
	}
	items := make([]AdminCollectionPreviewItem, 0, len(v.Items))
	for _, i := range v.Items {
		items = append(items, AdminCollectionPreviewItem{ContentID: ID(i.ContentID), Title: i.Title, Type: i.Type, PosterURL: i.PosterURL})
	}
	out := &AdminCollectionPreviewOutput{}
	out.Body.Collection = NewCollection(items)
	out.Body.Total = v.Total
	return out, nil
}
func (reg *Registry) syncAdminCollection(ctx context.Context, in *AdminCollectionIDInput) (*AdminCollectionSyncOutput, error) {
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	v, e := s.SyncAdminCollection(ctx, string(in.ID))
	if e != nil {
		return nil, adminCollectionError(e)
	}
	run := adminCollectionSyncRunOf(v)
	if run == nil {
		return nil, NewProblem(TypeInternalError, "Sync returned no result.")
	}
	return &AdminCollectionSyncOutput{Body: *run}, nil
}
func adminImportOutput(v handlers.AdminCollectionImportResult) *AdminCollectionImportOutput {
	return &AdminCollectionImportOutput{Location: Prefix + "/admin/collections/" + v.Collection.ID, Body: adminCollectionImportResultOf(v)}
}
func (reg *Registry) importAdminMDBList(ctx context.Context, in *AdminMDBListInput) (*AdminCollectionImportOutput, error) {

	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	cmd, p := in.Body.command()
	if p != nil {
		return nil, p
	}
	v, e := s.ImportAdminMDBList(ctx, cmd)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return adminImportOutput(v), nil
}
func (reg *Registry) importAdminTMDB(ctx context.Context, in *AdminTMDBInput) (*AdminCollectionImportOutput, error) {

	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	cmd, p := in.Body.command()
	if p != nil {
		return nil, p
	}
	v, e := s.ImportAdminTMDB(ctx, cmd)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return adminImportOutput(v), nil
}
func (reg *Registry) importAdminTrakt(ctx context.Context, in *AdminTraktInput) (*AdminCollectionImportOutput, error) {

	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	cmd, p := in.Body.command()
	if p != nil {
		return nil, p
	}
	v, e := s.ImportAdminTrakt(ctx, cmd)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return adminImportOutput(v), nil
}
func (reg *Registry) applyAdminTemplate(ctx context.Context, in *AdminTemplateInput) (*AdminTemplateOutput, error) {

	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(in.RawBody, &members) == nil {
		if p := rejectNonNullableNulls(members["featured"], nil); p != nil {
			return nil, p
		}
	}
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	cmd, p := in.Body.command()
	if p != nil {
		return nil, p
	}
	v, e := s.ApplyAdminCollectionTemplate(ctx, in.BundleID, cmd)
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminTemplateOutput{Body: adminTemplateResultOf(v)}, nil
}
func adminCollectionJobLocation(id string) string { return Prefix + "/admin/collection-jobs/" + id }
func adminCollectionJobOf(job *models.AdminJob) AdminJob {
	out := adminJobOf(job)
	out.Cancelable = false
	if job.ProgressTotal > 0 && job.ProgressCurrent >= 0 && job.ProgressCurrent <= job.ProgressTotal {
		out.Progress = &JobProgress{Current: job.ProgressCurrent, Total: job.ProgressTotal, Unit: "templates"}
	}
	if job.Status == adminjob.StatusCompleted {
		var result handlers.AdminCollectionTemplateResult
		if json.Unmarshal(job.ResultPayload, &result) == nil {
			out.TemplateResult = new(adminTemplateResultOf(result))
		}
	}
	return out
}
func (reg *Registry) queueAdminTemplate(ctx context.Context, in *AdminTemplateInput) (*AdminJobAcceptedOutput, error) {

	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(in.RawBody, &members) == nil {
		if p := rejectNonNullableNulls(members["featured"], nil); p != nil {
			return nil, p
		}
	}
	s, p := reg.adminCollectionExtras()
	if p != nil {
		return nil, p
	}
	cmd, p := in.Body.command()
	if p != nil {
		return nil, p
	}
	job, e := s.QueueAdminCollectionTemplate(ctx, in.BundleID, cmd, claimsFrom(ctx).UserID)
	if _, ok := errors.AsType[*adminjob.ActiveJobConflictError](e); ok {
		return nil, NewProblem(TypeConflict, "A collection template application is already active.")
	}
	if e != nil {
		return nil, adminCollectionError(e)
	}
	return &AdminJobAcceptedOutput{Location: adminCollectionJobLocation(job.ID), RetryAfter: "5", Body: adminCollectionJobOf(job)}, nil
}
func (reg *Registry) getAdminCollectionJob(ctx context.Context, in *LibraryJobInput) (*LibraryJobOutput, error) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeNotFound, "Job not found.")
	}
	if reg.deps.LibraryJobs == nil {
		return nil, unavailable("collection jobs")
	}
	job, e := reg.deps.LibraryJobs.GetByID(ctx, in.JobID)
	if errors.Is(e, adminjob.ErrJobNotFound) {
		return nil, NewProblem(TypeNotFound, "Job not found.")
	}
	if e != nil {
		return nil, serviceProblem(e)
	}
	if job.JobType != adminjob.JobTypeTemplateBundleApply || (claims.Role != models.RoleAdmin && job.CreatedByUserID != claims.UserID) {
		return nil, NewProblem(TypeNotFound, "Job not found.")
	}
	out := &LibraryJobOutput{Body: adminCollectionJobOf(job)}
	data, _ := json.Marshal(out.Body)
	sum := sha256.Sum256(data)
	tag := EntityTag{Opaque: hex.EncodeToString(sum[:])}
	out.ETag = tag.String()
	if !out.Body.Terminal {
		out.RetryAfter = "5"
	}
	if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
		return nil, p
	} else if matched {
		return NotModified(out, tag), nil
	}
	return out, nil
}
