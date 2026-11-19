package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type revisionProviderConfigRepo struct {
	*adminSubtitleConfigRepo
	guard            sync.Mutex
	current          *subtitles.VersionedProviderConfig
	getErr, saveErr  error
	failApplyRead    bool
	advanceAfterSave bool
	saves, reads     int
	change           subtitles.ProviderConfigChange
	expected         *int64
	saved            chan struct{}
}

func newRevisionProviderConfigRepo() *revisionProviderConfigRepo {
	return &revisionProviderConfigRepo{adminSubtitleConfigRepo: newAdminSubtitleConfigRepo(), current: &subtitles.VersionedProviderConfig{Revision: 5, Config: subtitles.ProviderConfig{ProviderName: "subdl", Enabled: true, APIKey: "PRIVATE-original"}}}
}
func (r *revisionProviderConfigRepo) GetProviderConfigWithRevision(context.Context, string) (*subtitles.VersionedProviderConfig, error) {
	r.guard.Lock()
	defer r.guard.Unlock()
	r.reads++
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.failApplyRead && r.saves > 0 {
		return nil, errors.New("PRIVATE read")
	}
	if r.current == nil {
		return nil, nil
	}
	copy := *r.current
	return &copy, nil
}
func (r *revisionProviderConfigRepo) GetProviderConfig(ctx context.Context, name string) (*subtitles.ProviderConfig, error) {
	current, err := r.GetProviderConfigWithRevision(ctx, name)
	if err != nil || current == nil {
		return nil, err
	}
	return &current.Config, nil
}
func (r *revisionProviderConfigRepo) SaveProviderConfigWithRevision(_ context.Context, name string, change subtitles.ProviderConfigChange, expected *int64) (int64, error) {
	r.guard.Lock()
	defer r.guard.Unlock()
	r.saves++
	r.change = change
	if expected != nil {
		r.expected = new(*expected)
	} else {
		r.expected = nil
	}
	if r.saveErr != nil {
		if _, conflict := errors.AsType[*subtitles.ProviderConfigRevisionConflict](r.saveErr); !conflict {
			r.current.Revision++
			r.current.Config.Enabled = false
		}
		return 0, r.saveErr
	}
	if r.current == nil {
		r.current = &subtitles.VersionedProviderConfig{Config: subtitles.ProviderConfig{ProviderName: name}}
	}
	cfg := &r.current.Config
	cfg.Enabled = change.Enabled
	if change.ClearCredentials {
		cfg.Enabled = false
		cfg.APIKey = ""
		cfg.Username = ""
		cfg.Password = ""
	} else {
		if change.APIKey != "" {
			cfg.APIKey = change.APIKey
		}
		if change.Username != "" {
			cfg.Username = change.Username
		}
		if change.Password != "" {
			cfg.Password = change.Password
		}
	}
	r.current.Revision++
	saved := r.current.Revision
	if r.advanceAfterSave {
		r.current.Revision++
		cfg.APIKey = "PRIVATE-newer"
	}
	if r.saved != nil {
		close(r.saved)
	}
	return saved, nil
}
func revisionProviderHandler(repo *revisionProviderConfigRepo) (*AdminSubtitleHandler, *subtitles.Manager) {
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)
	handler.providerFactory = func(cfg *subtitles.ProviderConfig) (subtitles.Provider, error) {
		return &adminSubtitleTestProvider{name: cfg.ProviderName, key: cfg.APIKey}, nil
	}
	return handler, manager
}
func TestProviderConfigurationCanonicalRedactionAndDependencies(t *testing.T) {
	repo := newRevisionProviderConfigRepo()
	h, _ := revisionProviderHandler(repo)
	view, err := h.GetAdminSubtitleProviderConfiguration(t.Context(), "subdl")
	if err != nil || view.Revision != 5 || !view.HasAPIKey {
		t.Fatal(view, err)
	}
	data, _ := json.Marshal(view)
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatal("credential exposed")
	}
	repo.current = nil
	view, err = h.GetAdminSubtitleProviderConfiguration(t.Context(), "subdl")
	if err != nil || view.Revision != 0 || view.Enabled || view.HasAPIKey {
		t.Fatal(view, err)
	}
	repo.getErr = errors.New("PRIVATE lookup")
	if _, err = h.GetAdminSubtitleProviderConfiguration(t.Context(), "subdl"); err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("lookup error not redacted")
	}
	unavailable := NewAdminSubtitleHandler(newAdminSubtitleConfigRepo())
	if _, err = unavailable.GetAdminSubtitleProviderConfiguration(t.Context(), "subdl"); err == nil {
		t.Fatal("missing revision repository admitted")
	}
	if _, err = h.SaveAdminSubtitleProviderConfiguration(t.Context(), "subdl", subtitles.ProviderConfigChange{Password: strings.Repeat("x", 8193)}, new(int64(5))); err == nil {
		t.Fatal("oversized credential admitted")
	}
}
