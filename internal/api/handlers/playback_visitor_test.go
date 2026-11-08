package handlers

import (
	"context"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type visitorRoomFilesFunc func(context.Context, string) (int, error)

func (f visitorRoomFilesFunc) VisitorRoomFile(ctx context.Context, roomID string) (int, error) {
	return f(ctx, roomID)
}

// A visitor starts only its room's selected file, pinned, with client-side
// progress; account sessions are not affected.
func TestCheckVisitorStart(t *testing.T) {
	rooms := visitorRoomFilesFunc(func(_ context.Context, roomID string) (int, error) {
		switch roomID {
		case "room":
			return 7, nil
		default:
			return 0, errors.New("closed")
		}
	})
	pinned, unpinned := false, true
	ok := playback.StartRequestV3{FileID: 7, AllowAlternateVersions: &pinned, ProgressPersistence: playback.ProgressPersistenceClientV3}
	visitor := &auth.Claims{UserID: 1, SessionPurpose: models.SessionPurposeWatchPartyVisitor, WatchPartyRoomID: "room", WatchPartyVisitorID: "v"}
	for _, tc := range []struct {
		name    string
		claims  *auth.Claims
		mutate  func(*playback.StartRequestV3)
		allowed bool
	}{
		{"account session plays anything", &auth.Claims{UserID: 1}, func(r *playback.StartRequestV3) { r.FileID = 99 }, true},
		{"room's file", visitor, nil, true},
		{"another file", visitor, func(r *playback.StartRequestV3) { r.FileID = 8 }, false},
		{"alternate versions allowed", visitor, func(r *playback.StartRequestV3) { r.AllowAlternateVersions = &unpinned }, false},
		{"alternate versions unset", visitor, func(r *playback.StartRequestV3) { r.AllowAlternateVersions = nil }, false},
		{"server-side progress", visitor, func(r *playback.StartRequestV3) { r.ProgressPersistence = playback.ProgressPersistenceServerV3 }, false},
		{"closed room", &auth.Claims{UserID: 1, SessionPurpose: models.SessionPurposeWatchPartyVisitor, WatchPartyRoomID: "gone", WatchPartyVisitorID: "v"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := ok
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			h := &PlaybackHandler{VisitorRooms: rooms}
			err := h.checkVisitorStart(apimw.SetClaims(context.Background(), tc.claims), req)
			if (err == nil) != tc.allowed {
				t.Fatalf("checkVisitorStart() = %v, allowed %v", err, tc.allowed)
			}
		})
	}
}
