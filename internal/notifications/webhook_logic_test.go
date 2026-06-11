package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// mapSettingReader is a SettingReader fake; missing keys read as unset.
type mapSettingReader map[string]string

func (m mapSettingReader) Get(_ context.Context, key string) (string, error) {
	return m[key], nil
}

func TestWebhooksDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	if NewSettings(nil).WebhooksEnabled(ctx) {
		t.Fatal("WebhooksEnabled must default to false until an admin opts in")
	}
	if !NewSettings(mapSettingReader{SettingWebhooksEnabled: "true"}).WebhooksEnabled(ctx) {
		t.Fatal("WebhooksEnabled = false with the setting on, want true")
	}
}

func TestWebhookCreateAndTestBlockedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	service := newWebhookService(nil, nil, NewSettings(nil), nil)

	name, url := "hook", "https://discord.com/api/webhooks/1/abc"
	if _, _, err := service.Create(ctx, 1, "profile", WebhookInput{Name: &name, URL: &url}); !errors.Is(err, ErrWebhooksDisabled) {
		t.Fatalf("Create error = %v, want ErrWebhooksDisabled", err)
	}
	if _, err := service.Test(ctx, "profile", "hook-id"); !errors.Is(err, ErrWebhooksDisabled) {
		t.Fatalf("Test error = %v, want ErrWebhooksDisabled", err)
	}
}

func TestWebhookIPAllowed(t *testing.T) {
	denied := []string{
		"0.1.2.3",
		"10.1.2.3",
		"100.64.0.1",   // CGNAT
		"127.0.0.1",    // loopback
		"169.254.1.1",  // link-local
		"172.16.0.1",   // private
		"192.0.0.1",    // IETF
		"192.0.2.1",    // TEST-NET-1
		"198.51.100.1", // TEST-NET-2
		"203.0.113.1",  // TEST-NET-3
		"192.88.99.1",  // 6to4 anycast
		"192.168.1.1",  // private
		"198.18.0.1",   // benchmarking
		"198.19.255.1", // benchmarking upper half
		"224.0.0.1",    // multicast
		"255.255.255.255",
		"::1",
		"fc00::1",            // ULA
		"fe80::1",            // link-local
		"2001:db8::1",        // documentation
		"64:ff9b::7f00:1",    // NAT64-mapped loopback
		"::ffff:127.0.0.1",   // v4-mapped loopback (the classic bypass)
		"::ffff:192.168.0.5", // v4-mapped private
	}
	for _, raw := range denied {
		if webhookIPAllowed(net.ParseIP(raw)) {
			t.Errorf("webhookIPAllowed(%q) = true, want denied", raw)
		}
	}

	allowed := []string{"1.1.1.1", "8.8.8.8", "151.101.1.69", "2606:4700::1111"}
	for _, raw := range allowed {
		if !webhookIPAllowed(net.ParseIP(raw)) {
			t.Errorf("webhookIPAllowed(%q) = false, want allowed", raw)
		}
	}
}

func TestValidateWebhookURL(t *testing.T) {
	if _, err := ValidateWebhookURL("http://example.com/hook", false); err == nil {
		t.Fatal("plain http must be rejected")
	}
	if _, err := ValidateWebhookURL("https://user:pass@example.com/hook", false); err == nil {
		t.Fatal("embedded credentials must be rejected")
	}
	if _, err := ValidateWebhookURL("https://127.0.0.1/hook", false); err == nil {
		t.Fatal("loopback literal must be rejected")
	}
	if _, err := ValidateWebhookURL("https://[::ffff:127.0.0.1]/hook", false); err == nil {
		t.Fatal("v4-mapped loopback literal must be rejected")
	}
	// allowPrivate bypasses the guard for dev environments.
	host, err := ValidateWebhookURL("https://192.168.1.50/hook", true)
	if err != nil || host != "192.168.1.50" {
		t.Fatalf("allowPrivate bypass failed: %q %v", host, err)
	}
}

func TestDiscordWebhookURLDetection(t *testing.T) {
	positives := []string{
		"https://discord.com/api/webhooks/123/abc",
		"https://discordapp.com/api/webhooks/123/abc",
		"https://ptb.discord.com/api/webhooks/123/abc",
	}
	for _, raw := range positives {
		if !discordWebhookURL(raw) {
			t.Errorf("discordWebhookURL(%q) = false, want true", raw)
		}
	}
	negatives := []string{
		"https://hooks.slack.com/services/T/B/x",
		"https://discord.com/channels/123",
		"https://evil.com/api/webhooks/123/abc",
		"https://discord.com.evil.com/api/webhooks/1/2",
	}
	for _, raw := range negatives {
		if discordWebhookURL(raw) {
			t.Errorf("discordWebhookURL(%q) = true, want false", raw)
		}
	}
}

