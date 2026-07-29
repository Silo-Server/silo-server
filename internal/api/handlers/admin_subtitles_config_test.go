package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
	manager := subtitles.NewManager(repo, newMockS3ClientForHandler(), "test")
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
	manager := subtitles.NewManager(repo, newMockS3ClientForHandler(), "test")
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)
	handler.providerFactory = func(cfg *subtitles.ProviderConfig) (subtitles.Provider, error) {
		return &adminSubtitleTestProvider{name: cfg.ProviderName, key: cfg.APIKey}, nil
	}
	return handler, manager
}

func TestUpdateSubtitleProviderAppliesSavedDraftLive(t *testing.T) {
	repo := newAdminSubtitleConfigRepo()
	repo.configs["subdl"] = subtitles.ProviderConfig{
		ProviderName: "subdl", Enabled: true, APIKey: subtitleTestCredential("old"),
	}
	handler, manager := newTestableAdminSubtitleHandler(repo)

	body, _ := json.Marshal(updateSubtitleProviderRequest{
		Enabled: true,
		APIKey:  subtitleTestCredential("valid"),
	})
	req := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPut, "/admin/subtitle-providers/subdl", body),
		"provider", "subdl",
	)
	rec := httptest.NewRecorder()
	handler.HandleUpdateProvider(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := repo.providerConfig("subdl").APIKey; got != subtitleTestCredential("valid") {
		t.Fatalf("saved API key = %q, want valid test credential", got)
	}
	results, err := manager.Search(context.Background(), subtitles.SearchRequest{Title: "Movie"})
	if err != nil || len(results.Results) != 1 {
		t.Fatalf("live provider results = %#v, err = %v", results, err)
	}
}

func TestUpdateSubtitleProviderDisablesLiveProvider(t *testing.T) {
	repo := newAdminSubtitleConfigRepo()
	handler, manager := newTestableAdminSubtitleHandler(repo)
	manager.RegisterProvider(&adminSubtitleTestProvider{
		name: "subdl",
		key:  subtitleTestCredential("valid"),
	})

	body, _ := json.Marshal(updateSubtitleProviderRequest{Enabled: false})
	req := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPut, "/admin/subtitle-providers/subdl", body),
		"provider", "subdl",
	)
	rec := httptest.NewRecorder()
	handler.HandleUpdateProvider(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	results, err := manager.Search(context.Background(), subtitles.SearchRequest{Title: "Movie"})
	if err != nil || len(results.Results) != 0 {
		t.Fatalf("disabled provider results = %#v, err = %v", results, err)
	}
}

