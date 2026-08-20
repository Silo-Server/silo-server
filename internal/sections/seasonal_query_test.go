package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSeasonalThemeHasQueryCoversAllOrderedThemes ensures every theme that can
// win multi-theme selection resolves to either a genre query or a keyword
// fallback. A theme in SeasonalThemeOrder without either would black out the
// section during its own window.
func TestSeasonalThemeHasQueryCoversAllOrderedThemes(t *testing.T) {
	for _, theme := range recipes.SeasonalThemeOrder {
		if !seasonalThemeHasQuery(theme) {
			t.Errorf("theme %q is selectable (SeasonalThemeOrder) but has no query or keyword fallback", theme)
		}
	}
}

// TestSeasonalKeywordThemesHaveNoGenreQuery pins the routing: keyword themes
// must not also have a genre QueryDefinition, otherwise the keyword fallback
// is dead code.
func TestSeasonalKeywordThemesHaveNoGenreQuery(t *testing.T) {
	for theme := range seasonalKeywordTitles {
		if _, ok := seasonalQueryDef(theme); ok {
			t.Errorf("theme %q has both a genre query and keyword fallback; keyword path unreachable", theme)
		}
		if _, ok := recipes.SeasonalPredicates[theme]; !ok {
			t.Errorf("keyword theme %q has no seasonal predicate", theme)
		}
	}
}

func TestSeasonalThemedFiltersItemsAboveProfileRating(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	allowedID := fmt.Sprintf("seasonal-rating-allowed-%d", suffix)
	blockedID := fmt.Sprintf("seasonal-rating-blocked-%d", suffix)
	var libraryID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', $1, true)
		RETURNING id`, fmt.Sprintf("seasonal-rating-%d", suffix)).Scan(&libraryID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{allowedID, blockedID}); err != nil {
			t.Errorf("cleanup media items: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID); err != nil {
			t.Errorf("cleanup media folder: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, content_rating)
		VALUES ($1, 'movie', 'Allowed Seasonal Movie', ARRAY['Action'], 'PG'),
		       ($2, 'movie', 'Blocked Seasonal Movie', ARRAY['Action'], 'R')`, allowedID, blockedID); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id)
		VALUES ($1, $3), ($2, $3)`, allowedID, blockedID, libraryID); err != nil {
		t.Fatalf("seed media item libraries: %v", err)
	}

	config, err := json.Marshal(recipes.SeasonalThemedParams{Theme: "summer_blockbuster", Mode: "pinned"})
	if err != nil {
		t.Fatalf("marshal seasonal config: %v", err)
	}
	fetcher := NewFetcher(pool)
	items, _, err := fetcher.fetchSeasonalThemed(ctx, ResolvedSection{
		ID:          "seasonal-rating-test",
		SectionType: SectionSeasonalThemed,
		ItemLimit:   10,
		Config:      config,
	}, &libraryID, nil, catalog.AccessFilter{MaxContentRating: "PG"})
	if err != nil {
		t.Fatalf("fetch seasonal section: %v", err)
	}
	if len(items) != 1 || items[0].ContentID != allowedID {
		got := make([]string, 0, len(items))
		for _, item := range items {
			got = append(got, item.ContentID)
		}
		t.Fatalf("seasonal items = %v, want only %q", got, allowedID)
	}
}

// TestRecommendationConfigAnchorPrecedence verifies because_you_watched honors
// both the legacy source_item_id key and the recipe's anchor_item_id key,
// preferring the legacy key when both are set.
func TestRecommendationConfigAnchorPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   string
	}{
		{"anchor only", `{"anchor_item_id":"abc"}`, "abc"},
		{"source only", `{"source_item_id":"def"}`, "def"},
		{"both set prefers legacy", `{"source_item_id":"def","anchor_item_id":"abc"}`, "def"},
		{"blank strings auto-pick", `{"source_item_id":"","anchor_item_id":"  "}`, ""},
		{"empty config auto-picks", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := parseRecommendationSectionConfig(json.RawMessage(tc.config))
			if got := cfg.anchor(); got != tc.want {
				t.Errorf("anchor() = %q, want %q", got, tc.want)
			}
		})
	}
}
