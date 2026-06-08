package markers

import "testing"

func TestProviderConfigEnabledForFetchSortsAndFilters(t *testing.T) {
	s := &ProviderConfigStore{cache: map[string]ProviderConfig{
		"alpha":   {Provider: "alpha", FetchEnabled: true, FetchPriority: 200},
		"bravo":   {Provider: "bravo", FetchEnabled: true, FetchPriority: 100},
		"charlie": {Provider: "charlie", FetchEnabled: false, FetchPriority: 50},
	}}

	got := s.EnabledForFetch()
	if len(got) != 2 {
		t.Fatalf("EnabledForFetch returned %d providers, want 2 (charlie disabled)", len(got))
	}
	if got[0].Provider != "bravo" || got[1].Provider != "alpha" {
		t.Errorf("order = [%s %s], want [bravo alpha] by fetch_priority", got[0].Provider, got[1].Provider)
	}
}

func TestProviderConfigGet(t *testing.T) {
	s := &ProviderConfigStore{cache: map[string]ProviderConfig{
		"introdb": {Provider: "introdb", FetchEnabled: true, ContributeEnabled: false},
	}}
	c, ok := s.Get("introdb")
	if !ok {
		t.Fatal("expected introdb config")
	}
	if c.ContributeEnabled {
		t.Error("contribute should default off")
	}
	if _, ok := s.Get("missing"); ok {
		t.Error("unexpected config for unknown provider")
	}
}
