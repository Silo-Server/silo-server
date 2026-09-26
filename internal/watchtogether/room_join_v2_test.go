package watchtogether

import (
	"context"
	"testing"
	"time"
)

type joinLookupRepo struct {
	stubRepo
	codes, tokens []string
}

func (r *joinLookupRepo) GetRoomByCode(_ context.Context, code string) (*Room, error) {
	r.codes = append(r.codes, code)
	room := r.room
	return &room, nil
}
func (r *joinLookupRepo) GetRoomByJoinToken(_ context.Context, token string) (*Room, error) {
	r.tokens = append(r.tokens, token)
	room := r.room
	return &room, nil
}
func TestJoinLookupPrecedenceAndNoMembership(t *testing.T) {
	now := time.Now()
	r := &joinLookupRepo{stubRepo: stubRepo{room: baseRoom(now)}}
	s := newServiceForTest(now, &r.stubRepo, nil, nil, nil)
	s.repo = r
	for range 2 {
		if _, err := s.JoinRoom(t.Context(), JoinInput{Code: " CODE ", JoinToken: " invite "}); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.tokens) != 2 || r.tokens[0] != "invite" || len(r.codes) != 0 {
		t.Fatal("invite precedence changed")
	}
	if _, err := s.JoinRoom(t.Context(), JoinInput{Code: " CODE "}); err != nil {
		t.Fatal(err)
	}
	if len(r.codes) != 1 || r.codes[0] != "CODE" {
		t.Fatal("code normalization changed")
	}
	if len(s.rooms["room-1"].members) != 0 {
		t.Fatal("HTTP join connected membership")
	}
}
