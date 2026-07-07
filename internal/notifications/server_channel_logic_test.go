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
		if groups[0].Meta.Title != genericEpisodeTitle {
			t.Fatalf("unexpected series fallback %q", groups[0].Meta.Title)
		}
		if groups[1].Meta.Title != "New movie" {
			t.Fatalf("unexpected movie fallback %q", groups[1].Meta.Title)
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

	t.Run("audiobooks and ebooks render like movies with author metadata", func(t *testing.T) {
		metas := map[string]ContentMeta{
			"book-1": {Title: "Project Hail Mary", Year: 2021, Author: "Andy Weir"},
		}
		groups := GroupContentEvents([]ReleaseEvent{
			itemEvent("1", 1, EventKindAudiobook, "book-1"),
		}, metas)
		if len(groups) != 1 || groups[0].Kind != EventKindAudiobook {
			t.Fatalf("unexpected groups %+v", groups)
		}
		if got := contentGroupTitle(groups[0]); got != "Project Hail Mary (2021)" {
			t.Fatalf("unexpected audiobook title %q", got)
		}
		if groups[0].Meta.Author != "Andy Weir" {
			t.Fatalf("author not carried: %+v", groups[0].Meta)
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

	t.Run("same audiobook in two libraries announces once", func(t *testing.T) {
		events := []ReleaseEvent{
			itemEvent("1", 1, EventKindAudiobook, "book-1"),
			itemEvent("2", 2, EventKindAudiobook, "book-1"),
		}
		if groups := GroupContentEvents(events, nil); len(groups) != 1 {
			t.Fatalf("got %d groups, want 1 (cross-library dedupe)", len(groups))
		}
	})

	t.Run("missing flat item titles fall back per kind", func(t *testing.T) {
		groups := GroupContentEvents([]ReleaseEvent{
			itemEvent("1", 1, EventKindAudiobook, "a"),
			itemEvent("2", 1, EventKindEbook, "b"),
		}, nil)
		if groups[0].Meta.Title != "New audiobook" || groups[1].Meta.Title != "New ebook" {
			t.Fatalf("unexpected fallbacks %q / %q", groups[0].Meta.Title, groups[1].Meta.Title)
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

func TestServerChannelWantsToggles(t *testing.T) {
	ch := ServerChannel{
		NotifyNewMovies:        true,
		NotifyNewEpisodes:      false,
		NotifyNewAudiobooks:    true,
		NotifyNewEbooks:        false,
		NotifyRequestSubmitted: true,
		NotifyRequestFulfilled: false,
	}
	cases := []struct {
		kind string
		want bool
	}{
		{EventKindMovie, true},
		{EventKindEpisode, false},
		{EventKindAudiobook, true},
		{EventKindEbook, false},
		{"", false},      // legacy rows follow the episode toggle
		{"music", false}, // unknown future kinds are never announced
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
	titles := map[string]ContentMeta{
		"series-1": {
			Title:         testSeriesTitle,
			Type:          "series",
			Overview:      "Mark leads a team whose memories have been surgically divided.",
			PosterPath:    testSeriesPosterPath,
			PosterURL:     testSeriesPosterCDN,
			Genres:        []string{"Drama", "Sci-Fi & Fantasy", "Mystery", "Thriller"},
			ContentRating: "TV-MA",
			RatingIMDB:    8.7,
			IMDBID:        "tt11280740",
			TMDBID:        "95396",
			TVDBID:        "371980",
		},
		"movie-1": {Title: testMovieTitle, Year: 2026, Type: "movie"},
	}
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

	episodes := decoded.Embeds[0]
	if episodes.Author == nil || episodes.Author.Name != "New episodes available on Silo" {
		t.Fatalf("unexpected author %+v", episodes.Author)
	}
	if episodes.URL != "https://www.themoviedb.org/tv/95396" {
		t.Fatalf("unexpected title URL %q", episodes.URL)
	}
	if episodes.Thumbnail == nil || episodes.Thumbnail.URL != testSeriesPosterCDN {
		t.Fatalf("unexpected thumbnail %+v", episodes.Thumbnail)
	}
	if !strings.HasPrefix(episodes.Description, "Mark leads a team") ||
		!strings.Contains(episodes.Description, "[IMDb](https://www.imdb.com/title/tt11280740/)") ||
		!strings.Contains(episodes.Description, "[TVDB](https://thetvdb.com/dereferrer/series/371980)") {
		t.Fatalf("unexpected description %q", episodes.Description)
	}
	if len(episodes.Fields) != 2 ||
		episodes.Fields[0].Value != "★ 8.7 IMDb" ||
		episodes.Fields[1].Value != "Drama, Sci-Fi & Fantasy, Mystery" {
		t.Fatalf("unexpected fields %+v", episodes.Fields)
	}
	if episodes.Footer == nil || episodes.Footer.Text != "Silo • TV-MA" {
		t.Fatalf("unexpected footer %+v", episodes.Footer)
	}

	// Embeds may name public provider origins only — never this server's.
	movie := decoded.Embeds[1]
	if movie.URL != "" || movie.Thumbnail != nil {
		t.Fatalf("metadata-less movie must omit url/thumbnail, got %+v", movie)
	}
}

func TestBuildServerChannelDiscordContentAudiobook(t *testing.T) {
	metas := map[string]ContentMeta{
		"book-1": {
			Title:  "Project Hail Mary",
			Year:   2021,
			Type:   "audiobook",
			Author: "Andy Weir",
			Genres: []string{"Sci-Fi"},
		},
		"ebook-1": {Title: "The Martian", Year: 2011, Type: "ebook", Author: "Andy Weir"},
	}
	groups := GroupContentEvents([]ReleaseEvent{
		itemEvent("1", 1, EventKindAudiobook, "book-1"),
		itemEvent("2", 1, EventKindEbook, "ebook-1"),
	}, metas)

	body, err := BuildServerChannelDiscordContent(groups, false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Embeds []discordEmbed `json:"embeds"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Embeds) != 2 {
		t.Fatalf("got %d embeds, want 2", len(decoded.Embeds))
	}

	audiobook := decoded.Embeds[0]
	if audiobook.Author == nil || audiobook.Author.Name != "New audiobook available on Silo" {
		t.Fatalf("unexpected author line %+v", audiobook.Author)
	}
	if audiobook.Title != "Project Hail Mary (2021)" {
		t.Fatalf("unexpected title %q", audiobook.Title)
	}
	if len(audiobook.Fields) != 2 ||
		audiobook.Fields[0].Name != "Author" || audiobook.Fields[0].Value != "Andy Weir" ||
		audiobook.Fields[1].Name != "Genres" || audiobook.Fields[1].Value != "Sci-Fi" {
		t.Fatalf("unexpected fields %+v", audiobook.Fields)
	}

	ebook := decoded.Embeds[1]
	if ebook.Author == nil || ebook.Author.Name != "New ebook available on Silo" {
		t.Fatalf("unexpected ebook author line %+v", ebook.Author)
	}
	if len(ebook.Fields) != 1 || ebook.Fields[0].Name != "Author" {
		t.Fatalf("unexpected ebook fields %+v", ebook.Fields)
	}
}

func TestBuildServerChannelGenericContentAudiobook(t *testing.T) {
	metas := map[string]ContentMeta{
		"book-1": {Title: "Project Hail Mary", Year: 2021, Author: "Andy Weir"},
	}
	groups := GroupContentEvents([]ReleaseEvent{
		itemEvent("1", 4, EventKindAudiobook, "book-1"),
	}, metas)

	body, err := BuildServerChannelGenericContent(groups, "chan-1", false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded serverChannelContentBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(decoded.Items))
	}
	item := decoded.Items[0]
	if item.Kind != EventKindAudiobook || item.ItemID != "book-1" ||
		item.Title != "Project Hail Mary" || item.Year != 2021 ||
		item.Author != "Andy Weir" || item.LibraryID != 4 {
		t.Fatalf("unexpected audiobook item %+v", item)
	}
	// Flat items never carry episode span fields.
	if item.EpisodeCount != 0 || item.SeriesID != "" {
		t.Fatalf("unexpected episode fields on flat item %+v", item)
	}
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

func TestBuildServerChannelRequestPayloads(t *testing.T) {
	info := RequestEventInfo{
		RequestID:     "req-1",
		TMDBID:        42,
		MediaType:     "movie",
		Title:         testMovieTitle,
		Year:          2026,
		Overview:      "Paul Atreides unites with the Fremen.",
		PosterPath:    "/dune.jpg",
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
	if embed.Author == nil || embed.Author.Name != "New media request on Silo" {
		t.Fatalf("unexpected author %+v", embed.Author)
	}
	if embed.URL != "https://www.themoviedb.org/movie/42" {
		t.Fatalf("unexpected title URL %q", embed.URL)
	}
	if !strings.HasPrefix(embed.Description, "Paul Atreides unites") ||
		!strings.Contains(embed.Description, "[TMDB](https://www.themoviedb.org/movie/42)") {
		t.Fatalf("unexpected description %q", embed.Description)
	}
	if embed.Thumbnail == nil || embed.Thumbnail.URL != "https://image.tmdb.org/t/p/w500/dune.jpg" {
		t.Fatalf("unexpected thumbnail %+v", embed.Thumbnail)
	}
	if len(embed.Fields) != 2 || embed.Fields[1].Value != "quick" {
		t.Fatalf("unexpected fields %+v", embed.Fields)
	}
	// Without a resolved Discord identity there is no mention.
	if discord.Content != "" || discord.AllowedMentions != nil {
		t.Fatalf("unexpected mention without discord id: content=%q mentions=%+v",
			discord.Content, discord.AllowedMentions)
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
