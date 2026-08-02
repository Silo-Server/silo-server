package jellycompat

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestDirectAndRemuxRecipeCardsPreserveUpstreamSessionIdentity(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	upstream := &playback.Session{
		ID:         "upstream-1",
		Generation: "f09e4a04-b8ab-4531-88cf-c3a2a0717789",
		StartedAt:  startedAt,
	}
	h := &PlaybackHandler{}
	ps := &PlaybackSession{
		UpstreamSessionID:         upstream.ID,
		UpstreamSessionGeneration: upstream.Generation,
		UpstreamStartedAt:         upstream.StartedAt,
	}
	cs := &Session{StreamAppUserID: 7, ProfileID: "profile-1"}
	source := PlaybackMediaSource{FileID: 42}

	for _, method := range []string{"direct", "remux"} {
		card := h.upstreamRecipeCard(ps, cs, source, method)
		if card.SessionGeneration != upstream.Generation || !card.StartedAt.Equal(startedAt) {
			t.Fatalf("%s identity = (%q, %s), want (%q, %s)", method, card.SessionGeneration, card.StartedAt, upstream.Generation, startedAt)
		}
	}
}
