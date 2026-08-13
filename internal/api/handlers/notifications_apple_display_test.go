package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHandleApplePushDisplayDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run DB-backed notification display handler test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	id := func(name string) string { return name + "-" + suffix }
	profileID, seriesID := id("profile"), id("series")
	episodeID, deliveryID, eventID := id("episode"), id("delivery"), id("event")

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// Deliveries first: their release_event_id is ON DELETE SET NULL, so
		// dropping the event first would orphan rather than remove them.
		// episodes cascades from media_items.
		for _, stmt := range []struct {
			query string
			arg   string
		}{
			{`DELETE FROM notification_deliveries WHERE profile_id = $1`, profileID},
			{`DELETE FROM release_events WHERE id = $1`, eventID},
			{`DELETE FROM media_items WHERE content_id = $1`, seriesID},
		} {
			if _, err := pool.Exec(cleanupCtx, stmt.query, stmt.arg); err != nil {
				t.Errorf("cleanup %q: %v", stmt.query, err)
			}
		}
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, title, type, genres)
		VALUES ($1, 'Severance', 'series', ARRAY[]::text[])`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, title, season_number, episode_number)
		VALUES ($1, $2, 'Hello, Ms. Cobel', 2, 1)`, episodeID, seriesID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	// release_events_kind_shape_check requires an episode event to carry the
	// full series/episode identity, and release_events_ordinals_check requires
	// episode_key to equal season*1000000 + episode.
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_events (
			id, library_id, available_at, dedupe_key,
			kind, series_id, episode_id, season_number, episode_number, episode_key
		) VALUES ($1, 7, now(), $1, 'episode', $2, $3, 2, 1, 2000001)`,
		eventID, seriesID, episodeID); err != nil {
		t.Fatalf("seed release event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_deliveries (
			id, release_event_id, user_id, profile_id, library_id, series_id, episode_id,
			type, reason_flags, status
		) VALUES ($1, $2, 42, $3, 7, $4, $5, 'episode.available', '{"favorite":true}', 'delivered')`,
		deliveryID, eventID, profileID, seriesID, episodeID); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}

	handler := NewNotificationsHandler(&notifications.System{
		Deliveries: notifications.NewDeliveryRepository(pool),
	}, nil)
	router := chi.NewRouter()
	router.Get("/notifications/push/apple/display/{delivery_id}", handler.HandleApplePushDisplay)

	req := httptest.NewRequest(http.MethodGet, "/notifications/push/apple/display/"+deliveryID, nil)
	req = req.WithContext(apimw.SetProfileID(req.Context(), profileID))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var response notifications.NotificationDisplay
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.DeliveryID != deliveryID ||
		response.Title != "The latest episode of Severance S02E01 just dropped!" ||
		response.Body != "Hello, Ms. Cobel" ||
		response.ThreadID != "series:"+seriesID ||
		response.Category != "episode_available" ||
		response.URL != "/item/"+episodeID {
		t.Fatalf("response = %+v", response)
	}

	req = httptest.NewRequest(http.MethodGet, "/notifications/push/apple/display/"+deliveryID, nil)
	req = req.WithContext(apimw.SetProfileID(req.Context(), "other-profile"))
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-profile status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cross-profile Cache-Control = %q", got)
	}
	if strings.Contains(rr.Body.String(), "Severance") {
		t.Fatalf("cross-profile response leaked display metadata: %s", rr.Body.String())
	}
}
