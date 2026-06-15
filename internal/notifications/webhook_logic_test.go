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
		SeriesTitle:     "Severance",
		EpisodeTitle:    "Hello, Ms. Cobel",
		SeasonNumber:    &season,
		EpisodeNumber:   &episode,
		PosterPath:      testSeriesPosterPath,
		PosterURL:       testSeriesPosterCDN,
		MediaType:       "series",
		SeriesOverview:  "Mark leads a team whose memories have been surgically divided.",
		EpisodeOverview: "Mark is promoted after the disappearance of his colleague.",
		Genres:          []string{"Drama", "Sci-Fi & Fantasy"},
		ContentRating:   "TV-MA",
		RatingIMDB:      8.7,
		IMDBID:          "tt11280740",
		TMDBID:          "95396",
		TVDBID:          "371980",
	}
}

func TestBuildDiscordWebhookPayload(t *testing.T) {
	payload, err := BuildDiscordWebhookPayload(webhookTestRow(), false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Username string         `json:"username"`
		Embeds   []discordEmbed `json:"embeds"`
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
	if embed.Author == nil || embed.Author.Name != "New episode on Silo" {
		t.Fatalf("unexpected author %+v", embed.Author)
	}
	if embed.URL != "https://www.themoviedb.org/tv/95396" {
		t.Fatalf("unexpected title URL %q", embed.URL)
	}
	if embed.Thumbnail == nil || embed.Thumbnail.URL != testSeriesPosterCDN {
		t.Fatalf("unexpected thumbnail %+v", embed.Thumbnail)
	}
	// Episode overview wins over the series overview; provider links follow.
	if !strings.HasPrefix(embed.Description, "Mark is promoted") ||
		!strings.Contains(embed.Description, "[TMDB](https://www.themoviedb.org/tv/95396)") ||
		!strings.Contains(embed.Description, "[IMDb](https://www.imdb.com/title/tt11280740/)") ||
		!strings.Contains(embed.Description, "[TVDB](https://thetvdb.com/dereferrer/series/371980)") {
		t.Fatalf("unexpected description %q", embed.Description)
	}
	if len(embed.Fields) != 3 ||
		embed.Fields[0].Name != "Reason" || embed.Fields[0].Value != "Favorited & Continue Watching" ||
		embed.Fields[1].Value != "★ 8.7 IMDb" ||
		embed.Fields[2].Value != "Drama, Sci-Fi & Fantasy" {
		t.Fatalf("unexpected fields: %+v", embed.Fields)
	}
	if embed.Footer == nil || embed.Footer.Text != "Silo • TV-MA" {
		t.Fatalf("unexpected footer %+v", embed.Footer)
	}
	// The privacy contract: only public provider origins may appear — never
	// this server's own URL (which the builder cannot even see).
	for _, origin := range allOriginsIn(t, string(payload)) {
		switch origin {
		case "www.themoviedb.org", "image.tmdb.org", "www.imdb.com", "thetvdb.com":
		default:
			t.Fatalf("payload names non-provider origin %q: %s", origin, payload)
		}
	}
}

// allOriginsIn extracts every http(s) host named anywhere in the payload.
func allOriginsIn(t *testing.T, payload string) []string {
	t.Helper()
	hosts := make([]string, 0, 4)
	rest := payload
	for {
		at := strings.Index(rest, "https://")
		if at < 0 {
			break
		}
		rest = rest[at+len("https://"):]
		end := strings.IndexAny(rest, "/\"\\)")
		if end < 0 {
			end = len(rest)
		}
		hosts = append(hosts, rest[:end])
	}
	if strings.Contains(payload, "http://") {
		t.Fatalf("payload contains insecure http:// URL: %s", payload)
	}
	return hosts
}

func TestBuildDiscordWebhookPayloadWithoutPosterURL(t *testing.T) {
	row := webhookTestRow()
	// The poster decision is the sender layer's; a row without a resolved
	// PosterURL must render without an image regardless of stored paths.
	row.PosterURL = ""
	payload, err := BuildDiscordWebhookPayload(row, false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if strings.Contains(string(payload), `"thumbnail"`) {
		t.Fatalf("rows without a resolved poster URL must not render a thumbnail: %s", payload)
	}
}

// fakePresigner fakes the catalog image resolver: every path presigns to a
// recognizable server-storage URL.
type fakePresigner struct{}

func (fakePresigner) PresignImageURL(_ context.Context, path, _, _ string) string {
	return "https://s3.example.com/" + path + "?sig=abc"
}

func TestDiscordPosterURLModes(t *testing.T) {
	const cachedKey = "tmdb/series/95396/poster/original.jpg"
	system := func(mode string, images ImageURLResolver) *System {
		return &System{
			Settings: NewSettings(mapSettingReader{SettingDiscordPosterMode: mode}),
			images:   images,
		}
	}
	ctx := context.Background()

	// Off: nothing renders, even provider-CDN-resolvable artwork.
	if got := system("off", fakePresigner{}).discordPosterURL(ctx, testSeriesPosterPath, ""); got != "" {
		t.Fatalf("mode off must drop posters, got %q", got)
	}
	// Provider (default): public CDN URLs only; cached keys never presign.
	if got := system("", fakePresigner{}).discordPosterURL(ctx, testSeriesPosterPath, ""); got != testSeriesPosterCDN {
		t.Fatalf("provider mode CDN resolution failed, got %q", got)
	}
	if got := system("", fakePresigner{}).discordPosterURL(ctx, cachedKey, ""); got != "" {
		t.Fatalf("provider mode must not presign cached keys, got %q", got)
	}
	// Server: provider CDN still wins; cached keys presign as the fallback.
	if got := system("server", fakePresigner{}).discordPosterURL(ctx, cachedKey, testSeriesPosterPath); got != testSeriesPosterCDN {
		t.Fatalf("server mode must still prefer provider CDN, got %q", got)
	}
	if got := system("server", fakePresigner{}).discordPosterURL(ctx, cachedKey, ""); got != "https://s3.example.com/"+cachedKey+"?sig=abc" {
		t.Fatalf("server mode presign fallback failed, got %q", got)
	}
	// Server without a wired resolver degrades to no image.
	if got := system("server", nil).discordPosterURL(ctx, cachedKey, ""); got != "" {
		t.Fatalf("server mode without resolver must render no image, got %q", got)
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
		SeriesTitle:    "Dune",
		MediaType:      "movie",
		Year:           2021,
		SeriesOverview: "Paul Atreides, a brilliant and gifted young man.",
		RatingTMDB:     7.8,
		TMDBID:         "438631",
	}
}

func TestBuildDiscordWebhookPayloadRequestFulfilled(t *testing.T) {
	payload, err := BuildDiscordWebhookPayload(requestFulfilledTestRow(), false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Embeds []discordEmbed `json:"embeds"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(body.Embeds) != 1 {
		t.Fatalf("unexpected body shape: %+v", body)
	}
	embed := body.Embeds[0]
	if embed.Title != "Dune (2021)" {
		t.Fatalf("unexpected title %q", embed.Title)
	}
	if embed.Author == nil || embed.Author.Name != "Your request is now available on Silo" {
		t.Fatalf("unexpected author %+v", embed.Author)
	}
	if embed.URL != "https://www.themoviedb.org/movie/438631" {
		t.Fatalf("unexpected title URL %q", embed.URL)
	}
	if !strings.HasPrefix(embed.Description, "Paul Atreides") {
		t.Fatalf("unexpected description %q", embed.Description)
	}
	if len(embed.Fields) != 2 ||
		embed.Fields[0].Name != "Type" || embed.Fields[0].Value != "Movie" ||
		embed.Fields[1].Name != "Rating" || embed.Fields[1].Value != "★ 7.8 TMDB" {
		t.Fatalf("expected Type and Rating fields, got %+v", embed.Fields)
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

func requestDeclinedTestRow() DeliveryRow {
	return DeliveryRow{
		Delivery: Delivery{
			ID:        "01DECLINED",
			ProfileID: "profile-1",
			Type:      DeliveryTypeRequestDeclined,
			ReasonFlags: []byte(`{"request_id":"01REQ","tmdb_id":438631,"media_type":"movie",` +
				`"title":"Dune","year":2021,"reason":"Already available in 4K"}`),
			CreatedAt: time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		},
	}
}

func TestBuildDiscordWebhookPayloadRequestLifecycle(t *testing.T) {
	payload, err := BuildDiscordWebhookPayload(requestDeclinedTestRow(), false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Embeds []discordEmbed `json:"embeds"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(body.Embeds) != 1 {
		t.Fatalf("unexpected body shape: %+v", body)
	}
	embed := body.Embeds[0]
	// No catalog join exists for declined requests; title and link come from
	// the reason flags.
	if embed.Title != "Dune (2021)" {
		t.Fatalf("unexpected title %q", embed.Title)
	}
	if embed.Author == nil || embed.Author.Name != "Your request was declined on Silo" {
		t.Fatalf("unexpected author %+v", embed.Author)
	}
	if embed.URL != "https://www.themoviedb.org/movie/438631" {
		t.Fatalf("unexpected title URL %q", embed.URL)
	}
	if embed.Color != serverChannelColorDeclined {
		t.Fatalf("unexpected color %d", embed.Color)
	}
	if len(embed.Fields) != 2 ||
		embed.Fields[0].Name != "Type" || embed.Fields[0].Value != "Movie" ||
		embed.Fields[1].Name != "Reason" || embed.Fields[1].Value != "Already available in 4K" {
		t.Fatalf("expected Type and Reason fields, got %+v", embed.Fields)
	}

	approved := requestDeclinedTestRow()
	approved.Type = DeliveryTypeRequestApproved
	payload, err = BuildDiscordWebhookPayload(approved, false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	embed = body.Embeds[0]
	if embed.Author == nil || embed.Author.Name != "Your request was approved on Silo" {
		t.Fatalf("unexpected author %+v", embed.Author)
	}
	if embed.Color != serverChannelColorApproved {
		t.Fatalf("unexpected color %d", embed.Color)
	}
	// The decline reason must not leak into approved embeds.
	for _, field := range embed.Fields {
		if field.Name == "Reason" {
			t.Fatalf("approved embed must not carry a decline reason: %+v", embed.Fields)
		}
	}
}

func TestGenericWebhookPayloadRequestDeclined(t *testing.T) {
	payload, err := BuildGenericWebhookPayload(requestDeclinedTestRow(), "hook-1", false)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var body struct {
		Type    string `json:"type"`
		Request *struct {
			ID        string `json:"id"`
			TMDBID    int    `json:"tmdb_id"`
			MediaType string `json:"media_type"`
			Title     string `json:"title"`
			Year      int    `json:"year"`
			Reason    string `json:"reason"`
		} `json:"request"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if body.Type != DeliveryTypeRequestDeclined {
		t.Fatalf("unexpected type %q", body.Type)
	}
	if body.Request == nil || body.Request.ID != "01REQ" || body.Request.Title != "Dune" ||
		body.Request.Year != 2021 || body.Request.Reason != "Already available in 4K" {
		t.Fatalf("unexpected request block: %+v", body.Request)
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
