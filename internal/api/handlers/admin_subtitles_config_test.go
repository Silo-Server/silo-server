package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type adminSubtitleConfigRepo struct {
	*handlerMockSubtitleRepo
	mu      sync.Mutex
	configs map[string]subtitles.ProviderConfig
}

func newAdminSubtitleConfigRepo() *adminSubtitleConfigRepo {
	return &adminSubtitleConfigRepo{
		handlerMockSubtitleRepo: newMockSubtitleRepoForHandler(),
		configs:                 make(map[string]subtitles.ProviderConfig),
	}
}

func (r *adminSubtitleConfigRepo) ListProviderConfigs(context.Context) ([]subtitles.ProviderConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	configs := make([]subtitles.ProviderConfig, 0, len(r.configs))
	for _, cfg := range r.configs {
		configs = append(configs, cfg)
	}
	return configs, nil
}

func (r *adminSubtitleConfigRepo) GetProviderConfig(_ context.Context, name string) (*subtitles.ProviderConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg, ok := r.configs[name]
	if !ok {
		return nil, nil
	}
	return &cfg, nil
}

func (r *adminSubtitleConfigRepo) UpsertProviderConfig(_ context.Context, cfg *subtitles.ProviderConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.configs[cfg.ProviderName] = *cfg
	return nil
}

func (r *adminSubtitleConfigRepo) ClearProviderCredentials(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.configs[name] = subtitles.ProviderConfig{ProviderName: name, Enabled: false}
	return nil
}

func (r *adminSubtitleConfigRepo) providerConfig(name string) subtitles.ProviderConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.configs[name]
}

type blockedSubtitleUpsertRepo struct {
	*adminSubtitleConfigRepo
	upsertPersisted chan struct{}
	releaseUpsert   chan struct{}
}

func (r *blockedSubtitleUpsertRepo) UpsertProviderConfig(ctx context.Context, cfg *subtitles.ProviderConfig) error {
	if err := r.adminSubtitleConfigRepo.UpsertProviderConfig(ctx, cfg); err != nil {
		return err
	}
	close(r.upsertPersisted)
	select {
	case <-r.releaseUpsert:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type adminSubtitleTestProvider struct {
	name string
	key  string
}

func (p *adminSubtitleTestProvider) Name() string { return p.name }

func subtitleTestCredential(kind string) string { return "test-only-" + kind }

func (p *adminSubtitleTestProvider) Search(context.Context, subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	if p.key == subtitleTestCredential("valid") {
		return []subtitles.SubtitleResult{{ID: "result", Provider: p.name}}, nil
	}
	return nil, nil
}

func (p *adminSubtitleTestProvider) Download(context.Context, string) ([]byte, subtitles.SubtitleFormat, error) {
	return []byte("subtitle"), subtitles.FormatSRT, nil
}

func newTestableAdminSubtitleHandler(repo *adminSubtitleConfigRepo) (*AdminSubtitleHandler, *subtitles.Manager) {
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)
	handler.providerFactory = func(cfg *subtitles.ProviderConfig) (subtitles.Provider, error) {
		return &adminSubtitleTestProvider{name: cfg.ProviderName, key: cfg.APIKey}, nil
	}
	return handler, manager
}

func newTestableAdminSubtitleHandlerWithRepo(
	repo subtitles.Repository,
) (*AdminSubtitleHandler, *subtitles.Manager) {
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)
	handler.providerFactory = func(cfg *subtitles.ProviderConfig) (subtitles.Provider, error) {
		return &adminSubtitleTestProvider{name: cfg.ProviderName, key: cfg.APIKey}, nil
	}
	return handler, manager
}

func TestSubtitleProviderConnectionTestRequiresSearchResultsForEveryBuiltin(t *testing.T) {
	for _, provider := range []string{"opensubtitles", "subdl", "subsource"} {
		t.Run(provider, func(t *testing.T) {
			repo := newAdminSubtitleConfigRepo()
			repo.configs[provider] = subtitles.ProviderConfig{ProviderName: provider, Enabled: true, APIKey: subtitleTestCredential("invalid")}
			handler, _ := newTestableAdminSubtitleHandler(repo)
			view := handler.TestSubtitleProvider(context.Background(), provider, SubtitleProviderTestConfig{Enabled: true, APIKey: subtitleTestCredential("invalid")})
			if view.Success || view.Error == "" {
				t.Fatalf("view = %#v, want failed test after search returned no results", view)
			}
		})
	}
}

func newAdminSubtitleRequest(method, path string, body []byte) *http.Request {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    1,
		Role:      "admin",
		TokenType: auth.TokenTypeAccess,
	})
	return req.WithContext(ctx)
}

func withSubtitleRouteParam(req *http.Request, key, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

// A connection test checks the submitted draft credentials over the saved
// ones and saves nothing.
func TestSubtitleProviderConnectionTestUsesUnsavedDraft(t *testing.T) {
	repo := newAdminSubtitleConfigRepo()
	repo.configs["subdl"] = subtitles.ProviderConfig{ProviderName: "subdl", Enabled: true, APIKey: subtitleTestCredential("old")}
	handler, _ := newTestableAdminSubtitleHandler(repo)

	view := handler.TestSubtitleProvider(context.Background(), "subdl", SubtitleProviderTestConfig{APIKey: subtitleTestCredential("valid")})
	if !view.Success {
		t.Fatalf("view = %#v, want the draft key to pass", view)
	}
	if got := repo.providerConfig("subdl").APIKey; got != subtitleTestCredential("old") {
		t.Fatalf("connection test persisted draft key %q", got)
	}
}
