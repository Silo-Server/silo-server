package notifications

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	testMovieTitle  = "Dune"
	testSeriesTitle = "Severance"
)

func episodeEvent(id string, libraryID int, seriesID string, season, episode int) ReleaseEvent {
	return ReleaseEvent{
		ID:            id,
		LibraryID:     libraryID,
		Kind:          EventKindEpisode,
		SeriesID:      seriesID,
		EpisodeID:     "ep-" + id,
		SeasonNumber:  season,
		EpisodeNumber: episode,
		EpisodeKey:    EpisodeKey(season, episode),
	}
}

func movieEvent(id string, libraryID int, itemID string) ReleaseEvent {
	return ReleaseEvent{
		ID:        id,
		LibraryID: libraryID,
		Kind:      EventKindMovie,
		ItemID:    itemID,
	}
}

func TestGroupContentEvents(t *testing.T) {
	titles := map[string]ContentTitle{
		"series-1": {Title: testSeriesTitle},
		"movie-1":  {Title: testMovieTitle, Year: 2026},
	}

	t.Run("season pack groups into one entry with a range", func(t *testing.T) {
		events := []ReleaseEvent{
			episodeEvent("3", 1, "series-1", 2, 3),
			episodeEvent("1", 1, "series-1", 2, 1),
			episodeEvent("2", 1, "series-1", 2, 2),
		}
		groups := GroupContentEvents(events, titles)
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1", len(groups))
		}
		if got := contentGroupTitle(groups[0]); got != "Severance — 3 new episodes (S2 E1–E3)" {
			t.Fatalf("unexpected group title %q", got)
		}
	})

	t.Run("single episode renders its code", func(t *testing.T) {
		groups := GroupContentEvents([]ReleaseEvent{episodeEvent("1", 1, "series-1", 2, 5)}, titles)
		if got := contentGroupTitle(groups[0]); got != "Severance — S2 E5" {
			t.Fatalf("unexpected group title %q", got)
		}
	})

	t.Run("cross-season range spells both codes", func(t *testing.T) {
		events := []ReleaseEvent{
			episodeEvent("1", 1, "series-1", 1, 10),
			episodeEvent("2", 1, "series-1", 2, 1),
		}
		groups := GroupContentEvents(events, titles)
		if got := contentGroupTitle(groups[0]); got != "Severance — 2 new episodes (S1 E10 – S2 E1)" {
			t.Fatalf("unexpected group title %q", got)
		}
	})

	t.Run("movies render individually with year", func(t *testing.T) {
		groups := GroupContentEvents([]ReleaseEvent{movieEvent("1", 1, "movie-1")}, titles)
		if len(groups) != 1 || groups[0].Kind != EventKindMovie {
			t.Fatalf("unexpected groups %+v", groups)
		}
		if got := contentGroupTitle(groups[0]); got != "Dune (2026)" {
			t.Fatalf("unexpected movie title %q", got)
		}
	})

	t.Run("same movie in two libraries announces once", func(t *testing.T) {
		events := []ReleaseEvent{
			movieEvent("1", 1, "movie-1"),
			movieEvent("2", 2, "movie-1"),
		}
		if groups := GroupContentEvents(events, titles); len(groups) != 1 {
			t.Fatalf("got %d groups, want 1 (cross-library movie dedupe)", len(groups))
		}
	})

	t.Run("same series in two libraries stays separate", func(t *testing.T) {
		// Episode-level cross-library dedupe is the per-profile path's job;
		// the broadcast feed reflects each library's catalog.
		events := []ReleaseEvent{
			episodeEvent("1", 1, "series-1", 1, 1),
			episodeEvent("2", 2, "series-1", 1, 1),
		}
		if groups := GroupContentEvents(events, titles); len(groups) != 2 {
			t.Fatalf("got %d groups, want 2", len(groups))
		}
	})

	t.Run("missing titles fall back to generic labels", func(t *testing.T) {
		groups := GroupContentEvents([]ReleaseEvent{
			episodeEvent("1", 1, "unknown-series", 1, 1),
			movieEvent("2", 1, "unknown-movie"),
		}, nil)
		if groups[0].SeriesTitle != genericEpisodeTitle {
			t.Fatalf("unexpected series fallback %q", groups[0].SeriesTitle)
		}
		if groups[1].Title != "New movie" {
			t.Fatalf("unexpected movie fallback %q", groups[1].Title)
		}
	})

	t.Run("pre-discriminator rows with empty kind group as episodes", func(t *testing.T) {
		event := episodeEvent("1", 1, "series-1", 1, 1)
		event.Kind = ""
		groups := GroupContentEvents([]ReleaseEvent{event}, titles)
		if len(groups) != 1 || groups[0].Kind != EventKindEpisode {
			t.Fatalf("unexpected groups %+v", groups)
		}
	})
}

