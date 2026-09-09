package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/google/uuid"
)

func TestPostgresUserDataWithProductionDecorator(t *testing.T) {
	pool := newCompatTestPool(t)
	var userID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "compat-progress-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	provider := notifications.WrapUserStoreProvider(pgstore.NewPostgresProvider(pool), &notifications.System{})
	store, err := provider.ForUser(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	profile := uuid.NewString()
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: profile, Name: "Profile"}); err != nil {
		t.Fatal(err)
	}
	service := &directUserDataService{storeProvider: provider, watchState: watchstate.NewService(provider)}
	codec := NewResourceIDCodec()
	id := codec.EncodeStringID(EncodedIDItem, "compat-progress-item")
	handler := NewUserDataHandler(&stubContentService{detail: &upstreamItemDetail{ContentID: "compat-progress-item", Type: "movie", Runtime: 10}}, service, codec, &config.Config{})
	session := &Session{Token: uuid.NewString(), StreamAppUserID: userID, ProfileID: profile, PseudoUserID: uuid.New()}
	for _, body := range []string{
		`{"PlaybackPositionTicks":1230000000,"Played":false,"LastPlayedDate":"2024-01-02T03:04:05Z"}`,
		`{"PlaybackPositionTicks":1240000000,"Played":false,"LastPlayedDate":"2024-01-02T03:04:05Z"}`,
	} {
		rec := httptest.NewRecorder()
		handler.HandleUpdateUserData(rec, viewerRequest("POST", "/", body, "itemId", id, session))
		var dto itemUserDataDTO
		_ = json.Unmarshal(rec.Body.Bytes(), &dto)
		if rec.Code != 200 || dto.Played || dto.PlaybackPositionTicks == 0 || dto.LastPlayedDate != "2024-01-02T03:04:05Z" {
			t.Fatalf("explicit update status=%d dto=%+v body=%s", rec.Code, dto, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	handler.HandleMarkPlayed(rec, viewerRequest("POST", "/?datePlayed=2024-01-02T03:04:05Z", "", "itemId", id, session))
	var dto itemUserDataDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != 200 || !dto.Played || dto.LastPlayedDate != "2024-01-02T03:04:05Z" {
		t.Fatalf("dated mark status=%d dto=%+v body=%s", rec.Code, dto, rec.Body.String())
	}
}
