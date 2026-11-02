package watchlist

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func TestSnapshotFromDetail(t *testing.T) {
	snap, err := SnapshotFromDetail(&tmdb.MediaDetail{MediaType: "series", ID: 5, FirstAirDate: "2020-02-03", VoteAverage: 8, VoteCount: 0})
	if err != nil || snap.VoteAverage != nil || snap.ReleaseDate == nil || snap.ReleaseDate.Format(time.DateOnly) != "2020-02-03" {
		t.Fatalf("snapshot = %+v %v", snap, err)
	}
	if _, err := SnapshotFromDetail(&tmdb.MediaDetail{MediaType: "person", ID: 5}); err == nil {
		t.Fatal("expected an unsupported media type error")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-1, 0, 0)
	future := now.AddDate(0, 1, 0)
	for _, tc := range []struct {
		date *time.Time
		want time.Duration
	}{{nil, checkRecent}, {&future, checkRecent}, {&old, checkSettled}} {
		if got := nextCheckAfterSuccess(now, tc.date).Sub(now); got != tc.want {
			t.Errorf("next check for %v = %v, want %v", tc.date, got, tc.want)
		}
	}
}
