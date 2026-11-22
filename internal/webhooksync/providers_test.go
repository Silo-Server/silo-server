package webhooksync

import (
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
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

const jellyfinMovieItem = `"item": { "id": "item-1", "type": "Movie", "name": "Movie", "year": 2008, "runtime_ticks": 6000000000, "provider_ids": { "imdb": "tt1254207", "tmdb": "10378", "tvdb": "" } }`

func parseJellyfin(t *testing.T, body string) (*CanonicalEvent, error) {
	t.Helper()
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
	return NewJellyfinProvider().ParseWebhook(context.Background(), &Connection{}, req)
}

func TestJellyfinProviderParsePlaybackStop(t *testing.T) {
	t.Parallel()

	event, err := parseJellyfin(t, `{
		"provider": "jellyfin",
		"notification_type": "PlaybackStop",
		"timestamp": "2026-04-07T12:00:00.1234567Z",
		"user": { "id": "user-1", "name": "Alice" },
		`+jellyfinMovieItem+`,
		"playback": { "position_ticks": 1200000000, "played_to_completion": false, "runtime_ticks": 6000000000 }
	}`)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if !event.Apply || event.Action != ActionImportProgress || event.Completed {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.PositionSeconds != 120 || event.DurationSeconds != 600 {
		t.Fatalf("position/duration = %v/%v, want 120/600", event.PositionSeconds, event.DurationSeconds)
	}
	if want := time.Date(2026, 4, 7, 12, 0, 0, 123456000, time.UTC); !event.OccurredAt.Equal(want) {
		t.Fatalf("OccurredAt = %v, want %v", event.OccurredAt, want)
	}
	if event.UserID != "user-1" || event.UserName != "Alice" || event.Record.TMDBID != "10378" {
		t.Fatalf("unexpected identity: %#v", event)
	}
}

func newPlexMetadataServer(t *testing.T) *httptest.Server {
	t.Helper()
	// The server reports its owner's view state: watched in 2024 at full
	// offset. Webhooks for other accounts must not copy it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"42","type":"movie","title":"Movie","year":2008,"duration":600000,"viewOffset":590000,"viewCount":3,"lastViewedAt":1704067200,"Guid":[{"id":"tmdb://10378"},{"id":"imdb://tt1254207"}]}]}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func parsePlex(t *testing.T, server *httptest.Server, payload string) (*CanonicalEvent, error) {
	t.Helper()
	return parsePlexAt(t, t.Context(), server, payload)
}

func parsePlexAt(t *testing.T, ctx context.Context, server *httptest.Server, payload string) (*CanonicalEvent, error) {
	t.Helper()
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("payload", payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	conn := &Connection{UserID: 7, BaseURL: server.URL, AccessToken: "owner-token"}
	trusted := historyimport.NewLocalNetworkAccess(staticSettings{historyimport.SettingAllowPrivateDestinations: "true"}, nil)
	return NewPlexProvider(historyimport.NewPlexClient()).ParseWebhook(trusted.Context(ctx, conn.UserID), conn, req)
}

func TestPlexProviderUsesEventStateNotOwnerMetadata(t *testing.T) {
	t.Parallel()
	server := newPlexMetadataServer(t)

	scrobble, err := parsePlex(t, server, `{"event":"media.scrobble","Account":{"id":5,"title":"Kid"},"Metadata":{"ratingKey":"42","type":"movie"}}`)
	if err != nil {
		t.Fatalf("scrobble: %v", err)
	}
	if !scrobble.Apply || !scrobble.Completed || !scrobble.CompletionPerPlayback || scrobble.UserID != "5" {
		t.Fatalf("unexpected scrobble: %#v", scrobble)
	}
	if scrobble.Record.LastPlayedAt == nil || scrobble.Record.LastPlayedAt.Year() == 2024 || scrobble.DurationSeconds != 600 || scrobble.Record.TMDBID != "10378" {
		t.Fatalf("scrobble record = %#v", scrobble.Record)
	}

	pause, err := parsePlex(t, server, `{"event":"media.pause","Account":{"id":5,"title":"Kid"},"Metadata":{"ratingKey":"42","type":"movie","viewOffset":300000}}`)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !pause.Apply || pause.Completed || pause.PositionSeconds != 300 || pause.Record.LastPlayedAt != nil {
		t.Fatalf("unexpected pause: %#v", pause)
	}

	unknownOffset, err := parsePlex(t, server, `{"event":"media.stop","Account":{"id":5,"title":"Kid"},"Metadata":{"ratingKey":"42","type":"movie"}}`)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if unknownOffset.Apply {
		t.Fatalf("stop without an offset should be ignored: %#v", unknownOffset)
	}

	play, err := parsePlex(t, server, `{"event":"media.play","Account":{"id":5,"title":"Kid"},"Metadata":{"ratingKey":"42","type":"movie","viewOffset":0}}`)
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if !play.Apply || play.Action != ActionPlaybackStarted {
		t.Fatalf("unexpected play: %#v", play)
	}
}
