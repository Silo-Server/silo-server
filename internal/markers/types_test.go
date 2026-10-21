package markers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizeModeDefaultsToOnlineAndLocalWithoutChangingExplicitModes(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want Mode
	}{
		{"", ModeBoth},
		{" ", ModeBoth},
		{"off", ModeOff},
		{"local", ModeLocal},
		{"online", ModeOnline},
		{"both", ModeBoth},
		{"invalid", ModeLocal},
	} {
		t.Run("mode_"+tt.raw, func(t *testing.T) {
			if got := NormalizeMode(tt.raw); got != tt.want {
				t.Fatalf("NormalizeMode(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

type fakeProvider struct {
	id     string
	result Result
	err    error
	calls  int
}

func (p *fakeProvider) ID() string { return p.id }

func (p *fakeProvider) FetchMarkers(context.Context, Request) (Result, error) {
	p.calls++
	return p.result, p.err
}

func populateRegistryForTest(ctx context.Context, registry *Registry) (Result, bool, error) {
	var result Result
	service := NewPopulationService(PopulationOptions{
		Registry: registry, Store: &populationRecorder{},
		Settings: populationSettings{"setup.completed": "true", SettingMode: "online", SettingOnlineStorage: "stored"},
		Resolver: populationResolver{ids: ExternalIDs{Kind: ItemKindEpisode, TmdbID: "42", SeasonNumber: 1, EpisodeNumber: 1}},
		Write: func(_ context.Context, _ *models.MediaFile, selected Result) (bool, error) {
			result = selected
			return true, nil
		},
	})
	_, _, err := service.Refresh(ctx, &models.MediaFile{ID: 1, Duration: 2000})
	return result, len(result.Markers) > 0, err
}

func TestPopulationRegistryUsesRegistrationPriority(t *testing.T) {
	first := &fakeProvider{id: "first"}
	second := &fakeProvider{
		id: "second",
		result: Result{
			Markers: []Marker{{Kind: MarkerKindIntro, Start: time.Second, End: 2 * time.Second}},
		},
	}
	third := &fakeProvider{
		id: "third",
		result: Result{
			Markers: []Marker{{Kind: MarkerKindIntro, Start: 3 * time.Second, End: 4 * time.Second}},
		},
	}

	registry := NewRegistry(nil)
	if err := registry.Register(first); err != nil {
		t.Fatalf("register first: %v", err)
	}
	if err := registry.Register(second); err != nil {
		t.Fatalf("register second: %v", err)
	}
	if err := registry.Register(third); err != nil {
		t.Fatalf("register third: %v", err)
	}

	result, ok, err := populateRegistryForTest(context.Background(), registry)
	if err != nil {
		t.Fatalf("population returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected hit")
	}
	if len(result.Markers) != 1 || result.Markers[0].ProviderID != "second" {
		t.Fatalf("markers = %+v, want second provider", result.Markers)
	}
	if result.SourceClass != "online" {
		t.Fatalf("SourceClass = %q, want online", result.SourceClass)
	}
	if third.calls != 1 {
		t.Fatalf("third provider calls = %d, want 1", third.calls)
	}
}

func TestPopulationRegistryReturnsLastProviderErrorAsMiss(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	registry := NewRegistry(nil)
	if err := registry.Register(&fakeProvider{id: "first", err: errors.New("temporary")}); err != nil {
		t.Fatalf("register first: %v", err)
	}
	if err := registry.Register(&fakeProvider{id: "second", err: wantErr}); err != nil {
		t.Fatalf("register second: %v", err)
	}

	_, ok, err := populateRegistryForTest(context.Background(), registry)
	if ok {
		t.Fatal("expected miss")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

// With fetch priorities configured, the lowest priority that answers wins,
// a failing preferred provider falls back with its error reported, and equal
// priorities break the tie by marker quality.
func TestPopulationRegistryUsesConfiguredProviderPriority(t *testing.T) {
	intro := func(id string, end time.Duration, confidence float64, submissions int) *fakeProvider {
		return &fakeProvider{id: id, result: Result{ProviderID: id, Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 0, End: end, Confidence: confidence, SubmissionCount: submissions},
		}}}
	}
	for _, tc := range []struct {
		name       string
		providers  []*fakeProvider
		priorities map[string]int
		wantErr    bool
		winner     string
		wantEnd    time.Duration // zero: not checked
	}{
		{
			name:       "lower fetch_priority wins",
			providers:  []*fakeProvider{intro("a", 30*time.Second, 0.9, 500), intro("b", 25*time.Second, 0.6, 1)},
			priorities: map[string]int{"a": 20, "b": 10},
			winner:     "b", wantEnd: 25 * time.Second,
		},
		{
			name:       "falls back when the higher priority provider errors",
			providers:  []*fakeProvider{{id: "higher", err: errors.New("temporary provider failure")}, intro("lower", 25*time.Second, 0.6, 0)},
			priorities: map[string]int{"higher": 10, "lower": 20},
			wantErr:    true, winner: "lower",
		},
		{
			name:       "same priority tie-breaks by submission count",
			providers:  []*fakeProvider{intro("a", 30*time.Second, 0.7, 1), intro("b", 25*time.Second, 0.9, 2)},
			priorities: map[string]int{"a": 10, "b": 10},
			winner:     "b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry(nil)
			config := &ProviderConfigStore{cache: map[string]ProviderConfig{}}
			for _, provider := range tc.providers {
				_ = registry.Register(provider)
				config.cache[provider.id] = ProviderConfig{Provider: provider.id, FetchEnabled: true, FetchPriority: tc.priorities[provider.id]}
			}
			registry.config = config

			res, ok, err := populateRegistryForTest(context.Background(), registry)
			if (err != nil) != tc.wantErr {
				t.Fatalf("population error = %v, want error %v", err, tc.wantErr)
			}
			if !ok || len(res.Markers) != 1 {
				t.Fatalf("merged = %+v, ok=%v", res.Markers, ok)
			}
			if got := res.Markers[0]; got.ProviderID != tc.winner || (tc.wantEnd != 0 && got.End != tc.wantEnd) {
				t.Fatalf("winner = %+v, want provider %s", got, tc.winner)
			}
		})
	}
}

func TestNormalizeSetting(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		want    string
		wantErr bool
	}{
		{name: "mode", key: SettingMode, value: "both", want: "both"},
		{name: "mode trims", key: SettingMode, value: " online ", want: "online"},
		{name: "mode rejects unknown", key: SettingMode, value: "remote", wantErr: true},
		{name: "lazy true", key: SettingLazyPlayback, value: "TRUE", want: "true"},
		{name: "lazy rejects unknown", key: SettingLazyPlayback, value: "yes", wantErr: true},
		{name: "detect intros false", key: SettingDetectIntros, value: " False ", want: "false"},
		{name: "detect intros rejects unknown", key: SettingDetectIntros, value: "off", wantErr: true},
		{name: "detect credits true", key: SettingDetectCredits, value: "true", want: "true"},
		{name: "detect credits rejects empty", key: SettingDetectCredits, value: "", wantErr: true},
		{name: "other rejected", key: "playback.foo", value: " raw ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSetting(tt.key, tt.value)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.key) {
					t.Fatalf("error = %v, want it to name %s", err, tt.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeSetting returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeSetting = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectionToggleEnabled(t *testing.T) {
	for raw, want := range map[string]bool{
		"":        true,
		"true":    true,
		" TRUE ":  true,
		"false":   false,
		" False ": false,
	} {
		if got := DetectionToggleEnabled(raw); got != want {
			t.Errorf("DetectionToggleEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}
