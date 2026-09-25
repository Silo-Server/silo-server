package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestPlayedOnlyBrowseBindsEveryParameter(t *testing.T) {
	for _, played := range []bool{true, false} {
		plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(BrowseFilters{Type: "movie", UserID: 7, ProfileID: "child", IsPlayed: new(played), Limit: 1})
		if err != nil || empty {
			t.Fatalf("build: empty=%v err=%v", empty, err)
		}
		sql, args := plan.pagedSQL(false)
		for i := range args {
			if !regexp.MustCompile(fmt.Sprintf(`\$%d\b`, i+1)).MatchString(sql) {
				t.Fatalf("played=%v parameter $%d has no SQL reference: %s", played, i+1, sql)
			}
		}
	}
}

func TestBrowseCombinedPredicatesPreserveProfileAndAccess(t *testing.T) {
	filters := BrowseFilters{Type: "movie", Genres: []string{"Drama", "Comedy"}, Years: []int{2020, 2024}, SearchTerm: "100%", UserID: 7, ProfileID: "child", IsFavorite: true, IsPlayed: new(false), IsResumable: true, LibraryIDs: []int{3}, DisabledLibraryIDs: []int{9}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}, Limit: 1, Offset: 2}
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(filters)
	if err != nil || empty {
		t.Fatalf("build plan: empty=%v err=%v", empty, err)
	}
	sql, args := plan.pagedSQL(false)
	for _, predicate := range []string{"mi.genres &&", "mi.year = ANY", "mi.title ILIKE", "user_favorites", "user_watch_progress", "profile_id", "NOT EXISTS", "content_rating_age", "media_folder_id", "LIMIT", "OFFSET"} {
		if !strings.Contains(sql, predicate) {
			t.Errorf("missing %q in %s", predicate, sql)
		}
	}
	hasProfile, hasLiteral := false, false
	for _, arg := range args {
		if str, ok := arg.(string); ok {
			hasProfile = hasProfile || str == "child"
			hasLiteral = hasLiteral || str == `%100\%%`
		}
	}
	if !hasProfile || !hasLiteral {
		t.Fatalf("missing bound profile or literal search pattern: %v", args)
	}
}

func TestBrowseUserStateWithoutProfileFailsClosed(t *testing.T) {
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(BrowseFilters{Type: "movie", UserID: 7, IsFavorite: true})
	if err != nil || empty {
		t.Fatalf("build: %v %v", empty, err)
	}
	sql, _ := plan.pagedSQL(false)
	if !strings.Contains(sql, "FALSE") {
		t.Fatalf("unscoped state query: %s", sql)
	}
}

func TestBrowsePlayedSeriesUsesEpisodeCompletionAndHistory(t *testing.T) {
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(BrowseFilters{Type: "series", UserID: 7, ProfileID: "child", IsPlayed: new(false)})
	if err != nil || empty {
		t.Fatalf("build: %v %v", empty, err)
	}
	sql, _ := plan.pagedSQL(false)
	for _, predicate := range []string{"episodes.series_id = mi.content_id", "user_watch_history", "user_history_hidden_items", "episode_libraries"} {
		if !strings.Contains(sql, predicate) {
			t.Fatalf("played series omits %s: %s", predicate, sql)
		}
	}
}

func TestBrowseLanguagePredicatesUseStoredCodeArrays(t *testing.T) {
	filters := BrowseFilters{Type: "movie,series", AudioLanguages: []string{"eng", "pt-BR"}, SubtitleLanguages: []string{"fre"}, Limit: 1}
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(filters)
	if err != nil || empty {
		t.Fatalf("build plan: empty=%v err=%v", empty, err)
	}
	sql, args := plan.pagedSQL(false)
	for _, predicate := range []string{"mf.content_id = mi.content_id", "mf.audio_language_codes &&", "mf.subtitle_language_codes &&", "external_subtitles", "mf.missing_since IS NULL"} {
		if !strings.Contains(sql, predicate) {
			t.Errorf("missing %q in %s", predicate, sql)
		}
	}
	for i := range args {
		if !regexp.MustCompile(fmt.Sprintf(`\$%d\b`, i+1)).MatchString(sql) {
			t.Fatalf("parameter $%d has no SQL reference: %s", i+1, sql)
		}
	}
	var audio, subtitle []string
	for _, arg := range args {
		if codes, ok := arg.([]string); ok {
			switch {
			case slices.Contains(codes, "en"):
				audio = codes
			case slices.Contains(codes, "fr"):
				subtitle = codes
			}
		}
	}
	if !slices.Equal(audio, []string{"en", "eng", "pt-br"}) {
		t.Fatalf("audio codes = %v, want canonical and raw lowercase forms", audio)
	}
	if !slices.Equal(subtitle, []string{"fr", "fra", "fre"}) {
		t.Fatalf("subtitle codes = %v, want the canonical code and both ISO 639-2 forms", subtitle)
	}
}

func TestBrowseLanguagePredicateJoinsEpisodeFiles(t *testing.T) {
	var conditions []string
	var args []any
	idx := 1
	appendCompatBrowsePredicates(BrowseFilters{Type: browseTypeEpisode, AudioLanguages: []string{"ja"}}, &conditions, &args, &idx)
	if len(conditions) != 1 || !strings.Contains(conditions[0], "mf.episode_id = mi.content_id") {
		t.Fatalf("episode scope should join media files by episode_id: %v", conditions)
	}
}
