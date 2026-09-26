package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
func TestAdminSubtitleInspectionAccessAndProjection(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	fake := new(fakeAdminSubtitleInspection)
	deps.AdminSubtitleInspection = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/subtitle-providers"
	out := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if out.Code != 200 || strings.Contains(out.Body.String(), "PRIVATE") || strings.Count(out.Body.String(), "updated_at") != 1 || !strings.Contains(out.Body.String(), `"has_api_key":true`) {
		t.Fatalf("unsafe config: %d %s", out.Code, out.Body.String())
	}
	tested := do(t, h, http.MethodPost, path+"/subdl/test", `{"api_key":"draft-secret"}`, actingRequestAdmin)
	if tested.Code != 200 || strings.Contains(tested.Body.String(), "PRIVATE") || fake.calls != 1 || fake.input.APIKey != "draft-secret" {
		t.Fatalf("test: %d %s calls=%d", tested.Code, tested.Body.String(), fake.calls)
	}
	denied := do(t, h, http.MethodPost, path+"/subdl/test", `{}`, viewerHeaders())
	if denied.Code != 403 || fake.calls != 1 {
		t.Fatalf("non-admin reached provider: %d %d", denied.Code, fake.calls)
	}
	deps.AdminSubtitleInspection = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}
