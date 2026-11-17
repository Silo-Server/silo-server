package catalog

import (
	"encoding/json"
	"testing"
)

// Backups carry scores; a backup written before scores carries whole stars
// as rating, which restores at twice the stars. Out-of-range values refuse
// the document.
func TestDecodeRatingsReadsScoresAndLegacyStars(t *testing.T) {
	for _, tc := range []struct {
		doc   string
		score int
		ok    bool
	}{
		{`{"item":"m1","score":7,"rated_at":"2026-01-01T00:00:00Z"}`, 7, true},
		{`{"item":"m1","score":10,"rated_at":"2026-01-01T00:00:00Z"}`, 10, true},
		{`{"item":"m1","rating":4,"rated_at":"2026-01-01T00:00:00Z"}`, 8, true},
		{`{"item":"m1","score":11,"rated_at":"2026-01-01T00:00:00Z"}`, 0, false},
		{`{"item":"m1","rating":6,"rated_at":"2026-01-01T00:00:00Z"}`, 0, false},
		{`{"item":"m1","rated_at":"2026-01-01T00:00:00Z"}`, 0, false},
		{`{"item":"m1","score":8,"rating":4,"rated_at":"2026-01-01T00:00:00Z"}`, 0, false},
	} {
		doc, _, err := decodeRatings(json.RawMessage(`{"ratings":[` + tc.doc + `]}`))
		if (err == nil) != tc.ok {
			t.Fatalf("%s: err = %v, want ok %v", tc.doc, err, tc.ok)
		}
		if tc.ok && doc.Ratings[0].score() != tc.score {
			t.Fatalf("%s: score = %d, want %d", tc.doc, doc.Ratings[0].score(), tc.score)
		}
	}
}
