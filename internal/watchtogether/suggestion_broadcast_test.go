package watchtogether

import (
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
)

type suggestionRead struct {
	roomID    string
	userID    int
	profileID string
}

type broadcastSuggestions struct {
	stubSuggestions
	votes      map[string]map[string]bool
	reads      []suggestionRead
	beforeRead func()
	readErr    error
}

func (s *broadcastSuggestions) ListSuggestions(_ context.Context, roomID string, userID int, profileID string) ([]Suggestion, error) {
	s.reads = append(s.reads, suggestionRead{roomID, userID, profileID})
	if s.beforeRead != nil {
		s.beforeRead()
	}
	rows := slices.Clone(s.ordered)
	for i := range rows {
		rows[i].VotedByMe = s.votes[rows[i].ID][buildMemberKey(userID, profileID)]
	}
	slices.SortStableFunc(rows, func(a, b Suggestion) int { return cmp.Compare(b.VoteCount, a.VoteCount) })
	return rows, s.readErr
}

func (s *broadcastSuggestions) CreateSuggestion(_ context.Context, row Suggestion) (*Suggestion, error) {
	s.ordered = append(s.ordered, row)
	return &row, nil
}

func (s *broadcastSuggestions) DeleteSuggestion(_ context.Context, id string) error {
	s.ordered = slices.DeleteFunc(s.ordered, func(row Suggestion) bool { return row.ID == id })
	delete(s.votes, id)
	return nil
}

func (s *broadcastSuggestions) AddVote(_ context.Context, id string, userID int, profileID string) error {
	if s.votes[id] == nil {
		s.votes[id] = make(map[string]bool)
	}
	s.votes[id][buildMemberKey(userID, profileID)] = true
	for i := range s.ordered {
		if s.ordered[i].ID == id {
			s.ordered[i].VoteCount++
		}
	}
	return nil
}

func (s *broadcastSuggestions) RemoveVote(_ context.Context, id string, userID int, profileID string) error {
	delete(s.votes[id], buildMemberKey(userID, profileID))
	for i := range s.ordered {
		if s.ordered[i].ID == id {
			s.ordered[i].VoteCount--
		}
	}
	return nil
}

func addSuggestionViewers(s *Service, roomID string) []*recordingConn {
	var conns []*recordingConn
	for _, profile := range []string{"host", "guest", "another"} {
		conn := new(recordingConn)
		s.rooms[roomID].members[buildMemberKey(7, profile)] = &memberState{userID: 7, profileID: profile, connection: conn}
		conns = append(conns, conn)
	}
	return conns
}

func suggestionEvent(roomID string) cache.Event {
	return cache.Event{Type: clusterSuggestionsEventType, Payload: `{"source":"other","room_id":"` + roomID + `"}`}
}

func TestSuggestionMutationsBroadcastCommonRowsAcrossNodes(t *testing.T) {
	local, repo := newVoteRoomService(t, RoomSelectionModeVote, nil)
	remote := newServiceForTest(local.now(), repo, nil, nil, nil)
	t.Cleanup(remote.Close)
	store := &broadcastSuggestions{votes: make(map[string]map[string]bool)}
	local.suggestions, remote.suggestions = store, store
	localConns := addSuggestionViewers(local, repo.room.ID)
	remoteConns := addSuggestionViewers(remote, repo.room.ID)
	allConns := append(slices.Clone(localConns), remoteConns...)
	var suggestionID string
	steps := []struct {
		name   string
		mutate func() ([]Suggestion, error)
		count  int
		votes  int
		host   bool
		guest  bool
	}{
		{"create", func() ([]Suggestion, error) {
			return local.CreateSuggestion(t.Context(), repo.room.ID, 7, "host", CreateSuggestionInput{ContentID: "movie", ContentType: "movie", Title: "Movie"})
		}, 1, 0, false, false},
		{"host vote", func() ([]Suggestion, error) {
			return local.Vote(t.Context(), repo.room.ID, suggestionID, 7, "host")
		}, 1, 1, true, false},
		{"guest vote", func() ([]Suggestion, error) {
			return local.Vote(t.Context(), repo.room.ID, suggestionID, 7, "guest")
		}, 1, 2, true, true},
		{"host unvote", func() ([]Suggestion, error) {
			return local.Unvote(t.Context(), repo.room.ID, suggestionID, 7, "host")
		}, 1, 1, false, true},
		{"delete", func() ([]Suggestion, error) {
			return local.DeleteSuggestion(t.Context(), repo.room.ID, suggestionID, 7, "host")
		}, 0, 0, false, false},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			for _, conn := range allConns {
				conn.payloads = nil
			}
			rows, err := step.mutate()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != step.count {
				t.Fatalf("mutation returned %d suggestions, want %d", len(rows), step.count)
			}
			if len(rows) > 0 {
				suggestionID = rows[0].ID
				if rows[0].VoteCount != step.votes {
					t.Fatalf("vote_count = %d, want %d", rows[0].VoteCount, step.votes)
				}
			}
			common := slices.Clone(rows)
			for i := range common {
				common[i].VotedByMe = false
			}
			store.reads = nil
			remote.handleClusterEvent(suggestionEvent(repo.room.ID))
			if !reflect.DeepEqual(store.reads, []suggestionRead{{roomID: repo.room.ID}}) {
				t.Fatalf("cluster reads = %+v, want one unpersonalized room query", store.reads)
			}
			for _, conn := range allConns {
				if len(conn.payloads) != 1 {
					t.Fatalf("viewer received %d updates, want 1", len(conn.payloads))
				}
				if !reflect.DeepEqual(conn.payloads, localConns[0].payloads) {
					t.Fatalf("local/remote viewer payload mismatch: %+v != %+v", conn.payloads, localConns[0].payloads)
				}
				if conn.payloads[0]["type"] != suggestionsUpdateType {
					t.Fatalf("unexpected message: %+v", conn.payloads[0])
				}
				if socketRows := conn.payloads[0]["suggestions"].([]Suggestion); !slices.Equal(socketRows, common) {
					t.Fatalf("socket rows = %+v, want common rows %+v", socketRows, common)
				}
			}
			for profile, voted := range map[string]bool{"host": step.host, "guest": step.guest, "another": false} {
				personalized, err := local.ListSuggestions(t.Context(), repo.room.ID, 7, profile)
				if err != nil || len(personalized) != step.count {
					t.Fatalf("%s HTTP service read = %+v, %v", profile, personalized, err)
				}
				if len(personalized) > 0 && (personalized[0].VotedByMe != voted || personalized[0].VoteCount != step.votes) {
					t.Fatalf("%s personalized rows = %+v, want voted=%v, count=%d", profile, personalized, voted, step.votes)
				}
			}
		})
	}
}

