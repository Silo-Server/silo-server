package apiv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

func TestAIStatusReadsStopWhenRequestIsCanceled(t *testing.T) {
	for _, path := range []string{
		"/api/v1/metadata/ai/status", "/api/v1/subtitles/ai/status",
		Prefix + "/capabilities/metadata-ai", Prefix + "/subtitles/ai/status",
	} {
		t.Run(path, func(t *testing.T) {
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			defer close(release)
			source := func(ctx context.Context) (llm.Config, error) {
				started <- struct{}{}
				select {
				case <-ctx.Done():
				case <-release:
				}
				return llm.Config{}, context.Canceled
			}
			metadata := handlers.NewMetadataAIHandler(translation.NewService(t.Context(), translation.Config{
				Enabled: true, Configured: true, ChatModel: "model", OnView: "auto", TextConfigSource: source,
			}, nil, nil, nil, nil, nil, nil))
			subtitles := handlers.NewSubtitleAIHandler(ai.NewService(t.Context(), ai.Config{
				Configured: true, TranslateEnabled: true, ChatModel: "model", TextConfigSource: source,
			}, nil, nil, nil, nil, nil, nil, nil, "", nil, nil))
			deps, _ := catalogDeps(t)
			deps.MetadataAI, deps.SubtitleAI = metadata, subtitles
			h := newTestHandler(t, deps)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			for key, value := range viewerHeaders() {
				r.Header.Set(key, value)
			}
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				switch path {
				case "/api/v1/metadata/ai/status":
					metadata.HandleStatus(w, r)
				case "/api/v1/subtitles/ai/status":
					subtitles.HandleStatus(w, r)
				default:
					h.ServeHTTP(w, r)
				}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("status request did not reach live configuration read")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("status request retained a configuration read after cancellation")
			}
		})
	}
}
