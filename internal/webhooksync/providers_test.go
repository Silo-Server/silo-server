package webhooksync

import (
	"context"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEmbyProviderParseWebhook(t *testing.T) {
	t.Parallel()

	provider := NewEmbyProvider()
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{
		"Event": "playback.stop",
		"Date": "2026-04-07T12:00:00Z",
		"User": { "Id": "user-1", "Name": "Alice" },
		"Item": {
			"Id": "item-1",
			"Name": "Pilot",
			"Type": "Episode",
			"SeriesName": "The Show",
			"ProductionYear": 2024,
			"IndexNumber": 1,
			"ParentIndexNumber": 2,
			"RunTimeTicks": 30000000000,
			"ProviderIds": { "Tvdb": "tvdb-1" }
		},
		"PlaybackInfo": {
			"PlayedToCompletion": true,
			"PositionTicks": 30000000000
		}
	}`))

	event, err := provider.ParseWebhook(context.Background(), &Connection{ServerName: "Emby"}, req)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if event == nil || !event.Apply {
		t.Fatalf("expected event to apply")
	}
	if event.Action != ActionImportProgress {
		t.Fatalf("unexpected action: %q", event.Action)
	}
	if event.UserID != "user-1" || event.UserName != "Alice" {
		t.Fatalf("unexpected external user: %#v", event)
	}
	if event.Record.Kind != "episode" || event.Record.SeriesTitle != "The Show" {
		t.Fatalf("unexpected record: %#v", event.Record)
	}
	if !event.Completed {
		t.Fatalf("expected completed event")
	}
}

func TestEmbyProviderParseWebhookMultipart(t *testing.T) {
	t.Parallel()

	provider := NewEmbyProvider()
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormField("data")
	if err != nil {
		t.Fatalf("CreateFormField() error = %v", err)
	}
	if _, err := part.Write([]byte(`{
		"Event": "playback.stop",
		"Date": "2026-04-07T12:00:00Z",
		"User": { "Id": "user-1", "Name": "Alice" },
		"Item": {
			"Id": "item-1",
			"Name": "Movie",
			"Type": "Movie",
			"ProductionYear": 2024,
			"RunTimeTicks": 30000000000,
			"ProviderIds": { "Imdb": "tt123" }
		},
		"PlaybackInfo": {
			"PlayedToCompletion": false,
			"PositionTicks": 12000000000
		}
	}`)); err != nil {
		t.Fatalf("part.Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", writer.FormDataContentType())

	event, err := provider.ParseWebhook(context.Background(), &Connection{ServerName: "Emby"}, req)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if event == nil || !event.Apply {
		t.Fatalf("expected multipart event to apply")
	}
	if event.Action != ActionImportProgress {
		t.Fatalf("unexpected action: %q", event.Action)
	}
	if event.Record.Kind != "movie" || event.Record.IMDbID != "tt123" {
		t.Fatalf("unexpected record: %#v", event.Record)
	}
}

func TestEmbyProviderParseWebhookMarkUnplayed(t *testing.T) {
	t.Parallel()

	provider := NewEmbyProvider()
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{
		"Event": "item.markunplayed",
		"Date": "2026-04-07T12:00:00Z",
		"User": { "Id": "user-1", "Name": "Alice" },
		"Item": {
			"Id": "item-1",
			"Name": "Movie",
			"Type": "Movie",
			"ProductionYear": 2024,
			"ProviderIds": { "Tmdb": "1" }
		}
	}`))

	event, err := provider.ParseWebhook(context.Background(), &Connection{ServerName: "Emby"}, req)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if event == nil || !event.Apply {
		t.Fatalf("expected event to apply")
	}
	if event.Action != ActionMarkUnplayed {
		t.Fatalf("unexpected action: %q", event.Action)
	}
	if event.Completed {
		t.Fatalf("mark unplayed should not be completed")
	}
}

