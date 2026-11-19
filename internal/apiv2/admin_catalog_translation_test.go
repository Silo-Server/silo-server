package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/go-chi/chi/v5"
)

type fakeAdminTranslation struct {
	calls, userID int
	content       string
	jobID         int64
	request       handlers.TranslateMetadataRequest
	libraryID     int
	language      string
}

func (f *fakeAdminTranslation) TranslateAdminMetadata(_ context.Context, id string, req handlers.TranslateMetadataRequest, userID int) (*translation.Job, error) {
	f.calls++
	f.content, f.request, f.userID = id, req, userID
	return &translation.Job{ID: 7, ContentID: id, TargetLanguage: req.TargetLanguage, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}, nil
}
func (f *fakeAdminTranslation) ListAdminMetadataTranslationJobs(_ context.Context, id string) ([]translation.Job, error) {
	f.calls++
	f.content = id
	return nil, nil
}
func (f *fakeAdminTranslation) CancelAdminMetadataTranslation(_ context.Context, id string, jobID int64) error {
	f.calls++
	f.content, f.jobID = id, jobID
	return nil
}
func (f *fakeAdminTranslation) TranslateLibraryMetadata(_ context.Context, libraryID int, language string, userID int) (*translation.Job, error) {
	f.calls++
	f.libraryID, f.language, f.userID = libraryID, language, userID
	return &translation.Job{ID: 9, TargetKind: translation.TargetLibrary, ContentID: translation.LibraryContentID(libraryID), TargetLanguage: language, IncludeChildren: true, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}, nil
}
func (f *fakeAdminTranslation) ListLibraryMetadataTranslationJobs(_ context.Context, libraryID int) ([]translation.Job, error) {
	f.calls++
	f.libraryID = libraryID
	return nil, nil
}
func (f *fakeAdminTranslation) CancelLibraryMetadataTranslation(_ context.Context, libraryID int, jobID int64) error {
	f.calls++
	f.libraryID, f.jobID = libraryID, jobID
	return nil
}
func adminTranslationGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "id") != "item-1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func adminCatalogTranslationFixtureCases() []fixtureCase {
	path := Prefix + "/admin/items/item-1/metadata-translation"
	return []fixtureCase{
		{name: "admin_metadata_translation_queued", operationID: "translateAdminItemMetadata", method: "POST", path: path, body: `{"target_language":"de"}`, headers: bearer(memberToken), status: 202, schema: "#/components/schemas/MetadataTranslationJob", assertHeaders: []string{"Content-Type", "Location"}, scenario: "A delegated curator queues the persisted translation job for an authorized item."},
		{name: "admin_metadata_translation_jobs", operationID: "listAdminMetadataTranslationJobs", method: "GET", path: path + "/jobs", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminMetadataTranslationJobs", assertHeaders: []string{"Content-Type"}, scenario: "The recent-job list is bounded and empty arrays remain arrays."},
		{name: "admin_metadata_translation_cancel", operationID: "cancelAdminMetadataTranslation", method: "POST", path: path + "/jobs/7/cancel", headers: bearer(memberToken), status: 204, scenario: "Cancellation binds both authorized content and exact job identity."},
	}
}
