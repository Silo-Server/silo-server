package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/recommendations"
)

func TestDiscoverRowSectionKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rowType  string
		label    string
		cluster  int
		wantKind string
		wantKey  string
	}{
		{"for-you main", "cluster", "For You", 0, recommendations.SectionKindForYouMain, ""},
		{"cluster row", "cluster", "Because you enjoy Drama", 2, recommendations.SectionKindCluster, "2"},
		{"cluster zero", "cluster", "Because you enjoy Action", 0, recommendations.SectionKindCluster, "0"},
		{"similar users", "similar_users_liked", "Profiles Like You Enjoyed", 0, recommendations.SectionKindSimilarUsers, ""},
		{"popular", "popular", "Popular on This Server", 0, recommendations.SectionKindPopular, ""},
		{"recently added", "recently_added", "Recently Added", 0, recommendations.SectionKindRecentlyAdded, ""},
		{"top rated", "top_rated", "Top Rated", 0, recommendations.SectionKindTopRated, ""},
		{"highly rated", "top_rated", "Highly Rated in Your Library", 0, recommendations.SectionKindTopRated, ""},
		{"genre", "genre_sampler", "Top Drama", 0, recommendations.SectionKindGenre, "Drama"},
		{"genre with an ampersand", "genre_sampler", "Top Sci-Fi & Fantasy", 0, recommendations.SectionKindGenre, "Sci-Fi & Fantasy"},
		{"genre without a name", "genre_sampler", "Top ", 0, "", ""},
		{"unknown row", "watch_tonight", "Watch Tonight", 0, "", ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotKind, gotKey := discoverRowSectionKey(tc.rowType, tc.label, tc.cluster)
			if gotKind != tc.wantKind || gotKey != tc.wantKey {
				t.Fatalf(
					"discoverRowSectionKey(%q,%q,%d) = (%q,%q), want (%q,%q)",
					tc.rowType, tc.label, tc.cluster, gotKind, gotKey, tc.wantKind, tc.wantKey,
				)
			}
		})
	}
}
