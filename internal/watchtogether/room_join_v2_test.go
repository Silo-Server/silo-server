package watchtogether

import (
	"context"
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
