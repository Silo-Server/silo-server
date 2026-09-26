package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
func TestProviderConfigurationApplicationSaveAndApply(t *testing.T) {
	for _, scenario := range []string{"applied", "newer-applied", "saved-read-failed", "saved-factory-failed", "no-manager", "unsupported", "save-uncertain", "storage-conflict", "stale", "invalid-factory", "missing-factory", "clear", "create", "wildcard", "wildcard-missing"} {
		t.Run(scenario, func(t *testing.T) {
			repo := newRevisionProviderConfigRepo()
			h, manager := revisionProviderHandler(repo)
			name := "subdl"
			expected := new(int64(5))
			change := subtitles.ProviderConfigChange{Enabled: true}
			calls := 0
			h.providerFactory = func(cfg *subtitles.ProviderConfig) (subtitles.Provider, error) {
				calls++
				if scenario == "invalid-factory" || scenario == "saved-factory-failed" && calls > 1 {
					return nil, errors.New("PRIVATE invalid credentials")
				}
				if cfg.APIKey == "" {
					t.Fatal("enabled config was not validated with preserved credentials")
				}
				return &adminSubtitleTestProvider{name: cfg.ProviderName, key: cfg.APIKey}, nil
			}
			switch scenario {
			case "newer-applied":
				repo.advanceAfterSave = true
			case "saved-read-failed":
				repo.failApplyRead = true
			case "no-manager":
				h.manager = nil
			case "unsupported":
				name = "legacy-provider"
			case "save-uncertain":
				repo.saveErr = errors.New("PRIVATE lost SQL reply")
			case "storage-conflict":
				repo.saveErr = &subtitles.ProviderConfigRevisionConflict{CurrentRevision: 8}
			case "stale":
				expected = new(int64(4))
			case "missing-factory":
				h.providerFactory = nil
			case "clear":
				change.ClearCredentials = true
			case "create":
				repo.current = nil
				expected = new(int64(0))
				change.APIKey = "PRIVATE-new"
			case "wildcard":
				expected = nil
			case "wildcard-missing":
				expected = nil
				repo.current = nil
			}
			if scenario == "clear" || scenario == "saved-read-failed" || scenario == "saved-factory-failed" {
				manager.RegisterProvider(&adminSubtitleTestProvider{name: "subdl", key: "old"})
			}
			view, err := h.SaveAdminSubtitleProviderConfiguration(t.Context(), name, change, expected)
			fails := scenario == "save-uncertain" || scenario == "storage-conflict" || scenario == "stale" || scenario == "invalid-factory" || scenario == "missing-factory" || scenario == "wildcard-missing"
			if fails {
				if err == nil {
					t.Fatalf("unexpected success: %+v", view)
				}
				if strings.Contains(err.Error(), "PRIVATE") {
					t.Fatal("private error escaped")
				}
				if len(manager.ProviderNames()) != 0 {
					t.Fatal("failed/uncertain save applied provider")
				}
				if scenario == "save-uncertain" && (repo.reads != 1 || repo.current.Revision != 6) {
					t.Fatal("SQL uncertainty triggered reload")
				}
				if scenario == "stale" || scenario == "invalid-factory" || scenario == "missing-factory" || scenario == "wildcard-missing" {
					if repo.saves != 0 {
						t.Fatal("refused input reached write")
					}
				}
				return
			}
			if err != nil || view.SavedRevision <= 0 || repo.saves != 1 {
				t.Fatalf("save: %+v %v", view, err)
			}
			if !reflect.DeepEqual(repo.change, change) || !reflect.DeepEqual(repo.expected, expected) {
				t.Fatal("original blank-preserve/CAS input changed")
			}
			switch scenario {
			case "saved-read-failed", "saved-factory-failed":
				if view.LocalApply != SubtitleProviderLocalFailed || view.LocalAppliedRevision != nil || len(manager.ProviderNames()) != 1 {
					t.Fatalf("confirmed save hidden by apply failure: %+v", view)
				}
			case "no-manager":
				if view.LocalApply != SubtitleProviderLocalNotConfigured {
					t.Fatal(view)
				}
			case "unsupported":
				if view.LocalApply != SubtitleProviderLocalUnsupported || calls != 0 {
					t.Fatal(view)
				}
			default:
				if view.LocalApply != SubtitleProviderLocalApplied || view.LocalAppliedRevision == nil {
					t.Fatal(view)
				}
				if scenario == "newer-applied" && *view.LocalAppliedRevision != view.SavedRevision+1 {
					t.Fatal("local revision conflated with saved revision")
				}
				if scenario == "clear" && len(manager.ProviderNames()) != 0 {
					t.Fatal("clear retained live provider")
				}
			}
		})
	}
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
func TestProviderConfigurationApplyReadsAfterSharedLane(t *testing.T) {
	repo := newRevisionProviderConfigRepo()
	repo.saved = make(chan struct{})
	h, _ := revisionProviderHandler(repo)
	h.providerReloadMu.Lock()
	result := make(chan AdminSubtitleProviderSaveResult, 1)
	failure := make(chan error, 1)
	go func() {
		view, err := h.SaveAdminSubtitleProviderConfiguration(t.Context(), "subdl", subtitles.ProviderConfigChange{Enabled: true}, new(int64(5)))
		result <- view
		failure <- err
	}()
	<-repo.saved
	repo.guard.Lock()
	repo.current.Revision = 9
	repo.current.Config.Enabled = false
	repo.guard.Unlock()
	h.providerReloadMu.Unlock()
	view := <-result
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
	if view.SavedRevision != 6 || view.LocalAppliedRevision == nil || *view.LocalAppliedRevision != 9 || view.LocalApply != SubtitleProviderLocalApplied {
		t.Fatalf("apply did not read after shared lane: %+v", view)
	}
}
