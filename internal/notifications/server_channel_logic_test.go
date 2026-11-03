package notifications

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	testMovieTitle       = "Dune"
	testSeriesTitle      = "Severance"
	testSeriesPosterCDN  = "https://image.tmdb.org/t/p/w500/severance.jpg"
	testSeriesPosterPath = "tmdb://poster/severance.jpg"
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
	return itemEvent(id, libraryID, EventKindMovie, itemID)
}

func itemEvent(id string, libraryID int, kind, itemID string) ReleaseEvent {
	return ReleaseEvent{
		ID:        id,
		LibraryID: libraryID,
		Kind:      kind,
		ItemID:    itemID,
	}
}

func TestGroupContentEvents(t *testing.T) {
	titles := map[string]ContentMeta{
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

	t.Run("pre-discriminator rows with empty kind group as episodes", func(t *testing.T) {
		event := episodeEvent("1", 1, "series-1", 1, 1)
		event.Kind = ""
		groups := GroupContentEvents([]ReleaseEvent{event}, titles)
		if len(groups) != 1 || groups[0].Kind != EventKindEpisode {
			t.Fatalf("unexpected groups %+v", groups)
		}
	})

	t.Run("same item id in two kinds never cross-dedupes", func(t *testing.T) {
		events := []ReleaseEvent{
			itemEvent("1", 1, EventKindAudiobook, "item-1"),
			itemEvent("2", 1, EventKindEbook, "item-1"),
		}
		if groups := GroupContentEvents(events, nil); len(groups) != 2 {
			t.Fatalf("got %d groups, want 2 (dedupe must be kind-scoped)", len(groups))
		}
	})

	t.Run("unknown future kinds are skipped, not misrendered", func(t *testing.T) {
		events := []ReleaseEvent{
			itemEvent("1", 1, "music", "album-1"),
			movieEvent("2", 1, "movie-1"),
		}
		groups := GroupContentEvents(events, titles)
		if len(groups) != 1 || groups[0].Kind != EventKindMovie {
			t.Fatalf("unexpected groups %+v", groups)
		}
	})
}

func TestBuildServerChannelDiscordContentOverflow(t *testing.T) {
	groups := make([]ContentGroup, 0, 14)
	for i := 0; i < 14; i++ {
		groups = append(groups, ContentGroup{
			Kind:   EventKindMovie,
			ItemID: "movie",
			Meta:   ContentMeta{Title: "Movie", Year: 2000 + i},
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
	titles := map[string]ContentMeta{"series-1": {Title: testSeriesTitle}, "movie-1": {Title: testMovieTitle, Year: 2026}}
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

func TestBuildServerChannelRequestDiscordMentionsRequester(t *testing.T) {
	info := RequestEventInfo{
		RequestID:          "req-1",
		TMDBID:             42,
		MediaType:          "movie",
		Title:              testMovieTitle,
		RequesterName:      "quick",
		RequesterUserID:    7,
		RequesterDiscordID: "123456789",
	}

	body, err := BuildServerChannelRequestDiscord(ServerChannelEventRequestApproved, info)
	if err != nil {
		t.Fatal(err)
	}
	var discord discordWebhookBody
	if err := json.Unmarshal(body, &discord); err != nil {
		t.Fatal(err)
	}
	if discord.Content != "<@123456789>" {
		t.Fatalf("unexpected content %q", discord.Content)
	}
	if discord.AllowedMentions == nil ||
		len(discord.AllowedMentions.Users) != 1 || discord.AllowedMentions.Users[0] != "123456789" {
		t.Fatalf("unexpected allowed mentions %+v", discord.AllowedMentions)
	}
	// Parse must serialize as an empty list (not be omitted) so Discord's
	// implicit mention parsing stays disabled.
	if !strings.Contains(string(body), `"parse":[]`) {
		t.Fatalf("allowed_mentions.parse not serialized: %s", body)
	}
	// The embed keeps the plain username; only the content line pings.
	if len(discord.Embeds) != 1 || discord.Embeds[0].Fields[1].Value != "quick" {
		t.Fatalf("unexpected embed fields %+v", discord.Embeds)
	}

	// The generic payload never carries the Discord identity.
	generic, err := BuildServerChannelRequestGeneric(ServerChannelEventRequestApproved, info, "chan-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(generic), "123456789") {
		t.Fatalf("discord id leaked into generic payload: %s", generic)
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

func TestReleaseDedupeKeysAreDisjoint(t *testing.T) {
	episodeKey := EpisodeDedupeKey(3, "series-abc", EpisodeKey(2, 4))
	if episodeKey != "episode:3:series-abc:2000004" {
		t.Fatalf("unexpected episode dedupe key %q", episodeKey)
	}
	if ItemDedupeKey(EventKindMovie, 3, "series-abc:2000004") == episodeKey {
		t.Fatal("movie and episode dedupe keys must live in separate keyspaces")
	}
	if got := ItemDedupeKey(EventKindMovie, 3, "abc"); got != "movie:3:abc" {
		t.Fatalf("unexpected movie dedupe key %q", got)
	}
	if got := ItemDedupeKey(EventKindAudiobook, 3, "abc"); got != "audiobook:3:abc" {
		t.Fatalf("unexpected audiobook dedupe key %q", got)
	}
	if ItemDedupeKey(EventKindAudiobook, 3, "abc") == ItemDedupeKey(EventKindEbook, 3, "abc") {
		t.Fatal("flat kinds must keep disjoint dedupe keyspaces")
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
		itemEvent("5", 1, EventKindAudiobook, "b"),
		itemEvent("6", 1, EventKindEbook, "e"),
	}
	episodes, others := PartitionEventsByKind(events)
	if len(episodes) != 3 || len(others) != 3 {
		t.Fatalf("got %d/%d, want 3 episodes and 3 others", len(episodes), len(others))
	}
	if episodes[0].ID != "1" || episodes[1].ID != "2" || episodes[2].ID != "4" {
		t.Fatalf("episode order not preserved: %+v", episodes)
	}
	if others[0].ID != "3" || others[1].ID != "5" || others[2].ID != "6" {
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
