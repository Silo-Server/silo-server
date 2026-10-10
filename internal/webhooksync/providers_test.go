package webhooksync

import (
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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
	partial := &ItemState{LastEventAt: now, LastPositionSecond: 120}
	completed := &ItemState{LastEventAt: now, LastCompleted: true, LastPositionSecond: 3000}

	cases := []struct {
		name  string
		state *ItemState
		event CanonicalEvent
		want  bool
	}{
		{name: "first event applies", event: CanonicalEvent{OccurredAt: now}, want: false},
		{name: "newer event applies", state: partial, event: CanonicalEvent{OccurredAt: now.Add(time.Minute), PositionSeconds: 10}, want: false},
		{name: "older event is stale", state: partial, event: CanonicalEvent{OccurredAt: now.Add(-time.Minute), PositionSeconds: 600}, want: true},
		{name: "older completion cannot undo a later unplayed mark", state: &ItemState{LastEventAt: now}, event: CanonicalEvent{OccurredAt: now.Add(-time.Minute), Completed: true}, want: true},
		{name: "replayed event is a duplicate", state: partial, event: CanonicalEvent{OccurredAt: now, PositionSeconds: 120}, want: true},
		{name: "replayed completion is a duplicate", state: completed, event: CanonicalEvent{OccurredAt: now, Completed: true, PositionSeconds: 3000}, want: true},
		{name: "same-instant completion upgrade applies", state: partial, event: CanonicalEvent{OccurredAt: now, Completed: true, PositionSeconds: 121}, want: false},
		{name: "same-instant position increase applies", state: partial, event: CanonicalEvent{OccurredAt: now, PositionSeconds: 130}, want: false},
		{name: "per-playback completion repeats only after a new playback", state: completed, event: CanonicalEvent{OccurredAt: now.Add(time.Hour), Completed: true, CompletionPerPlayback: true}, want: true},
		{name: "per-playback completion after a new playback applies", state: partial, event: CanonicalEvent{OccurredAt: now.Add(time.Hour), Completed: true, CompletionPerPlayback: true}, want: false},
		{name: "per-playback stop after completion is part of that playback", state: completed, event: CanonicalEvent{OccurredAt: now.Add(time.Minute), PositionSeconds: 3100, CompletionPerPlayback: true}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldSkipEvent(tc.state, &tc.event); got != tc.want {
				t.Fatalf("shouldSkipEvent() = %v, want %v", got, tc.want)
			}
		})
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

func TestJellyfinProviderParseTogglePlayed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		reason        string
		played        bool
		wantApply     bool
		wantAction    string
		wantCompleted bool
	}{
		{name: "mark played", reason: "TogglePlayed", played: true, wantApply: true, wantAction: ActionImportProgress, wantCompleted: true},
		{name: "mark unplayed", reason: "TogglePlayed", played: false, wantApply: true, wantAction: ActionMarkUnplayed},
		{name: "playback finished repeats playback stop", reason: "PlaybackFinished", played: true},
		{name: "rating change", reason: "UpdateUserRating", played: true},
		{name: "template without user data", reason: "", played: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			played := "false"
			if tc.played {
				played = "true"
			}
			event, err := parseJellyfin(t, `{
				"provider": "jellyfin",
				"notification_type": "UserDataSaved",
				"timestamp": "2026-04-07T12:00:00Z",
				"user": { "id": "user-1", "name": "Alice" },
				`+jellyfinMovieItem+`,
				"user_data": { "save_reason": "`+tc.reason+`", "played": `+played+` }
			}`)
			if err != nil {
				t.Fatalf("ParseWebhook() error = %v", err)
			}
			if event.Apply != tc.wantApply {
				t.Fatalf("Apply = %v, want %v (%#v)", event.Apply, tc.wantApply, event)
			}
			if !tc.wantApply {
				return
			}
			if event.Action != tc.wantAction || event.Completed != tc.wantCompleted {
				t.Fatalf("action/completed = %q/%v, want %q/%v", event.Action, event.Completed, tc.wantAction, tc.wantCompleted)
			}
		})
	}
}

