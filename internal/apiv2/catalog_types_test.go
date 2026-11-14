package apiv2

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestCatalogUserStateEpisodeCounts(t *testing.T) {
	watched, unplayed := 4, 6
	for _, tc := range []struct {
		state *handlers.ItemUserStateView
		want  string
	}{
		{&handlers.ItemUserStateView{InWatchlist: true}, `{"played":false,"is_favorite":false,"in_watchlist":true,"not_interested":false}`},
		{&handlers.ItemUserStateView{WatchedCount: &watched, UnplayedCount: &unplayed}, `{"played":false,"is_favorite":false,"in_watchlist":false,"not_interested":false,"watched_count":4,"unplayed_count":6}`},
	} {
		got, err := json.Marshal(catalogUserStateOf(tc.state))
		if err != nil || string(got) != tc.want {
			t.Fatalf("user_state = %s, %v; want %s", got, err, tc.want)
		}
	}
	// v1 shares the view and never carries the counts.
	v1, err := json.Marshal(&handlers.ItemUserStateView{WatchedCount: &watched, UnplayedCount: &unplayed})
	if err != nil || string(v1) != `{"played":false,"is_favorite":false,"in_watchlist":false}` {
		t.Fatalf("v1 user_state = %s, %v", v1, err)
	}
}
