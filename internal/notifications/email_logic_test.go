package notifications

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEffectiveChannelMode(t *testing.T) {
	if got := effectiveChannelMode(ChannelModePerEpisode, true); got != ChannelModePerEpisode {
		t.Fatalf("allowed per-episode coerced to %q", got)
	}
	if got := effectiveChannelMode(ChannelModePerEpisode, false); got != ChannelModeDailyDigest {
		t.Fatalf("disallowed per-episode should coerce to digest, got %q", got)
	}
	if got := effectiveChannelMode(ChannelModeDailyDigest, false); got != ChannelModeDailyDigest {
		t.Fatalf("digest mode changed to %q", got)
	}
	if got := effectiveChannelMode(ChannelModeOff, true); got != ChannelModeOff {
		t.Fatalf("off mode changed to %q", got)
	}
	if got := effectiveChannelMode(ChannelModePerEpisodeAndDigest, true); got != ChannelModePerEpisodeAndDigest {
		t.Fatalf("allowed combined mode coerced to %q", got)
	}
	if got := effectiveChannelMode(ChannelModePerEpisodeAndDigest, false); got != ChannelModeDailyDigest {
		t.Fatalf("disallowed combined mode should coerce to digest, got %q", got)
	}
}

func TestChannelDigestDue(t *testing.T) {
	loc := time.UTC
	morning := time.Date(2026, 6, 11, 7, 30, 0, 0, loc)
	afternoon := time.Date(2026, 6, 11, 14, 0, 0, 0, loc)
	yesterday := time.Date(2026, 6, 10, 9, 0, 0, 0, loc)
	today := time.Date(2026, 6, 11, 8, 5, 0, 0, loc)

	if channelDigestDue(morning, 8, nil) {
		t.Fatal("digest due before today's send hour")
	}
	if !channelDigestDue(afternoon, 8, nil) {
		t.Fatal("first-ever digest not due after send hour")
	}
	if !channelDigestDue(afternoon, 8, &yesterday) {
		t.Fatal("digest not due when last one was yesterday")
	}
	if channelDigestDue(afternoon, 8, &today) {
		t.Fatal("digest due twice in one day")
	}
}

func TestChannelRetryEligible(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Second)
	stale := now.Add(-10 * time.Minute)

	if !channelRetryEligible(now, nil, 0) {
		t.Fatal("clean account not eligible")
	}
	if !channelRetryEligible(now, &recent, 0) {
		t.Fatal("successful account not eligible")
	}
	if channelRetryEligible(now, &recent, 1) {
		t.Fatal("eligible 30s after first failure (backoff is 1m)")
	}
	if !channelRetryEligible(now, &stale, 3) {
		t.Fatal("not eligible 10m after third failure (backoff is 4m)")
	}
	// Large failure counts must not overflow the shift; cap applies.
	old := now.Add(-7 * time.Hour)
	if !channelRetryEligible(now, &old, 60) {
		t.Fatal("not eligible past the 6h backoff cap")
	}
	if channelRetryEligible(now, &recent, 60) {
		t.Fatal("eligible 30s after many failures")
	}
}

// emailEpisodeRow builds an episode.available row for one profile.
func emailEpisodeRow(id, profileID, episodeID string, season, episode int) DeliveryRow {
	seriesID := "series-123"
	return DeliveryRow{
		Delivery: Delivery{
			ID:          id,
			ProfileID:   profileID,
			SeriesID:    &seriesID,
			EpisodeID:   &episodeID,
			Type:        DeliveryTypeEpisodeAvailable,
			ReasonFlags: []byte(`{"favorite":true}`),
			CreatedAt:   time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		},
		SeriesTitle:   "Severance",
		EpisodeTitle:  fmt.Sprintf("Episode %d", episode),
		SeasonNumber:  &season,
		EpisodeNumber: &episode,
	}
}

func TestCollateEmailItemsDedupesAcrossProfiles(t *testing.T) {
	rows := []DeliveryRow{
		emailEpisodeRow("01A", "profile-1", "ep-1", 2, 3),
		emailEpisodeRow("01B", "profile-2", "ep-1", 2, 3), // same episode, second profile
		emailEpisodeRow("01C", "profile-1", "ep-2", 2, 4),
		requestFulfilledTestRow(),
		{Delivery: Delivery{ID: "01D", Type: DeliveryTypeWebhookAutoDisabled}},
	}
	items := collateEmailItems(rows)
	if items.episodes != 2 {
		t.Fatalf("expected 2 deduped episodes, got %d", items.episodes)
	}
	if len(items.series) != 1 || items.series[0].title != "Severance" {
		t.Fatalf("unexpected series groups: %+v", items.series)
	}
	if len(items.requests) != 1 || len(items.others) != 1 {
		t.Fatalf("unexpected request/other split: %d/%d", len(items.requests), len(items.others))
	}
}

