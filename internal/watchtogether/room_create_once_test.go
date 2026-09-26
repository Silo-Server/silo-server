package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type roomCreationStore struct {
	stubRepo
	inputs       []Room
	result       Room
	err          error
	beforeReturn func()
}

func (r *roomCreationStore) CreateRoomOnce(_ context.Context, input Room) (*Room, bool, error) {
	r.inputs = append(r.inputs, input)
	if r.beforeReturn != nil {
		r.beforeReturn()
	}
	result := r.result
	return &result, len(r.inputs) == 1, r.err
}
func TestRoomCreationKeepsMembersAndCurrentReceipt(t *testing.T) {
	now := time.Now()
	base := baseRoom(now)
	base.IsPaused = true
	base.ID = "00000000-0000-4000-8000-000000000001"
	repo := &roomCreationStore{result: base}
	repo.result.Code = "retained"
	repo.result.JoinToken = "retained-invite"
	repo.result.Generation = 20
	repo.result.AnchorPositionSeconds = 99
	s := newServiceForTest(now, &stubRepo{room: base}, nil, nil, nil)
	s.repo = repo
	live := s.rooms[base.ID]
	if live == nil {
		live = &liveRoom{room: base, members: map[string]*memberState{}}
		s.rooms[base.ID] = live
	}
	conn := new(recordingConn)
	member := &memberState{userID: base.HostUserID, profileID: base.HostProfileID, connection: conn, sessionID: "session", isReady: true}
	live.members[buildMemberKey(base.HostUserID, base.HostProfileID)] = member
	input := CreateRoomInput{HostUserID: base.HostUserID, HostProfileID: base.HostProfileID, SelectionMode: RoomSelectionModeHostPick}
	for range 2 {
		snapshot, err := s.CreateRoomWithIdentity(t.Context(), base.ID, input)
		if err != nil || snapshot.Code != "retained" || snapshot.Generation != 20 || snapshot.AnchorPositionSeconds != 99 || snapshot.MemberCount != 1 || snapshot.AttachedSessionID != "session" {
			t.Fatalf("snapshot %+v: %v", snapshot, err)
		}
	}
	if s.rooms[base.ID] != live || !member.isReady || member.connection != conn || len(conn.payloads) != 0 {
		t.Fatal("replay replaced members/readiness or dispatched")
	}
	for _, candidate := range repo.inputs {
		if candidate.ID != base.ID || candidate.HostUserID != base.HostUserID || candidate.HostProfileID != base.HostProfileID || candidate.SelectionMode != input.SelectionMode || candidate.Code == "" || candidate.JoinToken == "" || candidate.Code == "retained" || candidate.Generation != 1 || candidate.CreatedAt != now {
			t.Fatalf("candidate %+v", candidate)
		}
	}
	repo.beforeReturn = func() { live.room.Generation = 30; live.room.AnchorPositionSeconds = 123 }
	snapshot, err := s.CreateRoomWithIdentity(t.Context(), base.ID, input)
	if err != nil || snapshot.Generation != 30 || snapshot.AnchorPositionSeconds != 123 {
		t.Fatalf("late receipt %+v %v", snapshot, err)
	}
	repo.beforeReturn = func() { live.room.Phase = RoomPhaseEnded }
	if _, err = s.CreateRoomWithIdentity(t.Context(), base.ID, input); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("closed %v", err)
	}
	delete(s.rooms, base.ID)
	repo.beforeReturn = nil
	if _, err = s.CreateRoomWithIdentity(t.Context(), base.ID, input); err != nil {
		t.Fatal(err)
	}
	if s.rooms[base.ID] != nil {
		t.Fatal("creation hydrated membership")
	}
	repo.err = ErrRoomCreationIdentityConflict
	if _, err = s.CreateRoomWithIdentity(t.Context(), base.ID, input); !errors.Is(err, repo.err) {
		t.Fatal(err)
	}
	calls := len(repo.inputs)
	if _, err = s.CreateRoomWithIdentity(t.Context(), "invalid", input); !errors.Is(err, ErrInvalidSelection) || len(repo.inputs) != calls {
		t.Fatal("invalid dispatched", err)
	}
}

func TestRoomCreationRetriesOnlyDefiniteCredentialCollision(t *testing.T) {
	table := "watch_together_rooms"
	for _, tc := range []struct {
		name    string
		err     error
		retries bool
	}{
		{"code", &pgconn.PgError{Code: "23505", TableName: table, ConstraintName: "watch_together_rooms_code_key"}, true},
		{"invite", &pgconn.PgError{Code: "23505", TableName: table, ConstraintName: "watch_together_rooms_join_token_key"}, true},
		{"other_unique", &pgconn.PgError{Code: "23505", TableName: table, ConstraintName: "watch_together_rooms_pkey"}, false},
		{"uncertain", errors.New("connection lost"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			repo := &roomCreationStore{err: tc.err}
			s := newServiceForTest(now, &stubRepo{room: baseRoom(now)}, nil, nil, nil)
			s.repo = repo
			id := "00000000-0000-4000-8000-000000000002"
			_, err := s.CreateRoomWithIdentity(t.Context(), id, CreateRoomInput{HostUserID: 7, HostProfileID: "host", SelectionMode: RoomSelectionModeVote})
			if !errors.Is(err, tc.err) {
				t.Fatal(err)
			}
			want := 1
			if tc.retries {
				want = 3
			}
			if len(repo.inputs) != want {
				t.Fatalf("calls %d", len(repo.inputs))
			}
			for i, candidate := range repo.inputs {
				if candidate.ID != id || candidate.SelectionMode != RoomSelectionModeVote {
					t.Fatal("changed binding")
				}
				if i > 0 && (candidate.Code == repo.inputs[0].Code || candidate.JoinToken == repo.inputs[0].JoinToken) {
					t.Fatal("credentials not regenerated")
				}
			}
		})
	}
}
