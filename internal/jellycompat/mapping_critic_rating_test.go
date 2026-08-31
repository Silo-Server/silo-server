package jellycompat

import "testing"

// TestItemFromList_NeverPopulatesCriticRating locks in that the list mapper
// never puts a 0-10 TMDB score into Jellyfin's 0-100 CriticRating field —
// Jellyfin Web treats CriticRating as a percentage with a 60 fresh/rotten
// threshold, so a raw TMDB score like 8.5 would render as rotten (fixes #693).
func TestItemFromList_NeverPopulatesCriticRating(t *testing.T) {
	m := &mapper{codec: NewResourceIDCodec()}
	tmdbScore := 8.5

	tests := map[string]map[string]bool{
		"no fields requested":               nil,
		"criticrating explicitly requested": {"criticrating": true},
		"all fields requested":              allDetailFields,
	}

	for name, fields := range tests {
		t.Run(name, func(t *testing.T) {
			item := upstreamListItem{ContentID: "1", Type: "movie", RatingTMDB: &tmdbScore}
			dto := m.itemFromList(item, false, nil, fields)

			if dto.CriticRating != nil {
				t.Errorf("CriticRating = %v, want nil (TMDB score must never populate this field)", *dto.CriticRating)
			}
		})
	}
}

// TestItemFromList_CriticRatingNilWhenTMDBRatingAbsent verifies an absent TMDB
// score stays absent rather than being synthesized into a critic rating.
func TestItemFromList_CriticRatingNilWhenTMDBRatingAbsent(t *testing.T) {
	m := &mapper{codec: NewResourceIDCodec()}
	item := upstreamListItem{ContentID: "1", Type: "movie", RatingTMDB: nil}

	dto := m.itemFromList(item, false, nil, allDetailFields)

	if dto.CriticRating != nil {
		t.Errorf("CriticRating = %v, want nil", *dto.CriticRating)
	}
}

// TestItemFromDetail_NeverPopulatesCriticRating mirrors the list-path guard
// for the detail response path.
func TestItemFromDetail_NeverPopulatesCriticRating(t *testing.T) {
	m := &mapper{codec: NewResourceIDCodec()}
	tmdbScore := 9.1
	item := upstreamItemDetail{ContentID: "1", Type: "movie", RatingTMDB: &tmdbScore}

	dto := m.itemFromDetail(item, false, nil)

	if dto.CriticRating != nil {
		t.Errorf("CriticRating = %v, want nil (TMDB score must never populate this field)", *dto.CriticRating)
	}
}

// TestItemFromDetail_CommunityRatingUnaffected verifies the unrelated
// CommunityRating (IMDb-sourced) mapping is untouched by the CriticRating fix.
func TestItemFromDetail_CommunityRatingUnaffected(t *testing.T) {
	m := &mapper{codec: NewResourceIDCodec()}
	imdbScore := 7.3
	item := upstreamItemDetail{ContentID: "1", Type: "movie", RatingIMDB: &imdbScore}

	dto := m.itemFromDetail(item, false, nil)

	if dto.CommunityRating == nil || *dto.CommunityRating != imdbScore {
		t.Errorf("CommunityRating = %v, want %v", dto.CommunityRating, imdbScore)
	}
	if dto.CriticRating != nil {
		t.Errorf("CriticRating = %v, want nil", *dto.CriticRating)
	}
}