func TestJellyfinProviderIgnoresUnsupportedNotification(t *testing.T) {
	t.Parallel()

	event, err := parseJellyfin(t, `{
		"provider": "jellyfin",
		"notification_type": "PlaybackProgress",
		"timestamp": "2026-04-07T12:00:00Z",
		"user": { "id": "user-1", "name": "Alice" },
		`+jellyfinMovieItem+`,
		"playback": { "position_ticks": 1, "played_to_completion": false, "runtime_ticks": 2 }
	}`)
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if event == nil || event.Apply {
		t.Fatalf("expected ignored event, got %#v", event)
	}
}

func TestJellyfinProviderRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty object":            `{}`,
		"not json":                `not json`,
		"trailing data":           `{"notification_type":"PlaybackStop"} {"x":1}`,
		"missing user":            `{"notification_type":"PlaybackStop","timestamp":"2026-04-07T12:00:00Z",` + jellyfinMovieItem + `}`,
		"missing item":            `{"notification_type":"PlaybackStop","timestamp":"2026-04-07T12:00:00Z","user":{"id":"user-1"}}`,
		"missing timestamp":       `{"notification_type":"PlaybackStop","user":{"id":"user-1"},` + jellyfinMovieItem + `}`,
		"bad timestamp":           `{"notification_type":"PlaybackStop","timestamp":"yesterday","user":{"id":"user-1"},` + jellyfinMovieItem + `}`,
		"toggle without user":     `{"notification_type":"UserDataSaved","timestamp":"2026-04-07T12:00:00Z",` + jellyfinMovieItem + `,"user_data":{"save_reason":"TogglePlayed","played":true}}`,
		"toggle without played":   `{"notification_type":"UserDataSaved","timestamp":"2026-04-07T12:00:00Z","user":{"id":"user-1"},` + jellyfinMovieItem + `,"user_data":{"save_reason":"TogglePlayed"}}`,
		"toggle with null played": `{"notification_type":"UserDataSaved","timestamp":"2026-04-07T12:00:00Z","user":{"id":"user-1"},` + jellyfinMovieItem + `,"user_data":{"save_reason":"TogglePlayed","played":null}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if event, err := parseJellyfin(t, body); err == nil {
				t.Fatalf("ParseWebhook() = %#v, want error", event)
			}
		})
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
	return NewPlexProvider(historyimport.NewPlexClient()).ParseWebhook(trusted.Context(t.Context(), conn.UserID), conn, req)
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

// A playback start resets the per-playback completion from the event's IDs
// alone, so an unreachable server cannot make the next scrobble look like part
// of the previous playback.
func TestPlexProviderPlaybackStartSkipsMetadata(t *testing.T) {
	t.Parallel()
	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	play, err := parsePlex(t, server, `{"event":"media.play","Account":{"id":5,"title":"Kid"},"Metadata":{"ratingKey":"42","type":"movie"}}`)
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if !play.Apply || play.Action != ActionPlaybackStarted || play.UserID != "5" || play.ExternalItemID != "42" || play.OccurredAt.IsZero() {
		t.Fatalf("unexpected play: %#v", play)
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("play made %d metadata lookups, want 0", n)
	}
}

func TestPlexProviderRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()
	server := newPlexMetadataServer(t)

	for name, payload := range map[string]string{
		"not json":        `not json`,
		"missing event":   `{"Account":{"id":5},"Metadata":{"ratingKey":"42","type":"movie"}}`,
		"missing account": `{"event":"media.scrobble","Metadata":{"ratingKey":"42","type":"movie"}}`,
		"missing item":    `{"event":"media.scrobble","Account":{"id":5}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if event, err := parsePlex(t, server, payload); err == nil {
				t.Fatalf("ParseWebhook() = %#v, want error", event)
			}
		})
	}
}

