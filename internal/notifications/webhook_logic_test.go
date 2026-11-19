package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// mapSettingReader is a SettingReader fake; missing keys read as unset.
type mapSettingReader map[string]string

func (m mapSettingReader) Get(_ context.Context, key string) (string, error) {
	return m[key], nil
}

func TestWebhookCreateAndTestBlockedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	service := newWebhookService(nil, nil, NewSettings(nil), nil)

	name, url := "hook", "https://discord.com/api/webhooks/1/abc"
	if _, _, err := service.Create(ctx, 1, "profile", WebhookInput{Name: &name, URL: &url}); !errors.Is(err, ErrWebhooksDisabled) {
		t.Fatalf("Create error = %v, want ErrWebhooksDisabled", err)
	}
	if _, err := service.Test(ctx, 1, "profile", "hook-id"); !errors.Is(err, ErrWebhooksDisabled) {
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
		if webhookIPAllowed(netip.MustParseAddr(raw), false) {
			t.Errorf("webhookIPAllowed(%q) = true, want denied", raw)
		}
	}

	allowed := []string{"1.1.1.1", "8.8.8.8", "151.101.1.69", "2606:4700::1111"}
	for _, raw := range allowed {
		if !webhookIPAllowed(netip.MustParseAddr(raw), false) {
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
	// allowPrivate permits the Private class for dev environments.
	host, err := ValidateWebhookURL("https://192.168.1.50/hook", true)
	if err != nil || host != "192.168.1.50" {
		t.Fatalf("allowPrivate failed: %q %v", host, err)
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
			ReasonFlags: []byte(`{"favorite":true,"continue_watching":true,"next_up":true}`),
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

// fakePresigner fakes the catalog image resolver: every path presigns to a
// recognizable server-storage URL.
type fakePresigner struct{}

func (fakePresigner) PresignImageURL(_ context.Context, path, _, _ string) string {
	return "https://s3.example.com/" + path + "?sig=abc"
}

// relativePresigner fakes local artwork delivery: signed root-relative URLs.
type relativePresigner struct{}

func (relativePresigner) PresignImageURL(_ context.Context, path, _, _ string) string {
	return "/api/v2/artwork/" + path + "?exp=1&sig=abc"
}

var (
	digestOverview = strings.Repeat("A long synopsis sentence that keeps going. ", 12)
	digestGenres   = []string{"Science Fiction & Fantasy", "Action & Adventure", "Documentary"}
)

type discordDigestCase struct {
	name  string
	items int
	// wantEmbeds, when set, is the exact embed count expected.
	wantEmbeds int
	// links is the provider links each embed's description must keep.
	links int
	// fullTeasers requires every teaser to be sent unshortened.
	fullTeasers bool
	// fullTitles and fullFields require over-long titles and field values to
	// be clipped exactly to Discord's character limits, no shorter.
	fullTitles, fullFields bool
	build                  func() ([]byte, error)
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
