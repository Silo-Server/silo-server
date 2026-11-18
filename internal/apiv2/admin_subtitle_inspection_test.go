package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeAdminSubtitleInspection struct {
	calls int
	input handlers.SubtitleProviderTestConfig
}

func (*fakeAdminSubtitleInspection) ListSubtitleProviderConfigs(context.Context) ([]subtitles.ProviderConfig, error) {
	return []subtitles.ProviderConfig{{ProviderName: "subdl", Enabled: true, HasAPIKey: true, APIKey: "PRIVATE", Username: "PRIVATE", Password: "PRIVATE", UpdatedAt: fixedTime()}, {ProviderName: "opensubtitles"}}, nil
}
func (f *fakeAdminSubtitleInspection) TestSubtitleProvider(_ context.Context, _ string, in handlers.SubtitleProviderTestConfig) handlers.SubtitleProviderTestView {
	f.calls++
	f.input = in
	return handlers.SubtitleProviderTestView{Error: "PRIVATE provider URL and credential"}
}
