package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type fakeRoomSuggestions struct {
	writes  int
	vote    bool
	profile string
}

func (f *fakeRoomSuggestions) CheckSuggestionRoomProof(room string, user int, profile, token string) error {
	if room != "room" || token != "room-proof" {
		return &handlers.APIError{Status: 403, Code: "forbidden", Message: "Room proof required."}
	}
	return nil
}
func (f *fakeRoomSuggestions) ListSuggestionPage(_ context.Context, room string, user int, profile string, limit int, after *watchtogether.SuggestionPosition) ([]watchtogether.Suggestion, bool, error) {
	f.profile = profile
	id := "first"
	more := true
	if after != nil {
		id = "second"
		more = false
	}
	return []watchtogether.Suggestion{{ID: id, RoomID: room, SuggesterUserID: 7, SuggesterProfileID: "host", ContentID: "movie", CreatedAt: fixedTime()}}, more, nil
}
func (f *fakeRoomSuggestions) SetSuggestionVote(_ context.Context, room, suggestion string, user int, profile string, vote bool) error {
	f.writes++
	f.vote = vote
	f.profile = profile
	return nil
}
func TestWatchTogetherSuggestionsProofAndCursor(t *testing.T) {
	f := new(fakeRoomSuggestions)
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSuggestions = f
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/suggestions"
	headers := profileOwner()
	headers["X-Room-Token"] = "room-proof"
	rec := do(t, h, http.MethodGet, path+"?limit=1", "", headers)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var page WatchTogetherSuggestionListOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &page.Body); err != nil {
		t.Fatal(err)
	}
	if len(page.Body.Items) != 1 || page.Body.Items[0].SuggesterUserID != "7" || !page.Body.Page.HasMore || f.profile != "p-owner" {
		t.Fatalf("page=%+v profile=%s", page, f.profile)
	}
	cursor := url.QueryEscape(page.Body.Page.NextCursor)
	rec = do(t, h, http.MethodGet, path+"?limit=1&cursor="+cursor, "", headers)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, path+"?limit=2&cursor="+cursor, "", headers)
	if rec.Code != 400 {
		t.Fatalf("changed limit=%d", rec.Code)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rec = do(t, h, method, path+"/first/vote", "", profileOwner())
		if rec.Code != 403 || f.writes != 0 {
			t.Fatalf("missing room proof=%d writes=%d", rec.Code, f.writes)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rec = do(t, h, method, path+"/first/vote", "", headers)
		if rec.Code != 204 || rec.Body.Len() != 0 || f.vote != (method == http.MethodPost) {
			t.Fatalf("vote=%d %s", rec.Code, rec.Body.String())
		}
	}
	if f.writes != 2 {
		t.Fatalf("writes=%d", f.writes)
	}
}
