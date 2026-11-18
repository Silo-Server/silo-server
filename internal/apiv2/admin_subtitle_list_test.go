package apiv2

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminSubtitleList struct {
	calls  int
	filter handlers.AdminSubtitleListFilter
	after  *handlers.AdminSubtitlePageKey
	err    error
}

func (f *fakeAdminSubtitleList) ListAdminSubtitlesPage(_ context.Context, filter handlers.AdminSubtitleListFilter, after *handlers.AdminSubtitlePageKey, limit int) (handlers.AdminSubtitlePage, error) {
	f.calls++
	f.filter = filter
	f.after = after
	rows := []handlers.AdminDownloadedSubtitle{{ID: 9007199254740993, MediaFileID: 42, Provider: "upload", Language: "en", CreatedAt: fixedTime().Add(123456 * time.Nanosecond), DownloadedBy: new(2)}}
	if after != nil {
		rows = []handlers.AdminDownloadedSubtitle{}
	}
	return handlers.AdminSubtitlePage{Items: rows, Total: 3, Uploads: 1, ProviderDownloads: 2, HasMore: after == nil}, f.err
}
func adminSubtitleListFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_stored_subtitle_list", operationID: opListAdminStoredSubtitles, method: http.MethodGet, path: Prefix + "/admin/subtitles?limit=1", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminStoredSubtitleCollection", scenario: "Administrator live list carries exact string IDs, filtered counts and a scoped cursor."}}
}