func TestClusterSuggestionsSkipRoomsWithoutViewers(t *testing.T) {
	for _, state := range []string{"absent", "ended", "disconnected"} {
		t.Run(state, func(t *testing.T) {
			service, repo := newVoteRoomService(t, RoomSelectionModeVote, nil)
			conns := addSuggestionViewers(service, repo.room.ID)
			store := &broadcastSuggestions{}
			service.suggestions = store
			switch state {
			case "absent":
				delete(service.rooms, repo.room.ID)
			case "ended":
				service.rooms[repo.room.ID].room.Phase = RoomPhaseEnded
			case "disconnected":
				for _, member := range service.rooms[repo.room.ID].members {
					member.connection = nil
				}
			}
			service.handleClusterEvent(suggestionEvent(repo.room.ID))
			if len(store.reads) != 0 {
				t.Fatalf("room without viewers queried suggestions: %+v", store.reads)
			}
			for _, conn := range conns {
				if len(conn.payloads) != 0 {
					t.Fatalf("room without viewers received updates: %+v", conn.payloads)
				}
			}
		})
	}
}

func TestClusterSuggestionsRecheckRoomAndConnectionsAfterQuery(t *testing.T) {
	for _, change := range []string{"room removed", "room ended", "room replaced", "connection disconnected", "connection replaced", "query failed"} {
		t.Run(change, func(t *testing.T) {
			service, repo := newVoteRoomService(t, RoomSelectionModeVote, nil)
			conn, replacement := new(recordingConn), new(recordingConn)
			member := &memberState{userID: 7, profileID: "host", connection: conn}
			live := service.rooms[repo.room.ID]
			live.members["host"] = member
			store := &broadcastSuggestions{stubSuggestions: stubSuggestions{ordered: []Suggestion{{ID: "suggestion"}}}}
			service.suggestions = store
			store.beforeRead = func() {
				if !service.mu.TryLock() {
					t.Fatal("suggestion query ran while holding the room mutex")
				}
				defer service.mu.Unlock()
				switch change {
				case "room removed":
					delete(service.rooms, repo.room.ID)
				case "room ended":
					live.room.Phase = RoomPhaseEnded
				case "room replaced":
					service.rooms[repo.room.ID] = &liveRoom{room: repo.room, members: map[string]*memberState{"host": member}}
				case "connection disconnected":
					member.connection = nil
				case "connection replaced":
					member.connection = replacement
				case "query failed":
					store.readErr = errors.New("query failed")
				}
			}
			service.handleClusterEvent(suggestionEvent(repo.room.ID))
			if len(store.reads) != 1 || len(conn.payloads) != 0 {
				t.Fatalf("reads = %+v, stale connection updates = %+v", store.reads, conn.payloads)
			}
			wantReplacementUpdates := 0
			if change == "connection replaced" {
				wantReplacementUpdates = 1
			}
			if len(replacement.payloads) != wantReplacementUpdates {
				t.Fatalf("replacement updates = %d, want %d", len(replacement.payloads), wantReplacementUpdates)
			}
		})
	}
}
