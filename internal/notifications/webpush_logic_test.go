package notifications

import (
	"context"
	"errors"
	"testing"
)

func TestBuildNotificationDisplay(t *testing.T) {
	t.Run("episode available", func(t *testing.T) {
		display := BuildNotificationDisplay(webhookTestRow())
		if display.DeliveryID != "01DELIVERY" ||
			display.Title != "Severance" ||
			display.Body != "New episode · S02E01 · Hello, Ms. Cobel" ||
			display.ThreadID != "series:series-123" ||
			display.Category != "episode_available" ||
			display.URL != "/item/episode-456" {
			t.Fatalf("display = %+v", display)
		}
	})

	t.Run("episode body drops placeholder titles and missing parts", func(t *testing.T) {
		season, episode := 10, 1
		cases := []struct {
			name      string
			row       DeliveryRow
			wantTitle string
			wantBody  string
		}{
			{
				name:      "placeholder episode title",
				row:       DeliveryRow{SeriesTitle: "9-1-1", EpisodeTitle: "TBA", SeasonNumber: &season, EpisodeNumber: &episode},
				wantTitle: "9-1-1",
				wantBody:  "New episode · S10E01",
			},
			{
				name:      "no episode numbers",
				row:       DeliveryRow{SeriesTitle: "9-1-1", EpisodeTitle: "Pilot"},
				wantTitle: "9-1-1",
				wantBody:  "New episode · Pilot",
			},
			{
				name:      "no series title",
				row:       DeliveryRow{EpisodeTitle: "Pilot", SeasonNumber: &season, EpisodeNumber: &episode},
				wantTitle: "New episode available",
				wantBody:  "S10E01 · Pilot",
			},
		}
		for _, tc := range cases {
			tc.row.Type = DeliveryTypeEpisodeAvailable
			display := BuildNotificationDisplay(tc.row)
			if display.Title != tc.wantTitle || display.Body != tc.wantBody {
				t.Errorf("%s: title %q body %q, want %q / %q", tc.name, display.Title, display.Body, tc.wantTitle, tc.wantBody)
			}
		}
	})

	t.Run("request declined", func(t *testing.T) {
		row := requestDeclinedTestRow()
		display := BuildNotificationDisplay(row)
		if display.Title != "Dune was declined" ||
			display.Body != "Reason: Already available in 4K" ||
			display.ThreadID != "request:01REQ" ||
			display.Category != "request_declined" ||
			display.URL != "/notifications" {
			t.Fatalf("display = %+v", display)
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		display := BuildNotificationDisplay(DeliveryRow{Delivery: Delivery{ID: "01X", Type: "future.type"}})
		if display.Title != genericNotificationTitle ||
			display.Category != "future_type" ||
			display.URL != "/notifications" {
			t.Fatalf("display = %+v", display)
		}
	})
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