func TestEmbyProviderParseWebhookFavorites(t *testing.T) {
	t.Parallel()

	provider := NewEmbyProvider()
	tests := []struct {
		name      string
		eventName string
		want      string
	}{
		{name: "add", eventName: "item.favorited", want: ActionAddFavorite},
		{name: "remove", eventName: "item.unfavorite", want: ActionRemoveFavorite},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{
				"Event": "`+tc.eventName+`",
				"Date": "2026-04-07T12:00:00Z",
				"User": { "Id": "user-1", "Name": "Alice" },
				"Item": {
					"Id": "item-1",
					"Name": "Movie",
					"Type": "Movie",
					"ProductionYear": 2024,
					"ProviderIds": { "Imdb": "tt123" }
				}
			}`))

			event, err := provider.ParseWebhook(context.Background(), &Connection{ServerName: "Emby"}, req)
			if err != nil {
				t.Fatalf("ParseWebhook() error = %v", err)
			}
			if event == nil || !event.Apply {
				t.Fatalf("expected event to apply")
			}
			if event.Action != tc.want {
				t.Fatalf("unexpected action: %q", event.Action)
			}
			if event.Record.IMDbID != "tt123" {
				t.Fatalf("unexpected record: %#v", event.Record)
			}
		})
	}
}

func TestEmbyProviderParseWebhookItemRateAsToggleFavorite(t *testing.T) {
	t.Parallel()

	provider := NewEmbyProvider()
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{
		"Event": "item.rate",
		"Date": "2026-04-07T12:00:00Z",
		"User": { "Id": "user-1", "Name": "Alice" },
		"Item": {
			"Id": "item-1",
			"Name": "The Show",
			"Type": "Series",
			"ProductionYear": 2024,
			"ProviderIds": { "Tvdb": "123" }
		}
	}`))

	event, err := provider.ParseWebhook(context.Background(), &Connection{ServerName: "Emby"}, req)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if event == nil || !event.Apply {
		t.Fatalf("expected event to apply")
	}
	if event.Action != ActionToggleFavorite {
		t.Fatalf("unexpected action: %q", event.Action)
	}
	if event.Record.Kind != "series" || event.Record.TVDBID != "123" {
		t.Fatalf("unexpected record: %#v", event.Record)
	}
}

func TestDecodeEmbyPayloadFormEncoded(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(
		"POST",
		"/webhook",
		strings.NewReader("data="+url.QueryEscape(`{"Event":"playback.stop","Date":"2026-04-07T12:00:00Z","User":{"Id":"user-1","Name":"Alice"},"Item":{"Id":"item-1","Name":"Movie","Type":"Movie"}}`)),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var payload embyWebhookPayload
	if err := decodeEmbyPayload(req, &payload); err != nil {
		t.Fatalf("decodeEmbyPayload() error = %v", err)
	}
	if payload.Event != "playback.stop" || payload.User.ID != "user-1" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestJellyfinProviderParseWebhookIgnoresNonStop(t *testing.T) {
	t.Parallel()

	provider := NewJellyfinProvider()
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{
		"provider": "jellyfin",
		"notification_type": "PlaybackProgress",
		"timestamp": "2026-04-07T12:00:00Z",
		"user": { "id": "user-1", "name": "Alice" },
		"item": { "id": "item-1", "type": "Movie", "name": "Movie", "provider_ids": {} },
		"playback": { "position_ticks": 1, "played_to_completion": false, "runtime_ticks": 2 }
	}`))

	event, err := provider.ParseWebhook(context.Background(), &Connection{}, req)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if event == nil || event.Apply {
		t.Fatalf("expected ignored event, got %#v", event)
	}
}

func TestEmbyWebhookAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		eventName string
		want      string
		ok        bool
	}{
		{eventName: "playback.stop", want: ActionImportProgress, ok: true},
		{eventName: "item.markplayed", want: ActionImportProgress, ok: true},
		{eventName: "item.markunplayed", want: ActionMarkUnplayed, ok: true},
		{eventName: "item.rate", want: ActionToggleFavorite, ok: true},
		{eventName: "item.addedtofavorites", want: ActionAddFavorite, ok: true},
		{eventName: "item.removedfromfavorites", want: ActionRemoveFavorite, ok: true},
		{eventName: "user.created", ok: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.eventName, func(t *testing.T) {
			t.Parallel()

			got, ok := embyWebhookAction(tc.eventName)
			if ok != tc.ok {
				t.Fatalf("embyWebhookAction(%q) ok = %v, want %v", tc.eventName, ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("embyWebhookAction(%q) = %q, want %q", tc.eventName, got, tc.want)
			}
		})
	}
}

func TestShouldSkipEvent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	state := &ItemState{
		LastEventAt:        now,
		LastCompleted:      false,
		LastPositionSecond: 120,
	}

	if !shouldSkipEvent(state, &CanonicalEvent{
		OccurredAt:      now.Add(-time.Minute),
		Completed:       false,
		PositionSeconds: 121,
	}) {
		t.Fatalf("expected stale low-progress event to be skipped")
	}

	if shouldSkipEvent(state, &CanonicalEvent{
		OccurredAt:      now.Add(-time.Minute),
		Completed:       true,
		PositionSeconds: 121,
	}) {
		t.Fatalf("expected completion upgrade to be applied")
	}

	if shouldSkipEvent(state, &CanonicalEvent{
		OccurredAt:      now.Add(-time.Minute),
		Completed:       false,
		PositionSeconds: 130,
	}) {
		t.Fatalf("expected material position increase to be applied")
	}
}
