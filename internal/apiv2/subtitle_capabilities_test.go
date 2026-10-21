package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeSubtitleCapabilities struct{ calls int }

func (f *fakeSubtitleCapabilities) SubtitleProviderStatus() handlers.SubtitleProviderStatusView {
	f.calls++
	return handlers.SubtitleProviderStatusView{SchemaVersion: 1, Enabled: true, Providers: []string{"example"}}
}
func (f *fakeSubtitleCapabilities) SubtitleAIStatus(context.Context) handlers.SubtitleAIStatusView {
	f.calls++
	return handlers.SubtitleAIStatusView{Enabled: true, TranscribeEnabled: true}
}

func TestSubtitleCapabilitiesV2EnforceViewerAccessBeforeService(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := &fakeSubtitleCapabilities{}
	deps.SubtitleProviders, deps.SubtitleAI = f, f
	h := newTestHandler(t, deps)
	for _, path := range []string{"/subtitles/providers/status", "/subtitles/ai/status"} {
		r := do(t, h, http.MethodGet, Prefix+path, "", nil)
		if r.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous request: %d %s", r.Code, r.Body.String())
		}
		requireProblem(t, do(t, h, http.MethodGet, Prefix+path, "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
		requireProblem(t, do(t, h, http.MethodGet, Prefix+path, "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	}
	if f.calls != 0 {
		t.Fatalf("capability service called %d times for rejected viewers", f.calls)
	}
}