func webhookTestRow() DeliveryRow {
	libraryID := 7
	seriesID := "series-123"
	episodeID := "episode-456"
	season := 2
	episode := 1
	return DeliveryRow{
		Delivery: Delivery{
			ID:          "01DELIVERY",
			ProfileID:   "profile-1",
			LibraryID:   &libraryID,
			SeriesID:    &seriesID,
			EpisodeID:   &episodeID,
			Type:        DeliveryTypeEpisodeAvailable,
			ReasonFlags: []byte(`{"favorite":true,"continue_watching":true}`),
			CreatedAt:   time.Date(2026, 4, 28, 12, 34, 56, 0, time.UTC),
		},
		SeriesTitle:   "Severance",
		EpisodeTitle:  "Hello, Ms. Cobel",
		SeasonNumber:  &season,
		EpisodeNumber: &episode,
	}
}

func TestBuildDiscordWebhookPayload(t *testing.T) {
	payload, err := BuildDiscordWebhookPayload(webhookTestRow(), false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Username string `json:"username"`
		Embeds   []struct {
			Title  string `json:"title"`
			Color  int    `json:"color"`
			Footer struct {
				Text string `json:"text"`
			} `json:"footer"`
			Fields []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"fields"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if body.Username != "Silo" || len(body.Embeds) != 1 {
		t.Fatalf("unexpected body shape: %+v", body)
	}
	embed := body.Embeds[0]
	if embed.Title != "Severance — S2 E1: Hello, Ms. Cobel" {
		t.Fatalf("unexpected title %q", embed.Title)
	}
	if embed.Color != discordColorFavorite {
		t.Fatalf("favorite reason must pick the favorite color, got %d", embed.Color)
	}
	if embed.Fields[0].Name != "Reason" || embed.Fields[0].Value != "Favorited & Continue Watching" {
		t.Fatalf("unexpected reason field: %+v", embed.Fields[0])
	}
	// The v1 privacy contract: no image, url, thumbnail, or avatar fields.
	for _, forbidden := range []string{`"image"`, `"thumbnail"`, `"avatar_url"`, `"url"`} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("v1 Discord payload must not contain %s: %s", forbidden, payload)
		}
	}
}

