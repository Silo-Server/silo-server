package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type transportCapabilityBearerValidator struct{}

func (transportCapabilityBearerValidator) ValidateToken(string) (*auth.Claims, error) {
	return nil, errors.New("expired access token")
}

type transportCapabilitySessionValidator struct{}

func (transportCapabilitySessionValidator) IsValid(context.Context, string) (bool, error) {
	return false, nil
}

func TestVerifiedStreamCardFromRequestUsesHeaderCapabilityForReconstruction(t *testing.T) {
	const secret = "transport-capability-reconstruction-secret"
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:       "playback-1",
		MediaPath:       "/media/movie.mkv",
		PlayMethod:      "transcode",
		TargetCodec:     "h264",
		OutputSubdir:    "playback-1-plan-1",
		UserID:          7,
		ProfileID:       "profile-1",
		MediaFileID:     42,
		TargetRes:       "1920x1080",
		SegmentDuration: 6,
	}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	authMiddleware := apimw.NewAuthMiddleware(
		transportCapabilityBearerValidator{},
		transportCapabilitySessionValidator{},
		nil,
		nil,
	)
	router := chi.NewRouter()
	router.With(authMiddleware.RequireTransportAuth(secret)).Get("/stream/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		card, claims := verifiedStreamCardFromRequest(r, "playback-1", secret)
		if card == nil || claims == nil {
			t.Fatal("header capability did not provide a reconstruction recipe")
		}
		if card.SessionID != "playback-1" || card.UserID != 7 || card.MediaFileID != 42 || card.InputPath != "/media/movie.mkv" || card.TargetCodecVideo != "h264" {
			t.Fatalf("reconstruction card = %#v", card)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/stream/playback-1", nil)
	req.Header.Set(streamtoken.Header, token)
	req.Header.Set("Authorization", "Bearer expired-access-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
