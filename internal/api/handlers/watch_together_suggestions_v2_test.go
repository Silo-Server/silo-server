package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func TestSuggestionRoomProofBindsAllAuthority(t *testing.T) {
	service := watchtogether.NewRoomTokenService("synthetic-test-secret", time.Minute)
	token, _, err := service.Mint(watchtogether.RoomTokenClaims{RoomID: "room", UserID: 7, ProfileID: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	h := &WatchTogetherHandler{Service: new(watchtogether.Service), TokenService: service}
	for _, tc := range []struct {
		room           string
		user           int
		profile, token string
		valid          bool
	}{{"room", 7, "profile", token, true}, {"other", 7, "profile", token, false}, {"room", 8, "profile", token, false}, {"room", 7, "other", token, false}, {"room", 7, "profile", "", false}} {
		err = h.CheckSuggestionRoomProof(tc.room, tc.user, tc.profile, tc.token)
		if (err == nil) != tc.valid {
			t.Fatalf("proof valid=%v err=%v", tc.valid, err)
		}
	}
	h.TokenService = nil
	if h.CheckSuggestionRoomProof("room", 7, "profile", token) == nil {
		t.Fatal("missing proof service accepted")
	}
}