func TestServerChannelWantsToggles(t *testing.T) {
	ch := ServerChannel{
		NotifyNewMovies:        true,
		NotifyNewEpisodes:      false,
		NotifyRequestSubmitted: true,
		NotifyRequestFulfilled: false,
	}
	cases := []struct {
		kind string
		want bool
	}{
		{EventKindMovie, true},
		{EventKindEpisode, false},
		{"", false},          // legacy rows follow the episode toggle
		{"audiobook", false}, // unknown future kinds are never announced
	}
	for _, tc := range cases {
		if got := ch.WantsContentKind(tc.kind); got != tc.want {
			t.Errorf("WantsContentKind(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}

	eventCases := []struct {
		event string
		want  bool
	}{
		{ServerChannelEventRequestSubmitted, true},
		{ServerChannelEventRequestApproved, false},
		{ServerChannelEventRequestDeclined, false},
		{ServerChannelEventRequestFulfilled, false},
		{"request.unknown", false},
	}
	for _, tc := range eventCases {
		if got := ch.WantsRequestEvent(tc.event); got != tc.want {
			t.Errorf("WantsRequestEvent(%q) = %v, want %v", tc.event, got, tc.want)
		}
	}
}

func TestBuildServerChannelDiscordContent(t *testing.T) {
	titles := map[string]ContentTitle{"series-1": {Title: testSeriesTitle}, "movie-1": {Title: testMovieTitle, Year: 2026}}
	groups := GroupContentEvents([]ReleaseEvent{
		episodeEvent("1", 1, "series-1", 2, 1),
		episodeEvent("2", 1, "series-1", 2, 2),
		movieEvent("3", 1, "movie-1"),
	}, titles)

	body, err := BuildServerChannelDiscordContent(groups, false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Content  string         `json:"content"`
		Username string         `json:"username"`
		Embeds   []discordEmbed `json:"embeds"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Embeds) != 2 {
		t.Fatalf("got %d embeds, want 2", len(decoded.Embeds))
	}
	if decoded.Username != "Silo" {
		t.Fatalf("unexpected username %q", decoded.Username)
	}
	if decoded.Content != "" {
		t.Fatalf("no overflow expected, got content %q", decoded.Content)
	}
	// The v1 embed shape must not name any fetchable origin.
	if strings.Contains(string(body), `"image"`) || strings.Contains(string(body), `"url"`) ||
		strings.Contains(string(body), `"thumbnail"`) {
		t.Fatal("server channel embeds must not contain image/url/thumbnail fields")
	}
}

func TestBuildServerChannelDiscordContentOverflow(t *testing.T) {
	groups := make([]ContentGroup, 0, 14)
	for i := 0; i < 14; i++ {
		groups = append(groups, ContentGroup{
			Kind:   EventKindMovie,
			ItemID: "movie",
			Title:  "Movie",
			Year:   2000 + i,
		})
	}
	body, err := BuildServerChannelDiscordContent(groups, false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Content string         `json:"content"`
		Embeds  []discordEmbed `json:"embeds"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Embeds) != serverChannelMaxEmbeds {
		t.Fatalf("got %d embeds, want %d", len(decoded.Embeds), serverChannelMaxEmbeds)
	}
	if !strings.Contains(decoded.Content, "4 more") {
		t.Fatalf("overflow line missing, got %q", decoded.Content)
	}
	// Newest groups are kept: the first four (oldest) drop.
	if decoded.Embeds[0].Title != "Movie (2004)" {
		t.Fatalf("expected oldest retained embed to be Movie (2004), got %q", decoded.Embeds[0].Title)
	}
}

func TestBuildServerChannelGenericContent(t *testing.T) {
	titles := map[string]ContentTitle{"series-1": {Title: testSeriesTitle}, "movie-1": {Title: testMovieTitle, Year: 2026}}
	groups := GroupContentEvents([]ReleaseEvent{
		episodeEvent("1", 7, "series-1", 2, 1),
		episodeEvent("2", 7, "series-1", 2, 3),
		movieEvent("3", 7, "movie-1"),
	}, titles)

	body, err := BuildServerChannelGenericContent(groups, "chan-1", true)
	if err != nil {
		t.Fatal(err)
	}
	var decoded serverChannelContentBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Event != ServerChannelEventContentAdded || decoded.ChannelID != "chan-1" || !decoded.Test {
		t.Fatalf("unexpected envelope %+v", decoded)
	}
	if len(decoded.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(decoded.Items))
	}
	episodes := decoded.Items[0]
	if episodes.SeriesTitle != testSeriesTitle || episodes.EpisodeCount != 2 ||
		episodes.FirstSeason != 2 || episodes.FirstEpisode != 1 ||
		episodes.LastSeason != 2 || episodes.LastEpisode != 3 {
		t.Fatalf("unexpected episode item %+v", episodes)
	}
	movie := decoded.Items[1]
	if movie.Kind != EventKindMovie || movie.Title != testMovieTitle || movie.Year != 2026 || movie.LibraryID != 7 {
		t.Fatalf("unexpected movie item %+v", movie)
	}
}

func TestBuildServerChannelRequestPayloads(t *testing.T) {
	info := RequestEventInfo{
		RequestID:     "req-1",
		TMDBID:        42,
		MediaType:     "movie",
		Title:         testMovieTitle,
		Year:          2026,
		RequesterName: "quick",
	}

	body, err := BuildServerChannelRequestDiscord(ServerChannelEventRequestSubmitted, info)
	if err != nil {
		t.Fatal(err)
	}
	var discord discordWebhookBody
	if err := json.Unmarshal(body, &discord); err != nil {
		t.Fatal(err)
	}
	if len(discord.Embeds) != 1 {
		t.Fatalf("got %d embeds, want 1", len(discord.Embeds))
	}
	embed := discord.Embeds[0]
	if embed.Title != "Dune (2026)" {
		t.Fatalf("unexpected title %q", embed.Title)
	}
	if embed.Description != "New media request on Silo" {
		t.Fatalf("unexpected description %q", embed.Description)
	}
	if len(embed.Fields) != 2 || embed.Fields[1].Value != "quick" {
		t.Fatalf("unexpected fields %+v", embed.Fields)
	}

	generic, err := BuildServerChannelRequestGeneric(ServerChannelEventRequestDeclined, info, "chan-1")
	if err != nil {
		t.Fatal(err)
	}
	var decoded serverChannelRequestBody
	if err := json.Unmarshal(generic, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Event != ServerChannelEventRequestDeclined || decoded.Request.ID != "req-1" ||
		decoded.Request.RequesterName != "quick" {
		t.Fatalf("unexpected generic body %+v", decoded)
	}
}

func TestServerChannelHeadersSigned(t *testing.T) {
	body := []byte(`{"event":"content.added"}`)
	now := time.Unix(1_700_000_000, 0)
	headers := serverChannelHeaders(ServerChannelEventContentAdded, "chan-1", "secret", now, body)
	if headers["X-Silo-Event"] != ServerChannelEventContentAdded {
		t.Fatalf("unexpected event header %q", headers["X-Silo-Event"])
	}
	if headers["X-Silo-Channel-Id"] != "chan-1" {
		t.Fatalf("unexpected channel header %q", headers["X-Silo-Channel-Id"])
	}
	want := SignGenericWebhook("secret", now.Unix(), body)
	if headers["X-Silo-Signature"] != want {
		t.Fatalf("signature mismatch: %q != %q", headers["X-Silo-Signature"], want)
	}
}

func TestMovieDedupeKeyDisjointFromEpisodeKeys(t *testing.T) {
	// Episode dedupe keys are "{library_id}:{episode_id}". A movie key must
	// never collide even if a movie item id equals an episode id.
	if MovieDedupeKey(3, "abc") == "3:abc" {
		t.Fatal("movie dedupe keys must live in their own keyspace")
	}
	if got := MovieDedupeKey(3, "abc"); got != "movie:3:abc" {
		t.Fatalf("unexpected movie dedupe key %q", got)
	}
}

func TestPartitionEventsByKind(t *testing.T) {
	legacy := episodeEvent("2", 1, "s", 1, 2)
	legacy.Kind = "" // rows that predate the kind column
	events := []ReleaseEvent{
		episodeEvent("1", 1, "s", 1, 1),
		legacy,
		movieEvent("3", 1, "m"),
		episodeEvent("4", 1, "s", 1, 3),
	}
	episodes, others := PartitionEventsByKind(events)
	if len(episodes) != 3 || len(others) != 1 {
		t.Fatalf("got %d/%d, want 3 episodes and 1 other", len(episodes), len(others))
	}
	if episodes[0].ID != "1" || episodes[1].ID != "2" || episodes[2].ID != "4" {
		t.Fatalf("episode order not preserved: %+v", episodes)
	}
	if others[0].ID != "3" {
		t.Fatalf("unexpected non-episode partition: %+v", others)
	}

	// The fanout path applies the burst cap only to the episode partition;
	// movies with empty series_id must never reach it, or they would all
	// collapse into one (library, "") burst group.
	fanout, suppressed := ApplyBurstCap(episodes, 2)
	if len(fanout)+len(suppressed) != len(episodes) {
		t.Fatalf("burst cap lost events: %d + %d != %d", len(fanout), len(suppressed), len(episodes))
	}
	for _, event := range append(fanout, suppressed...) {
		if event.Kind == EventKindMovie {
			t.Fatal("movie event leaked into burst-capped fanout set")
		}
	}
}

func TestMaxCursor(t *testing.T) {
	// Watermark advancement clamps through maxCursor so a watermark can never
	// move backward (used by the server-channel sweep and digest legs).
	earlier := Cursor{CreatedAt: time.Unix(1000, 0), ID: "a"}
	later := Cursor{CreatedAt: time.Unix(2000, 0), ID: "b"}
	if got := maxCursor(earlier, later); got != later {
		t.Fatalf("maxCursor(earlier, later) = %+v, want later", got)
	}
	if got := maxCursor(later, earlier); got != later {
		t.Fatalf("maxCursor(later, earlier) = %+v, want later", got)
	}
	// Same timestamp orders by id, matching the delivery queries.
	tie := Cursor{CreatedAt: time.Unix(2000, 0), ID: "c"}
	if got := maxCursor(later, tie); got != tie {
		t.Fatalf("maxCursor id tiebreak = %+v, want %+v", got, tie)
	}
}
