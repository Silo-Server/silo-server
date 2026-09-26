package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeAdminSubtitleMetadata struct {
	row       subtitles.DownloadedSubtitle
	lookupErr error
	writes    int
	patch     subtitles.SubtitleMetadataPatch
	revision  *int64
	race      bool
	conflict  bool
}

func fixtureAdminSubtitleMetadata() *fakeAdminSubtitleMetadata {
	return &fakeAdminSubtitleMetadata{row: subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, Provider: "upload", Language: "en", Format: subtitles.FormatSRT, ReleaseName: "Original", HearingImpaired: true, CreatedAt: fixedTime(), DownloadedBy: new(2), Revision: 4, S3Key: "PRIVATE-object", ContentSHA256: "PRIVATE-digest"}}
}
func (f *fakeAdminSubtitleMetadata) GetAdminSubtitleMetadata(context.Context, int) (*subtitles.DownloadedSubtitle, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	row := f.row
	return &row, nil
}
func (f *fakeAdminSubtitleMetadata) UpdateAdminSubtitleMetadata(_ context.Context, _ int, patch subtitles.SubtitleMetadataPatch, revision *int64) (*subtitles.DownloadedSubtitle, error) {
	f.writes++
	f.patch = patch
	f.revision = revision
	if f.race {
		f.row.Revision++
		return nil, &subtitles.SubtitleRevisionConflict{Current: &f.row}
	}
	if f.conflict {
		return nil, subtitles.ErrSubtitleLanguageConflict
	}
	if revision != nil && *revision != f.row.Revision {
		return nil, errors.New("wrong durable revision")
	}
	if patch.Language != nil {
		f.row.Language = *patch.Language
	}
	if patch.ReleaseName != nil {
		f.row.ReleaseName = *patch.ReleaseName
	}
	if patch.HearingImpaired != nil {
		f.row.HearingImpaired = *patch.HearingImpaired
	}
	f.row.Revision++
	row := f.row
	return &row, nil
}
func TestAdminSubtitleMetadataGuardedContract(t *testing.T) {
	fake := fixtureAdminSubtitleMetadata()
	deps := requestDeps(fixtureRequests())
	deps.AdminSubtitleMetadata = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/subtitles/9"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || !strings.HasPrefix(tag, `"`) || strings.Contains(read.Body.String(), "PRIVATE") || !strings.Contains(read.Body.String(), `"media_file_id":"42"`) {
		t.Fatalf("read: %d %s %s", read.Code, tag, read.Body)
	}
	if res := do(t, h, http.MethodGet, path, "", with(actingRequestAdmin, "If-None-Match", tag)); res.Code != 304 || res.Body.Len() != 0 {
		t.Fatalf("conditional: %d %s", res.Code, res.Body)
	}
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"release_name":"next"}`, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"release_name":"next"}`, with(actingRequestAdmin, "If-Match", `"old"`)), TypePreconditionFailed)
	for _, body := range []string{`{}`, `{"language":null}`, `{"release_name":null}`, `{"hearing_impaired":null}`, `{"language":"bad-language-code"}`, `{"s3_key":"private"}`} {
		requireProblem(t, do(t, h, http.MethodPatch, path, body, with(actingRequestAdmin, "If-Match", tag)), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"release_name":"next"}`, viewerHeaders()), TypePermissionDenied)
	if fake.writes != 0 {
		t.Fatal("refused request reached metadata writer")
	}
	updated := do(t, h, http.MethodPatch, path, `{"release_name":"","hearing_impaired":false}`, with(actingRequestAdmin, "If-Match", tag))
	if updated.Code != 200 || fake.revision == nil || *fake.revision != 4 || fake.patch.Language != nil || fake.patch.ReleaseName == nil || *fake.patch.ReleaseName != "" || fake.patch.HearingImpaired == nil || *fake.patch.HearingImpaired || updated.Header().Get("ETag") == tag {
		t.Fatalf("patch: %d %s patch=%+v", updated.Code, updated.Body, fake.patch)
	}
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"release_name":"late"}`, with(actingRequestAdmin, "If-Match", tag)), TypePreconditionFailed)
	current := updated.Header().Get("ETag")
	fake.race = true
	raced := do(t, h, http.MethodPatch, path, `{"release_name":"raced"}`, with(actingRequestAdmin, "If-Match", current))
	requireProblem(t, raced, TypePreconditionFailed)
	if raced.Header().Get("ETag") == current || raced.Header().Get("ETag") == "" {
		t.Fatal("CAS conflict omitted current validator")
	}
	fake.race = false
	fake.conflict = true
	requireProblem(t, do(t, h, http.MethodPatch, path, `{"language":"fr"}`, with(actingRequestAdmin, "If-Match", "*")), TypeConflict)
	fake.conflict = false
	forced := do(t, h, http.MethodPatch, path, `{"release_name":"forced"}`, with(actingRequestAdmin, "If-Match", "*"))
	if forced.Code != 200 || fake.revision != nil {
		t.Fatalf("explicit wildcard: %d %s", forced.Code, forced.Body)
	}
	deps.AdminSubtitleMetadata = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}
func adminSubtitleMetadataFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_subtitle_metadata", operationID: "getAdminSubtitleMetadata", method: http.MethodGet, path: Prefix + "/admin/subtitles/9", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminSubtitleMetadata", scenario: "Canonical administrator metadata omits storage keys and supplies a strong revision validator."},
		{name: "admin_subtitle_metadata_precondition_required", operationID: "updateAdminSubtitleMetadata", method: http.MethodPatch, path: Prefix + "/admin/subtitles/9", headers: actingRequestAdmin, body: `{"release_name":"New name"}`, status: 428, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem", scenario: "Metadata update refuses an absent captured validator."}}
}

func TestAdminSubtitleMetadataDependencyProblems(t *testing.T) {
	fake := fixtureAdminSubtitleMetadata()
	deps := requestDeps(fixtureRequests())
	deps.AdminSubtitleMetadata = fake
	h := newTestHandler(t, deps)
	for _, input := range []struct {
		err     error
		problem ProblemType
	}{{handlers.ErrAdminSubtitleMetadataUnavailable, TypeDependencyUnavailable}, {subtitles.ErrSubtitleNotFound, TypeNotFound}, {errors.New("PRIVATE database"), TypeInternalError}} {
		fake.lookupErr = input.err
		out := do(t, h, http.MethodGet, Prefix+"/admin/subtitles/9", "", actingRequestAdmin)
		requireProblem(t, out, input.problem)
		if strings.Contains(out.Body.String(), "PRIVATE") {
			t.Fatal("private error leaked")
		}
	}
}
