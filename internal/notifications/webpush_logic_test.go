package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBuildWebPushPayload(t *testing.T) {
	t.Run("episode available", func(t *testing.T) {
		raw, err := buildWebPushPayload(webhookTestRow(), "https://cdn.example.com/poster.jpg")
		if err != nil {
			t.Fatalf("build failed: %v", err)
		}
		var payload webPushPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Title != "New episode of Severance" {
			t.Fatalf("unexpected title %q", payload.Title)
		}
		if payload.Body != "S2E1 — Hello, Ms. Cobel" {
			t.Fatalf("unexpected body %q", payload.Body)
		}
		if payload.URL != "/item/episode-456" {
			t.Fatalf("unexpected url %q", payload.URL)
		}
		if payload.Icon != "https://cdn.example.com/poster.jpg" || payload.DeliveryID != "01DELIVERY" {
			t.Fatalf("unexpected payload: %+v", payload)
		}
	})

	t.Run("webhook auto-disable routes to settings", func(t *testing.T) {
		row := DeliveryRow{Delivery: Delivery{ID: "01X", Type: DeliveryTypeWebhookAutoDisabled}}
		raw, err := buildWebPushPayload(row, "")
		if err != nil {
			t.Fatal(err)
		}
		var payload webPushPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.URL != "/settings/notifications" {
			t.Fatalf("unexpected url %q", payload.URL)
		}
	})

	t.Run("unknown types render generically", func(t *testing.T) {
		row := DeliveryRow{Delivery: Delivery{ID: "01Y", Type: "future.type"}}
		raw, err := buildWebPushPayload(row, "")
		if err != nil {
			t.Fatal(err)
		}
		var payload webPushPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Title != "Silo notification" || payload.URL != "/notifications" {
			t.Fatalf("unexpected payload: %+v", payload)
		}
	})
}

func TestWebPushRetrySchedule(t *testing.T) {
	total := time.Duration(0)
	for attempt := 1; attempt < webPushMaxAttempts; attempt++ {
		delay, ok := webPushRetryDelay(attempt)
		if !ok {
			t.Fatalf("schedule ended early at attempt %d", attempt)
		}
		total += delay
	}
	if total != 30*time.Minute {
		t.Fatalf("schedule must span 30m, got %v", total)
	}
	if _, ok := webPushRetryDelay(webPushMaxAttempts); ok {
		t.Fatal("attempt 5 must exhaust the schedule")
	}
}

func TestWebPushSubscribeValidation(t *testing.T) {
	// Validation failures reject before any repository access, so a nil repo
	// is safe here.
	service := newWebPushService(nil, NewSettings(nil), nil)
	ctx := context.Background()

	cases := []struct {
		name     string
		endpoint string
		p256dh   string
		auth     string
	}{
		{"missing endpoint", "", "k", "a"},
		{"missing keys", "https://push.example.com/x", "", ""},
		{"plain http", "http://push.example.com/x", "k", "a"},
		{"loopback", "https://127.0.0.1/x", "k", "a"},
		{"v4-mapped loopback", "https://[::ffff:127.0.0.1]/x", "k", "a"},
		{"private network", "https://192.168.1.10/x", "k", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Subscribe(ctx, 1, "p1", tc.endpoint, tc.p256dh, tc.auth, "test")
			if !errors.Is(err, ErrWebPushInvalid) {
				t.Fatalf("Subscribe = %v, want ErrWebPushInvalid", err)
			}
		})
	}
}
