package apiv2

import (
	"context"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeSubtitleReads struct {
	fileID int
	access catalogpkg.AccessFilter
	err    error
}

func (f *fakeSubtitleReads) ListStoredSubtitles(_ context.Context, access catalogpkg.AccessFilter, id int) ([]subtitles.DownloadedSubtitle, error) {
	f.fileID, f.access = id, access
	return []subtitles.DownloadedSubtitle{{ID: 7, MediaFileID: id, Provider: "example", Language: "en", Format: subtitles.FormatSRT, S3Key: "PRIVATE_OBJECT", DownloadedBy: new(99), CreatedAt: fixedTime()}}, f.err
}
func (f *fakeSubtitleReads) SearchSubtitles(_ context.Context, access catalogpkg.AccessFilter, id int, _ []string) (*subtitles.SearchResponse, error) {
	f.fileID, f.access = id, access
	return &subtitles.SearchResponse{Results: []subtitles.SubtitleResult{{ID: "opaque-result", Provider: "example", Language: "en", Format: subtitles.FormatSRT}}, Warnings: []string{"PRIVATE_PROVIDER_ERROR"}}, f.err
}