func TestUpdateLegacySubtitleProviderPreservesV1Compatibility(t *testing.T) {
	repo := newAdminSubtitleConfigRepo()
	handler := NewAdminSubtitleHandler(repo)
	body, _ := json.Marshal(updateSubtitleProviderRequest{
		Enabled: true,
		APIKey:  subtitleTestCredential("legacy"),
	})
	req := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPut, "/admin/subtitle-providers/legacy-provider", body),
		"provider", "legacy-provider",
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateProvider(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := repo.providerConfig("legacy-provider"); !got.Enabled ||
		got.APIKey != subtitleTestCredential("legacy") {
		t.Fatalf("stored legacy provider = %#v", got)
	}
	var response struct {
		AppliedLive bool `json:"applied_live"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.AppliedLive {
		t.Fatal("legacy provider reported as applied live")
	}
}

func TestLegacySubtitleProviderConnectionFailureKeepsHTTP200(t *testing.T) {
	repo := newAdminSubtitleConfigRepo()
	repo.configs["legacy-provider"] = subtitles.ProviderConfig{
		ProviderName: "legacy-provider",
		Enabled:      true,
		APIKey:       subtitleTestCredential("legacy"),
	}
	handler := NewAdminSubtitleHandler(repo)
	req := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPost, "/admin/subtitle-providers/legacy-provider/test", nil),
		"provider", "legacy-provider",
	)
	rec := httptest.NewRecorder()

	handler.HandleTestProvider(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Success || response.Error == "" {
		t.Fatalf("response = %#v, want a 200 failure payload", response)
	}
}

func TestUpdateSubtitleProviderExplicitlyClearsCredentialsAndDisablesLiveProvider(t *testing.T) {
	repo := newAdminSubtitleConfigRepo()
	repo.configs["subdl"] = subtitles.ProviderConfig{
		ProviderName: "subdl", Enabled: true, APIKey: subtitleTestCredential("valid"),
	}
	handler, manager := newTestableAdminSubtitleHandler(repo)
	manager.RegisterProvider(&adminSubtitleTestProvider{
		name: "subdl",
		key:  subtitleTestCredential("valid"),
	})

	body, _ := json.Marshal(updateSubtitleProviderRequest{Enabled: true, ClearCredentials: true})
	req := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPut, "/admin/subtitle-providers/subdl", body),
		"provider", "subdl",
	)
	rec := httptest.NewRecorder()
	handler.HandleUpdateProvider(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := repo.providerConfig("subdl"); got.Enabled || got.APIKey != "" {
		t.Fatalf("cleared config = %#v", got)
	}
	results, err := manager.Search(context.Background(), subtitles.SearchRequest{Title: "Movie"})
	if err != nil || len(results.Results) != 0 {
		t.Fatalf("cleared live provider results = %#v, err = %v", results, err)
	}
}

func TestConcurrentSubtitlePutAndClearApplyLatestCommittedConfig(t *testing.T) {
	baseRepo := newAdminSubtitleConfigRepo()
	repo := &blockedSubtitleUpsertRepo{
		adminSubtitleConfigRepo: baseRepo,
		upsertPersisted:         make(chan struct{}),
		releaseUpsert:           make(chan struct{}),
	}
	handler, manager := newTestableAdminSubtitleHandlerWithRepo(repo)

	putBody, _ := json.Marshal(updateSubtitleProviderRequest{
		Enabled: true,
		APIKey:  subtitleTestCredential("valid"),
	})
	putReq := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPut, "/admin/subtitle-providers/subdl", putBody),
		"provider", "subdl",
	)
	putRec := httptest.NewRecorder()
	putDone := make(chan struct{})
	go func() {
		defer close(putDone)
		handler.HandleUpdateProvider(putRec, putReq)
	}()

	<-repo.upsertPersisted

	clearBody, _ := json.Marshal(updateSubtitleProviderRequest{ClearCredentials: true})
	clearReq := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPut, "/admin/subtitle-providers/subdl", clearBody),
		"provider", "subdl",
	)
	clearRec := httptest.NewRecorder()
	handler.HandleUpdateProvider(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", clearRec.Code, clearRec.Body.String())
	}

	close(repo.releaseUpsert)
	<-putDone
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	if got := repo.providerConfig("subdl"); got.Enabled || got.APIKey != "" {
		t.Fatalf("latest persisted config = %#v, want disabled with cleared credentials", got)
	}
	results, err := manager.Search(context.Background(), subtitles.SearchRequest{Title: "Movie"})
	if err != nil || len(results.Results) != 0 {
		t.Fatalf("live provider results = %#v, err = %v; stale PUT config was applied", results, err)
	}
}

func TestSubtitleProviderConnectionTestUsesUnsavedDraft(t *testing.T) {
	repo := newAdminSubtitleConfigRepo()
	repo.configs["subdl"] = subtitles.ProviderConfig{
		ProviderName: "subdl", Enabled: true, APIKey: subtitleTestCredential("old"),
	}
	handler, _ := newTestableAdminSubtitleHandler(repo)

	body, _ := json.Marshal(updateSubtitleProviderRequest{APIKey: subtitleTestCredential("valid")})
	req := withSubtitleRouteParam(
		newAdminSubtitleRequest(http.MethodPost, "/admin/subtitle-providers/subdl/test", body),
		"provider", "subdl",
	)
	rec := httptest.NewRecorder()
	handler.HandleTestProvider(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !response.Success {
		t.Fatalf("response = %s, err = %v", rec.Body.String(), err)
	}
	if got := repo.providerConfig("subdl").APIKey; got != subtitleTestCredential("old") {
		t.Fatalf("connection test persisted draft key %q", got)
	}
}