func requestFulfilledTestRow() DeliveryRow {
	contentID := "movie-123"
	return DeliveryRow{
		Delivery: Delivery{
			ID:          "01REQUEST",
			ProfileID:   "profile-1",
			SeriesID:    &contentID,
			Type:        DeliveryTypeRequestFulfilled,
			ReasonFlags: []byte(`{"request_id":"01REQ","tmdb_id":438631,"media_type":"movie"}`),
			CreatedAt:   time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
		SeriesTitle: "Dune",
	}
}

func TestBuildDiscordWebhookPayloadRequestFulfilled(t *testing.T) {
	payload, err := BuildDiscordWebhookPayload(requestFulfilledTestRow(), false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Fields      []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"fields"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(body.Embeds) != 1 {
		t.Fatalf("unexpected body shape: %+v", body)
	}
	embed := body.Embeds[0]
	if embed.Title != "Dune" {
		t.Fatalf("unexpected title %q", embed.Title)
	}
	if embed.Description != "Your media request is now available on Silo" {
		t.Fatalf("unexpected description %q", embed.Description)
	}
	if len(embed.Fields) != 1 || embed.Fields[0].Name != "Type" || embed.Fields[0].Value != "Movie" {
		t.Fatalf("expected a single Type=Movie field, got %+v", embed.Fields)
	}
}

func TestGenericWebhookPayloadRequestFulfilled(t *testing.T) {
	payload, err := BuildGenericWebhookPayload(requestFulfilledTestRow(), "hook-1", false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Type   string `json:"type"`
		Series *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"series"`
		Request *struct {
			ID        string `json:"id"`
			TMDBID    int    `json:"tmdb_id"`
			MediaType string `json:"media_type"`
		} `json:"request"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if body.Type != DeliveryTypeRequestFulfilled {
		t.Fatalf("unexpected type %q", body.Type)
	}
	if body.Request == nil || body.Request.ID != "01REQ" || body.Request.TMDBID != 438631 || body.Request.MediaType != "movie" {
		t.Fatalf("unexpected request block: %+v", body.Request)
	}
	if body.Series == nil || body.Series.ID != "movie-123" || body.Series.Title != "Dune" {
		t.Fatalf("unexpected series block: %+v", body.Series)
	}
}

func TestBuildDiscordWebhookPayloadTestMarker(t *testing.T) {
	payload, err := BuildDiscordWebhookPayload(webhookTestRow(), true)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if !strings.Contains(string(payload), "Silo test notification") {
		t.Fatal("test sends must be clearly marked in the footer")
	}
}

func TestDiscordTotalLimitTruncation(t *testing.T) {
	row := webhookTestRow()
	row.SeriesTitle = strings.Repeat("a", 300) // title gets clipped to 256
	payload, err := BuildDiscordWebhookPayload(row, false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Embeds []struct {
			Title string `json:"title"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Embeds[0].Title) > discordTitleLimit {
		t.Fatalf("title exceeds Discord limit: %d bytes", len(body.Embeds[0].Title))
	}

	embed := discordEmbed{
		Title:       "t",
		Description: strings.Repeat("d", 7000),
		Fields: []discordEmbedField{
			{Name: "a", Value: strings.Repeat("x", 500)},
			{Name: "b", Value: strings.Repeat("y", 500)},
		},
	}
	enforceDiscordTotalLimit(&embed)
	if total := discordEmbedTotal(&embed); total > discordTotalLimit {
		t.Fatalf("embed total %d exceeds %d after enforcement", total, discordTotalLimit)
	}
	if len(embed.Fields) != 2 {
		t.Fatal("description must be truncated before fields are dropped")
	}
}

func TestGenericWebhookPayloadAndSignature(t *testing.T) {
	row := webhookTestRow()
	payload, err := BuildGenericWebhookPayload(row, "01HOOK", false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if body["event"] != "notification.created" || body["version"] != float64(1) || body["test"] != false {
		t.Fatalf("unexpected envelope: %v", body)
	}
	if body["delivery_id"] != "01DELIVERY" || body["webhook_id"] != "01HOOK" {
		t.Fatalf("unexpected ids: %v", body)
	}
	series, seriesOK := body["series"].(map[string]any)
	episode, episodeOK := body["episode"].(map[string]any)
	if !seriesOK || !episodeOK || series["title"] != "Severance" || episode["season_number"] != float64(2) {
		t.Fatalf("unexpected content: %v", body)
	}
	if strings.Contains(string(payload), "http") {
		t.Fatal("generic payload must not contain any URLs")
	}

	// Signature: deterministic, Stripe-style, verifiable from literal bytes.
	const secretValue = "test-secret"
	timestamp := int64(1714299296)
	header := SignGenericWebhook(secretValue, timestamp, payload)
	wantPrefix := fmt.Sprintf("t=%d,v1=", timestamp)
	if !strings.HasPrefix(header, wantPrefix) {
		t.Fatalf("unexpected signature header %q", header)
	}
	mac := hmac.New(sha256.New, []byte(secretValue))
	mac.Write([]byte("1714299296."))
	mac.Write(payload)
	if header != wantPrefix+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("signature does not verify against literal body bytes")
	}
	if SignGenericWebhook(secretValue, timestamp, payload) != header {
		t.Fatal("signature must be deterministic")
	}
}

func TestWebhookRetrySchedule(t *testing.T) {
	// Cumulative schedule: 0, 30s, 2m, 10m, 30m, 2h, 6h, 12h, 18h, 24h.
	total := time.Duration(0)
	for attempt := 1; attempt < webhookMaxAttempts; attempt++ {
		delay, ok := webhookRetryDelay(attempt)
		if !ok {
			t.Fatalf("schedule ended early at attempt %d", attempt)
		}
		total += delay
		if total != webhookRetrySchedule[attempt] {
			t.Fatalf("cumulative delay after attempt %d = %v, want %v", attempt, total, webhookRetrySchedule[attempt])
		}
	}
	if _, ok := webhookRetryDelay(webhookMaxAttempts); ok {
		t.Fatal("attempt 10 must exhaust the schedule")
	}
	if total != 24*time.Hour {
		t.Fatalf("schedule must span 24h, got %v", total)
	}
}

func TestRetryableHTTPStatus(t *testing.T) {
	retryable := []int{0, 500, 502, 503, 408, 425, 429}
	for _, status := range retryable {
		if !retryableHTTPStatus(status) {
			t.Errorf("status %d must be retryable", status)
		}
	}
	nonRetryable := []int{400, 401, 403, 404, 410, 422}
	for _, status := range nonRetryable {
		if retryableHTTPStatus(status) {
			t.Errorf("status %d must not be retryable", status)
		}
	}
}

func TestWebhookMatchesReasons(t *testing.T) {
	hook := Webhook{NotifyFavorites: true, NotifyWatchlist: false, NotifyContinueWatching: false, NotifyNextUp: false}
	if !hook.MatchesReasons(ReasonFlags{Favorite: true, Watchlist: true}) {
		t.Fatal("favorite reason must match a favorites-enabled webhook")
	}
	if hook.MatchesReasons(ReasonFlags{Watchlist: true}) {
		t.Fatal("watchlist-only delivery must not match a favorites-only webhook")
	}
	if hook.MatchesReasons(ReasonFlags{}) {
		t.Fatal("no reasons must never match")
	}
}

func TestProfileRateLimiter(t *testing.T) {
	limiter := newProfileRateLimiter()
	for i := 0; i < 3; i++ {
		if !limiter.Allow("p1", 3) {
			t.Fatalf("delivery %d must be allowed", i+1)
		}
	}
	if limiter.Allow("p1", 3) {
		t.Fatal("4th delivery within the window must be limited")
	}
	if !limiter.Allow("p2", 3) {
		t.Fatal("limits must be per-profile")
	}
}
