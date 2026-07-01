package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `
		CREATE TEMP TABLE notification_deliveries (
			id text PRIMARY KEY,
			release_event_id text,
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			library_id integer,
			series_id text,
			episode_id text,
			type text NOT NULL,
			reason_flags jsonb NOT NULL,
			status text NOT NULL DEFAULT 'delivered',
			read_at timestamptz,
			delivered_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TEMP TABLE episodes (
			content_id text PRIMARY KEY,
			title text,
			season_number integer,
			episode_number integer,
			overview text
		);
		CREATE TEMP TABLE media_items (
			content_id text PRIMARY KEY,
			title text,
			poster_path text,
			poster_thumbhash text,
			poster_source_path text,
			type text,
			year integer,
			overview text,
			genres text[],
			content_rating text,
			rating_imdb double precision,
			rating_tmdb double precision,
			imdb_id text,
			tmdb_id text,
			tvdb_id text
		);
		INSERT INTO media_items (content_id, title, type, genres)
			VALUES ('series-1', 'Severance', 'series', ARRAY[]::text[]);
		INSERT INTO episodes (content_id, title, season_number, episode_number)
			VALUES ('episode-1', 'Hello, Ms. Cobel', 2, 1);
		INSERT INTO notification_deliveries (
			id, release_event_id, user_id, profile_id, library_id, series_id, episode_id,
			type, reason_flags, status
		) VALUES (
			'delivery-1', 'event-1', 42, 'profile-1', 7, 'series-1', 'episode-1',
			'episode.available', '{"favorite":true}', 'delivered'
		);
	`); err != nil {
		t.Fatalf("seed temp tables: %v", err)
	}

	handler := NewNotificationsHandler(&notifications.System{
		Deliveries: notifications.NewDeliveryRepository(pool),
	}, nil)
	router := chi.NewRouter()
	router.Get("/notifications/push/apple/display/{delivery_id}", handler.HandleApplePushDisplay)

	req := httptest.NewRequest(http.MethodGet, "/notifications/push/apple/display/delivery-1", nil)
	req = req.WithContext(apimw.SetProfileID(req.Context(), "profile-1"))
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
	if response.DeliveryID != "delivery-1" ||
		response.Title != "The latest episode of Severance S02E01 just dropped!" ||
		response.Body != "Hello, Ms. Cobel" ||
		response.ThreadID != "series:series-1" ||
		response.Category != "episode_available" ||
		response.URL != "/item/episode-1" {
		t.Fatalf("response = %+v", response)
	}

	req = httptest.NewRequest(http.MethodGet, "/notifications/push/apple/display/delivery-1", nil)
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
