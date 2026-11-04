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

// Confidence picks the winner across providers; equal confidence falls back to
// the lower fetch_priority, then submission count, and a failing provider
// falls back to the next with its error reported.
func TestPopulationRegistryUsesConfiguredProviderPriority(t *testing.T) {
	intro := func(id string, end time.Duration, confidence float64, submissions int) *fakeProvider {
		return &fakeProvider{id: id, result: Result{ProviderID: id, Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 0, End: end, Confidence: confidence, SubmissionCount: submissions},
		}}}
	}
	credits := func(id string, start, end time.Duration) *fakeProvider {
		return &fakeProvider{id: id, result: Result{ProviderID: id, Markers: []Marker{
			{Kind: MarkerKindCredits, Start: start, End: end, Confidence: 0.9},
		}}}
	}
	absent := func(id string, confidence float64) *fakeProvider {
		return &fakeProvider{id: id, result: Result{ProviderID: id, Markers: []Marker{
			{Kind: MarkerKindIntro, Absent: true, Confidence: confidence},
		}}}
	}
	both := absent("both", 0.9)
	both.result.Markers = append(both.result.Markers, Marker{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second, Confidence: 0.5})
	for _, tc := range []struct {
		name       string
		providers  []*fakeProvider
		priorities map[string]int
		wantErr    bool
		winner     string
		wantEnd    time.Duration // zero: not checked
	}{
		{
			name:       "an absent answer competes like a marker",
			providers:  []*fakeProvider{absent("a", 0.9), intro("b", 25*time.Second, 0.6, 1)},
			priorities: map[string]int{"a": 10, "b": 20},
			winner:     "a",
		},
		{
			name:       "a marker beats a weaker absent answer",
			providers:  []*fakeProvider{intro("a", 25*time.Second, 0.9, 1), absent("b", 0.6)},
			priorities: map[string]int{"a": 10, "b": 20},
			winner:     "a", wantEnd: 25 * time.Second,
		},
		{
			name:       "a provider's range outweighs its own absent answer",
			providers:  []*fakeProvider{both},
			priorities: map[string]int{"both": 10},
			winner:     "both", wantEnd: 30 * time.Second,
		},
		{
			name:       "higher confidence beats a better priority",
			providers:  []*fakeProvider{intro("a", 30*time.Second, 0.9, 1), intro("b", 25*time.Second, 0.6, 500)},
			priorities: map[string]int{"a": 20, "b": 10},
			winner:     "a", wantEnd: 30 * time.Second,
		},
		{
			name:       "equal confidence falls back to lower fetch_priority",
			providers:  []*fakeProvider{intro("a", 30*time.Second, 0.8, 500), intro("b", 25*time.Second, 0.8, 1)},
			priorities: map[string]int{"a": 20, "b": 10},
			winner:     "b", wantEnd: 25 * time.Second,
		},
		{
			name:       "a marker without confidence loses to a rated one",
			providers:  []*fakeProvider{intro("a", 30*time.Second, 0, 500), intro("b", 25*time.Second, 0.1, 1)},
			priorities: map[string]int{"a": 10, "b": 20},
			winner:     "b", wantEnd: 25 * time.Second,
		},
		{
			name:       "falls back when the higher priority provider errors",
			providers:  []*fakeProvider{{id: "higher", err: errors.New("temporary provider failure")}, intro("lower", 25*time.Second, 0.6, 0)},
			priorities: map[string]int{"higher": 10, "lower": 20},
			wantErr:    true, winner: "lower",
		},
		{
			// The helper's file runs 2000 seconds; its credits window opens at 1500.
			name:       "misplaced credits do not outrank another provider's credits",
			providers:  []*fakeProvider{credits("early", 94*time.Second, 112*time.Second), credits("late", 1900*time.Second, 2000*time.Second)},
			priorities: map[string]int{"early": 10, "late": 20},
			winner:     "late", wantEnd: 2000 * time.Second,
		},
		{
			name:       "same confidence and priority tie-breaks by submission count",
			providers:  []*fakeProvider{intro("a", 30*time.Second, 0.9, 1), intro("b", 25*time.Second, 0.9, 2)},
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

// A marker below its provider's minimum confidence leaves its kind to the next
// provider. A provider left with nothing for a kind withdraws its stored marker
// of that kind, even when its answer comes from the cache, and a local marker
// stays.
func TestPopulationRegistrySkipsMarkersBelowMinimumConfidence(t *testing.T) {
	registry := NewRegistry(nil)
	preferred := &fakeProvider{id: "preferred", result: Result{Markers: []Marker{
		{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second, Confidence: 0.2},
		{Kind: MarkerKindCredits, Start: 1800 * time.Second, End: 1900 * time.Second, Confidence: 0.9},
	}}}
	fallback := &fakeProvider{id: "fallback", result: Result{Markers: []Marker{
		{Kind: MarkerKindIntro, Start: 0, End: 25 * time.Second, Confidence: 0.6},
	}}}
	_ = registry.Register(preferred)
	_ = registry.Register(fallback)
	registry.config = &ProviderConfigStore{cache: map[string]ProviderConfig{
		"preferred": {Provider: "preferred", FetchEnabled: true, FetchPriority: 10, FetchMinConfidence: 0.5},
		"fallback":  {Provider: "fallback", FetchEnabled: true, FetchPriority: 20, FetchMinConfidence: 0.5},
	}}

	merged, _, err := populateRegistryForTest(context.Background(), registry)
	if err != nil {
		t.Fatalf("population returned error: %v", err)
	}
	winners := map[MarkerKind]string{}
	for _, m := range merged.Markers {
		winners[m.Kind] = m.ProviderID
	}
	if len(merged.Markers) != 2 || winners[MarkerKindIntro] != "fallback" || winners[MarkerKindCredits] != "preferred" {
		t.Fatalf("merged = %+v, want the fallback intro and the preferred credits", merged.Markers)
	}

	cached := mergeProviderResults([]providerResult{{
		entry:  fetchEntry{provider: preferred, minConfidence: 0.95},
		result: preferred.result,
	}})
	file := &models.MediaFile{ID: 1, Duration: 2000,
		IntroStart: new(0.0), IntroEnd: new(30.0), IntroMarkersSource: new(models.MarkerSourceOnline), IntroMarkersProvider: new("preferred"),
		CreditsStart: new(1810.0), CreditsEnd: new(1900.0), CreditsMarkersSource: new(models.MarkerSourceScanner),
		CreditsMarkersAlgorithm: new("credits-chapter:v1"), CreditsMarkersConfidence: new(0.95)}
	next := ApplyResult(file, cached)
	if next.IntroStart != nil {
		t.Fatalf("intro = %v, want the provider's stored intro withdrawn", *next.IntroStart)
	}
	if next.CreditsStart == nil || *next.CreditsStart != 1810 {
		t.Fatalf("credits = %v, want the local credits kept", next.CreditsStart)
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

func TestShareByDefaultEnabled(t *testing.T) {
	for raw, want := range map[string]bool{
		"":        false,
		"true":    true,
		" TRUE ":  true,
		"false":   false,
		" False ": false,
	} {
		if got := ShareByDefaultEnabled(raw); got != want {
			t.Errorf("ShareByDefaultEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}
