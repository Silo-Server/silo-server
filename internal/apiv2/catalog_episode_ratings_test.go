package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// An episode carries the acting profile's own rating, and an unrated episode
// carries no rating field at all. Clients tell "unrated" from "rated 0" by the
// field's absence, so `omitempty` is part of the contract, not a formatting
// detail.
func TestEpisodeRatingTravelsToTheContract(t *testing.T) {
	stars := 4
	out := episodesOf([]handlers.EpisodeView{
		{ContentID: "episode:severance-s01e01", UserRating: &stars},
		{ContentID: "episode:severance-s01e02"},
	})

	if len(out) != 2 {
		t.Fatalf("want 2 episodes, got %d", len(out))
	}
	if out[0].UserRating == nil || *out[0].UserRating != stars {
		t.Errorf("rated episode lost its rating: %v", out[0].UserRating)
	}
	if out[1].UserRating != nil {
		t.Errorf("unrated episode invented a rating: %v", *out[1].UserRating)
	}

	encoded, err := json.Marshal(out[1])
	if err != nil {
		t.Fatalf("encode unrated episode: %v", err)
	}
	if strings.Contains(string(encoded), "user_rating") {
		t.Errorf("unrated episode must omit user_rating, got %s", encoded)
	}

	encoded, err = json.Marshal(out[0])
	if err != nil {
		t.Fatalf("encode rated episode: %v", err)
	}
	if !strings.Contains(string(encoded), `"user_rating":4`) {
		t.Errorf("rated episode must carry user_rating, got %s", encoded)
	}
}
