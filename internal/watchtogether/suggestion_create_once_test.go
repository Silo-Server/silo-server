package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingCreator struct {
	stubSuggestions
	input        Suggestion
	calls, lists int
	listErr      error
}

func (r *recordingCreator) CreateSuggestionOnce(_ context.Context, input Suggestion) (*Suggestion, bool, error) {
	r.calls++
	r.input = input
	return &input, r.calls == 1, nil
}
func (r *recordingCreator) ListSuggestions(context.Context, string, int, string) ([]Suggestion, error) {
	r.lists++
	return []Suggestion{r.input}, r.listErr
}
func TestSuggestionCreationBroadcastOnlyOnInsertion(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivered", true: "postcommit_list_failure"}[failed], func(t *testing.T) {
			now := time.Now()
			s := newServiceForTest(now, &stubRepo{room: baseRoom(now)}, nil, nil, nil)
			store := new(recordingCreator)
			if failed {
				store.listErr = errors.New("list unavailable")
			}
			s.suggestions = store
			conn := new(recordingConn)
			s.rooms["room-1"].members["host"] = &memberState{userID: 7, profileID: "host", connection: conn}
			input := CreateSuggestionInput{ContentID: "content", ContentType: "movie", Title: "Title"}
			_, err := s.CreateSuggestionWithIdentity(t.Context(), "room-1", "request", 7, "host", input)
			if failed != (err != nil) {
				t.Fatalf("first: %v", err)
			}
			id, err := s.CreateSuggestionWithIdentity(t.Context(), "room-1", "request", 7, "host", input)
			if err != nil || id != "request" || store.calls != 2 || store.lists != 1 || store.input.CreatedAt != now || store.input.SuggesterUserID != 7 || store.input.SuggesterProfileID != "host" {
				t.Fatalf("retry %s %v %+v", id, err, store)
			}
			want := 1
			if failed {
				want = 0
			}
			if len(conn.payloads) != want {
				t.Fatalf("broadcasts %d", len(conn.payloads))
			}
		})
	}
}