// Plex webhooks identify the server owner by its server-local account ID, so
// the owner's default mapping must use that ID rather than the plex.tv one.
func TestPlexDefaultUserMapsServerOwnerAccount(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Account":[{"id":0,"name":""},{"id":1,"name":"owner"},{"id":23456789,"name":"Kid","home":true}]}}`))
	}))
	t.Cleanup(server.Close)
	conn := &Connection{UserID: 7, BaseURL: server.URL, AccessToken: "owner-token"}
	trusted := historyimport.NewLocalNetworkAccess(staticSettings{historyimport.SettingAllowPrivateDestinations: "true"}, nil)

	id, name, ok, err := NewPlexProvider(historyimport.NewPlexClient()).DefaultUser(trusted.Context(t.Context(), conn.UserID), conn, CreateConnectionInput{AccessToken: "owner-token"})
	if err != nil || !ok || id != "1" || name != "owner" {
		t.Fatalf("DefaultUser() = (%q, %q, %v, %v), want (\"1\", \"owner\", true, nil)", id, name, ok, err)
	}
}

func TestProgressOutranksEvent(t *testing.T) {
	t.Parallel()
	// A whole-second event time, so the pair's own write, which user stores
	// keep in whole seconds, has exactly the event's timestamp.
	at := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	inProgress := &ItemState{LastEventAt: at, LastPositionSecond: 100}
	cases := []struct {
		name      string
		updatedAt time.Time
		position  float64
		state     *ItemState
		eventAt   time.Time
		want      bool
	}{
		{name: "native progress newer than event", updatedAt: at, eventAt: at.Add(-time.Minute), want: true},
		{name: "native progress as recent as event", updatedAt: at, eventAt: at, want: true},
		{name: "event newer than progress", updatedAt: at, eventAt: at.Add(time.Minute), want: false},
		{name: "native progress later in the event's second", updatedAt: at, eventAt: at.Add(500 * time.Millisecond), want: true},
		{name: "same-timestamp upgrade over this pair's own write", updatedAt: at, position: 100, state: inProgress, eventAt: at, want: false},
		{name: "this pair's own completed write", updatedAt: at, position: 0, state: &ItemState{LastEventAt: at, LastCompleted: true, LastPositionSecond: 590}, eventAt: at, want: false},
		{name: "native progress in the second of this pair's last event", updatedAt: at, position: 500, state: inProgress, eventAt: at, want: true},
		{name: "native progress after this pair's last event", updatedAt: at.Add(time.Minute), position: 100, state: inProgress, eventAt: at.Add(time.Second), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := progressOutranksEvent(tc.updatedAt, tc.position, tc.state, tc.eventAt); got != tc.want {
				t.Fatalf("progressOutranksEvent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEndsPlaybackWithoutApplying(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	scrobble := &CanonicalEvent{OccurredAt: at, Completed: true, CompletionPerPlayback: true}
	cases := []struct {
		name  string
		state *ItemState
		event *CanonicalEvent
		want  bool
	}{
		{name: "first scrobble", event: scrobble, want: true},
		{name: "scrobble after an older event", state: &ItemState{LastEventAt: at.Add(-time.Minute)}, event: scrobble, want: true},
		{name: "scrobble older than the last event", state: &ItemState{LastEventAt: at.Add(time.Minute)}, event: scrobble, want: false},
		{name: "scrobble at the last event's time", state: &ItemState{LastEventAt: at}, event: scrobble, want: false},
		{name: "pause", event: &CanonicalEvent{OccurredAt: at, CompletionPerPlayback: true}, want: false},
		{name: "completion without per-playback semantics", event: &CanonicalEvent{OccurredAt: at, Completed: true}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := endsPlaybackWithoutApplying(tc.state, tc.event); got != tc.want {
				t.Fatalf("endsPlaybackWithoutApplying() = %v, want %v", got, tc.want)
			}
		})
	}
}
