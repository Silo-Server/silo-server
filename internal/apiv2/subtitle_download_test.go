package apiv2

import (
	"context"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeSubtitleDownloads struct {
	calls   int
	access  catalogpkg.AccessFilter
	request subtitles.DownloadRequest
	err     error
}

func (f *fakeSubtitleDownloads) DownloadStoredSubtitle(_ context.Context, access catalogpkg.AccessFilter, req subtitles.DownloadRequest) (*subtitles.DownloadedSubtitle, error) {
	f.calls++
	f.access = access
	f.request = req
	return &subtitles.DownloadedSubtitle{ID: 9007199254740993, MediaFileID: req.MediaFileID, Provider: req.ProviderName, Language: req.Language, Format: subtitles.FormatSRT, S3Key: "PRIVATE_OBJECT", DownloadedBy: new(99), CreatedAt: fixedTime()}, f.err
}

const subtitleDownloadFixtureBody = `{"media_file_id":"42","provider":"example","subtitle_id":"opaque-provider-result","language":"en","release_name":"Synthetic","score":80,"hearing_impaired":false}`
