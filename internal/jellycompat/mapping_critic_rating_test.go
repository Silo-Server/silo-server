package jellycompat

import (
	"encoding/json"
	"testing"
)

func floatPtr(v float64) *float64 {
	return &v
}

func TestMapping_CriticRatingNotPopulatedFromTMDB(t *testing.T) {
	codec := NewResourceIDCodec()
	m := newMapper(codec, nil)

	tmdbRating := floatPtr(8.5)
	imdbRating := floatPtr(7.8)

	listItem := upstreamListItem{
		ContentID:  "movie-1",
		Type:       "movie",
		Title:      "Test Movie",
		RatingTMDB: tmdbRating,
		RatingIMDB: imdbRating,
	}

	t.Run("itemFromList without fields", func(t *testing.T) {
		dto := m.itemFromList(listItem, false, nil, map[string]bool{})
		if dto.CriticRating != nil {
			t.Fatalf("expected CriticRating to be nil, got %v", *dto.CriticRating)
		}
		if dto.CommunityRating == nil || *dto.CommunityRating != 7.8 {
			t.Fatalf("expected CommunityRating to be 7.8, got %v", dto.CommunityRating)
		}
	})

	t.Run("itemFromList with requested criticrating field", func(t *testing.T) {
		dto := m.itemFromList(listItem, false, nil, map[string]bool{"criticrating": true})
		if dto.CriticRating != nil {
			t.Fatalf("expected CriticRating to be nil even when criticrating field requested, got %v", *dto.CriticRating)
		}
	})

	t.Run("itemFromList with allDetailFields sentinel", func(t *testing.T) {
		dto := m.itemFromList(listItem, false, nil, allDetailFields)
		if dto.CriticRating != nil {
			t.Fatalf("expected CriticRating to be nil with allDetailFields, got %v", *dto.CriticRating)
		}
	})

	detailItem := upstreamItemDetail{
		ContentID:  "movie-1",
		Type:       "movie",
		Title:      "Test Movie",
		RatingTMDB: tmdbRating,
		RatingIMDB: imdbRating,
	}

	t.Run("itemFromDetail", func(t *testing.T) {
		dto := m.itemFromDetail(detailItem, false, nil)
		if dto.CriticRating != nil {
			t.Fatalf("expected CriticRating to be nil on detail mapping, got %v", *dto.CriticRating)
		}
		if dto.CommunityRating == nil || *dto.CommunityRating != 7.8 {
			t.Fatalf("expected CommunityRating to be 7.8 on detail mapping, got %v", dto.CommunityRating)
		}
	})

	t.Run("JSON serialization omits CriticRating when nil", func(t *testing.T) {
		dto := m.itemFromDetail(detailItem, false, nil)
		data, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("unexpected marshal error: %v", err)
		}

		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unexpected unmarshal error: %v", err)
		}

		if val, exists := raw["CriticRating"]; exists {
			t.Fatalf("expected CriticRating key to be omitted from JSON when nil, but found %v", val)
		}
	})
}