func TestCollateEmailItemsSortsEpisodesWithinSeries(t *testing.T) {
	rows := []DeliveryRow{
		emailEpisodeRow("01A", "profile-1", "ep-2", 2, 4),
		emailEpisodeRow("01B", "profile-1", "ep-1", 2, 3),
	}
	items := collateEmailItems(rows)
	first := items.series[0].episodes[0]
	if *first.EpisodeNumber != 3 {
		t.Fatalf("episodes not sorted by number: got E%d first", *first.EpisodeNumber)
	}
}

func TestEmailSubject(t *testing.T) {
	single := collateEmailItems([]DeliveryRow{emailEpisodeRow("01A", "p1", "ep-1", 2, 3)})
	if got := emailSubject(EmailModePerEpisode, single); got != "New episode of Severance: S02E03 — Episode 3" {
		t.Fatalf("unexpected single-episode subject %q", got)
	}

	request := collateEmailItems([]DeliveryRow{requestFulfilledTestRow()})
	if got := emailSubject(EmailModePerEpisode, request); got != "Dune is now available" {
		t.Fatalf("unexpected single-request subject %q", got)
	}

	mixed := collateEmailItems([]DeliveryRow{
		emailEpisodeRow("01A", "p1", "ep-1", 2, 3),
		emailEpisodeRow("01B", "p1", "ep-2", 2, 4),
		requestFulfilledTestRow(),
	})
	if got := emailSubject(EmailModePerEpisode, mixed); got != "Silo: 2 new episodes, 1 request ready" {
		t.Fatalf("unexpected mixed subject %q", got)
	}
	if got := emailSubject(EmailModeDailyDigest, mixed); got != "Silo daily digest: 2 new episodes, 1 request ready" {
		t.Fatalf("unexpected digest subject %q", got)
	}
}

func TestCollateEmailItemsKeepsLifecycleAndFulfilledForSameRequest(t *testing.T) {
	// One request can produce an approved row and a fulfilled row in the same
	// window; both must render.
	rows := []DeliveryRow{
		requestDeclinedTestRow(),
		requestFulfilledTestRow(), // different request id is irrelevant; types differ
		{Delivery: Delivery{
			ID:          "01APPROVED",
			Type:        DeliveryTypeRequestApproved,
			ReasonFlags: []byte(`{"request_id":"01REQ","media_type":"movie","title":"Dune"}`),
		}},
	}
	items := collateEmailItems(rows)
	if len(items.requests) != 3 {
		t.Fatalf("expected 3 request rows (distinct types), got %d", len(items.requests))
	}
	if requestsAllFulfilled(items) {
		t.Fatal("mixed request rows must not report all-fulfilled")
	}
}

func TestRequestLineLifecycle(t *testing.T) {
	declined := requestDeclinedTestRow()
	if got := requestLine(declined); got != "Your request for Dune was declined — Already available in 4K" {
		t.Fatalf("unexpected declined line %q", got)
	}
	approved := requestDeclinedTestRow()
	approved.Type = DeliveryTypeRequestApproved
	approved.ReasonFlags = []byte(`{"request_id":"01REQ","title":"Dune"}`)
	if got := requestLine(approved); got != "Your request for Dune was approved" {
		t.Fatalf("unexpected approved line %q", got)
	}

	subject := emailSubject(EmailModePerEpisode, collateEmailItems([]DeliveryRow{approved}))
	if subject != "Your request for Dune was approved" {
		t.Fatalf("unexpected single-update subject %q", subject)
	}
	mixed := collateEmailItems([]DeliveryRow{approved, requestFulfilledTestRow()})
	if got := emailSubject(EmailModeDailyDigest, mixed); got != "Silo daily digest: 2 request updates" {
		t.Fatalf("unexpected mixed subject %q", got)
	}
}

func TestComposeNotificationEmailLinks(t *testing.T) {
	rows := []DeliveryRow{emailEpisodeRow("01A", "p1", "ep-1", 2, 3)}

	withLinks := composeNotificationEmail(EmailModePerEpisode, rows, emailComposeOptions{BaseURL: "https://silo.example.com"})
	if !strings.Contains(withLinks.HTML, `href="https://silo.example.com/item/ep-1"`) {
		t.Fatalf("episode link missing from HTML:\n%s", withLinks.HTML)
	}
	if !strings.Contains(withLinks.HTML, `href="https://silo.example.com/settings/notifications"`) {
		t.Fatalf("settings link missing from HTML footer:\n%s", withLinks.HTML)
	}

	withoutLinks := composeNotificationEmail(EmailModePerEpisode, rows, emailComposeOptions{})
	if strings.Contains(withoutLinks.HTML, "href=") {
		t.Fatalf("HTML contains links with no external URL configured:\n%s", withoutLinks.HTML)
	}
	if !strings.Contains(withoutLinks.Text, "S02E03 — Episode 3") {
		t.Fatalf("text body missing episode line:\n%s", withoutLinks.Text)
	}
}

