package markers

import (
	"context"
	"errors"
	"testing"
	"time"
)

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

func TestRegistryFetchFirstHitUsesPriorityOrder(t *testing.T) {
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

	result, ok, err := registry.FetchFirstHit(context.Background(), Request{Kind: ItemKindEpisode})
	if err != nil {
		t.Fatalf("FetchFirstHit returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected hit")
	}
	if result.ProviderID != "second" {
		t.Fatalf("ProviderID = %q, want second", result.ProviderID)
	}
	if result.SourceClass != "online" {
		t.Fatalf("SourceClass = %q, want online", result.SourceClass)
	}
	if third.calls != 0 {
		t.Fatalf("third provider should not be called after hit, got %d calls", third.calls)
	}
}

func TestRegistryFetchFirstHitReturnsLastProviderErrorAsMiss(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	registry := NewRegistry(nil)
	if err := registry.Register(&fakeProvider{id: "first", err: errors.New("temporary")}); err != nil {
		t.Fatalf("register first: %v", err)
	}
	if err := registry.Register(&fakeProvider{id: "second", err: wantErr}); err != nil {
		t.Fatalf("register second: %v", err)
	}

	_, ok, err := registry.FetchFirstHit(context.Background(), Request{Kind: ItemKindEpisode})
	if ok {
		t.Fatal("expected miss")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestRegistryFetchMergedKeepsBestPerSegment(t *testing.T) {
	a := &fakeProvider{id: "a", result: Result{ProviderID: "a", Algorithm: "a:v1", Markers: []Marker{
		{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second, Confidence: 0.7, SubmissionCount: 50},
		{Kind: MarkerKindCredits, Start: 100 * time.Second, End: 110 * time.Second, Confidence: 0.5, SubmissionCount: 1},
	}}}
	b := &fakeProvider{id: "b", result: Result{ProviderID: "b", Algorithm: "b:v1", Markers: []Marker{
		{Kind: MarkerKindIntro, Start: 0, End: 25 * time.Second, Confidence: 0.9, SubmissionCount: 5},
		{Kind: MarkerKindCredits, Start: 105 * time.Second, End: 120 * time.Second, Confidence: 0.6, SubmissionCount: 40},
	}}}

	registry := NewRegistry(nil)
	if err := registry.Register(a); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := registry.Register(b); err != nil {
		t.Fatalf("register b: %v", err)
	}

	res, ok, err := registry.FetchMerged(context.Background(), Request{Kind: ItemKindEpisode})
	if err != nil {
		t.Fatalf("FetchMerged: %v", err)
	}
	if !ok {
		t.Fatal("expected hit")
	}

	byKind := map[MarkerKind]Marker{}
	for _, m := range res.Markers {
		byKind[m.Kind] = m
	}
	if intro := byKind[MarkerKindIntro]; intro.ProviderID != "a" || intro.End != 30*time.Second {
		t.Errorf("intro = %+v, want provider a (50 submissions) end 30s", intro)
	}
	if credits := byKind[MarkerKindCredits]; credits.ProviderID != "b" || credits.End != 120*time.Second {
		t.Errorf("credits = %+v, want provider b (40 submissions) end 120s", credits)
	}
}

func TestRegistryFetchMergedSingleProviderParity(t *testing.T) {
	only := &fakeProvider{id: "introdb", result: Result{
		ProviderID:  "introdb",
		SourceClass: "online",
		Algorithm:   "introdb:v3",
		Markers:     []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second, Confidence: 0.9}},
	}}
	registry := NewRegistry(nil)
	if err := registry.Register(only); err != nil {
		t.Fatalf("register: %v", err)
	}
	res, ok, err := registry.FetchMerged(context.Background(), Request{Kind: ItemKindEpisode})
	if err != nil {
		t.Fatalf("FetchMerged: %v", err)
	}
	if !ok || len(res.Markers) != 1 || res.Markers[0].Kind != MarkerKindIntro {
		t.Fatalf("single-provider merge = %+v, ok=%v", res.Markers, ok)
	}
	if res.Markers[0].ProviderID != "introdb" {
		t.Errorf("provider = %q, want introdb (stamped from result)", res.Markers[0].ProviderID)
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
		{name: "other rejected", key: "playback.foo", value: " raw ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSetting(tt.key, tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
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
