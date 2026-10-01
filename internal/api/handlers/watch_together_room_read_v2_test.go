package handlers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

func TestRoomReadRejectsProofBeforeSnapshot(t *testing.T) {
	tokens := watchtogether.NewRoomTokenService("synthetic-room-read-secret", time.Minute)
	proof, _, err := tokens.Mint(watchtogether.RoomTokenClaims{RoomID: "room", UserID: 7, ProfileID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	h := &WatchTogetherHandler{Service: new(watchtogether.Service), TokenService: tokens}
	for _, tc := range []struct {
		room           string
		user           int
		profile, proof string
	}{{"other", 7, "host", proof}, {"room", 8, "host", proof}, {"room", 7, "guest", proof}, {"room", 7, "host", ""}} {
		_, _, err = h.ReadWatchTogetherRoom(t.Context(), tc.room, tc.user, tc.profile, tc.proof)
		if err == nil {
			t.Fatal("invalid proof reached snapshot")
		}
	}
	h.TokenService = nil
	if _, _, err = h.ReadWatchTogetherRoom(t.Context(), "room", 7, "host", proof); err == nil {
		t.Fatal("absent token service accepted")
	}
	h.TokenService = tokens
	response, err := h.buildRoomResponse(t.Context(), watchtogether.Snapshot{RoomID: "room"}, 7, "host")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tokens.Validate(response.RoomAccessToken)
	if err != nil || claims.RoomID != "room" || claims.UserID != 7 || claims.ProfileID != "host" {
		t.Fatal("renewal lost binding")
	}
}