func TestComposeNotificationEmailEscapesHTML(t *testing.T) {
	row := emailEpisodeRow("01A", "p1", "ep-1", 2, 3)
	row.SeriesTitle = `<script>alert("x")</script>`
	content := composeNotificationEmail(EmailModePerEpisode, []DeliveryRow{row}, emailComposeOptions{})
	if strings.Contains(content.HTML, "<script>") {
		t.Fatalf("series title not escaped:\n%s", content.HTML)
	}
}

func TestComposeNotificationEmailProfileAndUnsubscribe(t *testing.T) {
	rows := []DeliveryRow{emailEpisodeRow("01A", "p1", "ep-1", 2, 3)}
	opts := emailComposeOptions{
		BaseURL:        "https://silo.example.com",
		ProfileName:    "Emma & <Kids>",
		UnsubscribeURL: "https://silo.example.com/api/v1/notifications/email/unsubscribe?token=tok",
	}
	content := composeNotificationEmail(EmailModePerEpisode, rows, opts)
	if !strings.Contains(content.Subject, "(for Emma & <Kids>)") {
		t.Fatalf("subject missing profile label: %q", content.Subject)
	}
	if strings.Contains(content.HTML, "<Kids>") {
		t.Fatalf("profile name not escaped in HTML:\n%s", content.HTML)
	}
	if !strings.Contains(content.HTML, `href="https://silo.example.com/api/v1/notifications/email/unsubscribe?token=tok"`) {
		t.Fatalf("unsubscribe link missing from HTML:\n%s", content.HTML)
	}
	if !strings.Contains(content.Text, "To stop these emails, open: "+opts.UnsubscribeURL) {
		t.Fatalf("unsubscribe link missing from text:\n%s", content.Text)
	}

	plain := composeNotificationEmail(EmailModePerEpisode, rows, emailComposeOptions{})
	if strings.Contains(plain.Subject, "(for") || strings.Contains(plain.Text, "To stop these emails") {
		t.Fatalf("profile/unsubscribe copy leaked into unconfigured email: %q", plain.Subject)
	}
}

func TestComposeVerificationEmail(t *testing.T) {
	content := composeVerificationEmail(`<b>Emma</b>`, "https://silo.example.com/api/v1/notifications/email/verify?token=tok")
	if !strings.Contains(content.Text, "https://silo.example.com/api/v1/notifications/email/verify?token=tok") {
		t.Fatalf("verify link missing from text:\n%s", content.Text)
	}
	if strings.Contains(content.HTML, "<b>Emma</b>") {
		t.Fatalf("profile name not escaped:\n%s", content.HTML)
	}
}

func TestEmailTokens(t *testing.T) {
	token, hash, err := newEmailToken()
	if err != nil {
		t.Fatalf("newEmailToken: %v", err)
	}
	if token == "" || hash == "" || token == hash {
		t.Fatalf("degenerate token/hash: %q / %q", token, hash)
	}
	if hashEmailToken(token) != hash {
		t.Fatal("hashEmailToken does not round-trip newEmailToken")
	}
	other, _, _ := newEmailToken()
	if other == token {
		t.Fatal("tokens are not unique")
	}
}

func TestEmailUnsubscribeURL(t *testing.T) {
	if got := emailUnsubscribeURL("", "tok"); got != "" {
		t.Fatalf("URL built without a base: %q", got)
	}
	if got := emailUnsubscribeURL("https://x", ""); got != "" {
		t.Fatalf("URL built without a token: %q", got)
	}
	want := "https://x/api/v1/notifications/email/unsubscribe?token=tok"
	if got := emailUnsubscribeURL("https://x", "tok"); got != want {
		t.Fatalf("unexpected unsubscribe URL %q", got)
	}
}

func TestComposeNotificationEmailCapsRenderedItems(t *testing.T) {
	rows := make([]DeliveryRow, 0, emailMaxItemsRendered+10)
	for i := range emailMaxItemsRendered + 10 {
		rows = append(rows, emailEpisodeRow(
			fmt.Sprintf("01%03d", i), "p1", fmt.Sprintf("ep-%d", i), 1, i+1))
	}
	content := composeNotificationEmail(EmailModeDailyDigest, rows, emailComposeOptions{})
	if !strings.Contains(content.Text, "and 10 more in your Silo inbox") {
		t.Fatalf("overflow line missing:\n%s", content.Text)
	}
	if count := strings.Count(content.HTML, "<li"); count != emailMaxItemsRendered {
		t.Fatalf("expected %d rendered items, got %d", emailMaxItemsRendered, count)
	}
}
