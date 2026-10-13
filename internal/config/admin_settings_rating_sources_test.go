package config

import (
	"reflect"
	"testing"
)

func TestNormalizeExtraRatingSourcesSetting(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		" RT_Critic , rt_audience,rt_critic": "rt_critic,rt_audience",
		"metacritic,,letterboxd,":            "metacritic,letterboxd",
		// A well-formed name without a source definition is kept, though it
		// shows nothing.
		"kinopoisk": "kinopoisk",
	}
	for raw, want := range cases {
		got, err := NormalizeAdminSetting(CatalogExtraRatingSourcesSettingKey, raw)
		if err != nil || got != want {
			t.Errorf("NormalizeAdminSetting(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"rotten tomatoes", "rt-critic", "1rt", "rt_critic,<script>"} {
		if _, err := NormalizeAdminSetting(CatalogExtraRatingSourcesSettingKey, raw); err == nil {
			t.Errorf("NormalizeAdminSetting(%q) accepted a malformed source name", raw)
		}
	}
	if got, ok := adminSettingDefaults[CatalogExtraRatingSourcesSettingKey]; !ok || got != "" {
		t.Errorf("default = %q (present %v), want empty: only IMDb and TMDB show until an administrator opts in", got, ok)
	}
}

func TestParseRatingSourceListSkipsMalformedNames(t *testing.T) {
	got := ParseRatingSourceList("rt_critic, Metacritic ,bad name,rt_critic,")
	want := []string{"rt_critic", "metacritic"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRatingSourceList = %q, want %q", got, want)
	}
}
