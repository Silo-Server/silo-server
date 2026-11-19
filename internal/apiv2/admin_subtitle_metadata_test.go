package apiv2

import (
	"context"
	"errors"
	"net/http"

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
func adminSubtitleMetadataFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_subtitle_metadata", operationID: "getAdminSubtitleMetadata", method: http.MethodGet, path: Prefix + "/admin/subtitles/9", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminSubtitleMetadata", scenario: "Canonical administrator metadata omits storage keys and supplies a strong revision validator."},
		{name: "admin_subtitle_metadata_precondition_required", operationID: "updateAdminSubtitleMetadata", method: http.MethodPatch, path: Prefix + "/admin/subtitles/9", headers: actingRequestAdmin, body: `{"release_name":"New name"}`, status: 428, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem", scenario: "Metadata update refuses an absent captured validator."}}
}
