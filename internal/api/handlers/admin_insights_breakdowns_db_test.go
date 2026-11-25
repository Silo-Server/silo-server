package handlers

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type breakdownsSeed struct {
	user, movies, tv int
	now              time.Time
}

// seedBreakdowns writes a small catalog and three sessions for the Devices
// usage breakdowns and the Library read:
//
//	M1 Matrix (1999; Drama, Thriller; R) in Movies since 400 days ago, played
//	   5 days ago on its Japanese stereo track, audio converted to stereo, by
//	   Findroid 0.15 (a Jellyfin client by name).
//	M2 Palm Springs (2020; Comedy; PG-13) added to Movies 10 days ago, played
//	   36 h later with Spanish hearing-impaired subtitles on its default
//	   English stereo track, by Silo Android 1.2.0.
//	M3 Untitled (2023; Drama; no rating) added 2 days ago and never started.
//	S1 Dark (2017; Drama; TV-MA) in TV since 100 days ago; E1 played 3 days
//	   ago by Silo Android 1.2.0 on a row without route facts.
func seedBreakdowns(t *testing.T, pool *pgxpool.Pool) breakdownsSeed {
	t.Helper()
	ctx := t.Context()
	s := breakdownsSeed{now: time.Now().UTC()}
	row := func(dest *int, sql string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	row(&s.user, `INSERT INTO users (username, role) VALUES ('viewer', 'user') RETURNING id`)
	row(&s.movies, `INSERT INTO media_folders (type, name) VALUES ('movies', 'Movies') RETURNING id`)
	row(&s.tv, `INSERT INTO media_folders (type, name) VALUES ('tv', 'TV') RETURNING id`)
	exec(`INSERT INTO media_items (content_id, type, title, year, genres, content_rating) VALUES
		('M1', 'movie', 'The Matrix', 1999, '{Drama,Thriller}', 'R'),
		('M2', 'movie', 'Palm Springs', 2020, '{Comedy}', 'PG-13'),
		('M3', 'movie', 'Untitled', 2023, '{Drama}', ''),
		('S1', 'series', 'Dark', 2017, '{Drama}', 'TV-MA')`)
	exec(`INSERT INTO episodes (content_id, series_id, title, season_number, episode_number) VALUES ('E1', 'S1', 'Secrets', 1, 1)`)
	days := func(n float64) time.Time { return s.now.Add(-time.Duration(n * float64(24*time.Hour))) }
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES
		('M1', $1, $3), ('M2', $1, $4), ('M3', $1, $5), ('S1', $2, $6)`, s.movies, s.tv, days(400), days(10), days(2), days(100))
	var m1, m2, e1 int
	row(&m1, `INSERT INTO media_files (media_folder_id, file_path, content_id, audio_tracks) VALUES ($1, '/m/matrix.mkv', 'M1',
		'[{"language": "eng", "channels": 6, "default": true}, {"language": "jpn", "channels": 2}]') RETURNING id`, s.movies)
	row(&m2, `INSERT INTO media_files (media_folder_id, file_path, content_id, audio_tracks) VALUES ($1, '/m/palm.mkv', 'M2',
		'[{"language": "en", "channels": 2, "default": true}]') RETURNING id`, s.movies)
	exec(`INSERT INTO media_files (media_folder_id, file_path, content_id) VALUES ($1, '/m/untitled.mkv', 'M3')`, s.movies)
	row(&e1, `INSERT INTO media_files (media_folder_id, file_path, episode_id) VALUES ($1, '/tv/e1.mkv', 'E1') RETURNING id`, s.tv)

	session := func(item string, file int, start time.Time, client, version string, routeFacts bool, audioIndex, targetChannels any, audioDecision string, subtitle any) {
		t.Helper()
		exec(`INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at,
			 watched_seconds, played_seconds, measured, completed, client_name, client_version, device_platform,
			 route_facts_recorded, audio_track_index, target_audio_channels, audio_decision, subtitle)
			VALUES ($1, $2, 'p', $3, $4, 'direct', $5, $5::timestamptz + interval '2 hours', 7200, 7200, TRUE, TRUE,
			 $6, $7, 'Android', $8, $9, $10, $11, $12)`,
			"breakdowns-"+uuid.NewString(), s.user, item, file, start, client, version, routeFacts, audioIndex, targetChannels, audioDecision, subtitle)
	}
	session("M1", m1, days(5), "Findroid", "0.15", true, 1, 2, "transcode", nil)
	session("M2", m2, days(10).Add(36*time.Hour), "Silo Android", "1.2.0", true, nil, nil, "direct",
		`{"track_id": "s1", "language": "spa", "source": "embedded", "delivery": "embedded", "forced": false, "hearing_impaired": true}`)
	session("E1", e1, days(3), "Silo Android", "1.2.0", false, nil, nil, "", nil)
	return s
}

func TestAdminInsightsBreakdownsDB(t *testing.T) {
	pool := insightsTestPool(t)
	s := seedBreakdowns(t, pool)
	h := &AdminHandler{pool: pool}
	month := AdminInsightsWindowQuery{Range: InsightsRange30d, TZ: "UTC"}
	t.Run("devices usage", func(t *testing.T) { assertBreakdownsDevicesDB(t, h, month) })
	t.Run("library coverage", func(t *testing.T) { assertBreakdownsLibraryCoverageDB(t, h, s, month) })
	t.Run("library breakdowns", func(t *testing.T) { assertBreakdownsLibraryPlaysDB(t, h, s, month) })
	t.Run("library past window", func(t *testing.T) { assertBreakdownsLibraryPastWindowDB(t, h, pool, s) })
}

// Titles added at the same moment list, and are cut to the first 20, in
// content_id's "C" order whatever the database's default collation: "B-x"
// (upper case sorts first in C) leads and "a-20" is cut, where en_US would
// list a-01..a-20 and cut "B-x".
func TestInsightsLibraryUnplayedTieOrderDB(t *testing.T) {
	pool := insightsTestPool(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{"B-x"}
	for i := 1; i <= 20; i++ {
		ids = append(ids, fmt.Sprintf("a-%02d", i))
	}
	added := make([]insightsAddedTitle, 0, len(ids))
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, id); err != nil {
			t.Fatal(err)
		}
		added = append(added, insightsAddedTitle{contentID: id, addedAt: at})
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out := &AdminInsightsLibrary{}
	if err := queryInsightsLibraryUnplayed(ctx, tx, added, nil, out); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(out.RecentlyAddedUnplayed))
	for _, u := range out.RecentlyAddedUnplayed {
		got = append(got, u.ContentID)
	}
	if want := ids[:insightsLibraryUnplayedLimit]; !slices.Equal(got, want) {
		t.Errorf("unplayed = %v, want %v", got, want)
	}
}

// A window that ended before M2's first play: M2 is not played in it, but it
// is not "not played yet" or "never started" either, since both are about now.
// M4, also added in the window, gets a 10-second false start just before the
// window ends, resumed for an hour just after: inside the window that is no
// play, as every Insights read counts it, but as of now it is M4's first play.
func assertBreakdownsLibraryPastWindowDB(t *testing.T, h *AdminHandler, pool *pgxpool.Pool, s breakdownsSeed) {
	day := func(n int) string { return s.now.AddDate(0, 0, -n).Format("2006-01-02") }
	past := AdminInsightsWindowQuery{Range: InsightsRangeCustom, FromDate: day(12), ToDate: day(10), TZ: "UTC"}
	end := s.now.AddDate(0, 0, -9).Truncate(24 * time.Hour)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO media_items (content_id, type, title, year, genres, content_rating) VALUES ('M4', 'movie', 'Northbound', 2021, '{Drama}', 'PG')`)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ('M4', $1, $2)`, s.movies, end.Add(-72*time.Hour))
	exec(`INSERT INTO media_files (media_folder_id, file_path, content_id) VALUES ($1, '/m/northbound.mkv', 'M4')`, s.movies)
	for _, sitting := range []struct {
		start   time.Time
		seconds int
	}{{end.Add(-time.Hour), 10}, {end.Add(time.Hour), 3600}} {
		exec(`INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at,
			 watched_seconds, played_seconds, measured, completed, client_name)
			VALUES ($1, $2, 'p', 'M4', (SELECT id FROM media_files WHERE content_id = 'M4'), 'direct',
			 $3, $3::timestamptz + make_interval(secs => $4), $4, $4, TRUE, FALSE, 'Silo Web')`,
			"breakdowns-"+uuid.NewString(), s.user, sitting.start, sitting.seconds)
	}
	lib, err := h.ReadAdminInsightsLibrary(t.Context(), past, false)
	if err != nil {
		t.Fatal(err)
	}
	sum := lib.Summary
	if sum.Titles != 4 || sum.PlayedTitles != 0 || sum.NeverStarted != 0 || sum.AddedTitles != 2 || sum.AddedPlayed != 2 {
		t.Errorf("summary = %+v", sum)
	}
	if !labeledCountsEqual(lib.TimeToFirstPlay, map[string]int64{"day": 0, "week": 2, "month": 0, "later": 0, "not_yet": 0}) {
		t.Errorf("time to first play = %+v", lib.TimeToFirstPlay)
	}
	if len(lib.RecentlyAddedUnplayed) != 0 || len(lib.Genres) != 0 {
		t.Errorf("unplayed = %+v, genres = %+v", lib.RecentlyAddedUnplayed, lib.Genres)
	}
}

func assertBreakdownsDevicesDB(t *testing.T, h *AdminHandler, month AdminInsightsWindowQuery) {
	dev, err := h.ReadAdminInsightsDevices(t.Context(), AdminInsightsDevicesQuery{AdminInsightsWindowQuery: month}, false)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]AdminInsightsClientVersionRow{}
	for _, v := range dev.ClientVersions {
		versions[v.ClientName+" "+v.ClientVersion] = v
	}
	if len(versions) != 2 || versions["Silo Android 1.2.0"].Sessions != 2 || versions["Silo Android 1.2.0"].IsJellyfinClient ||
		versions["Findroid 0.15"].Sessions != 1 || !versions["Findroid 0.15"].IsJellyfinClient {
		t.Errorf("client versions = %+v", dev.ClientVersions)
	}
	var native, jellyfin int64
	for _, b := range dev.ProtocolBuckets {
		native += b.NativeSessions
		jellyfin += b.JellyfinSessions
	}
	if len(dev.ProtocolBuckets) != 3 || native != 2 || jellyfin != 1 {
		t.Errorf("protocol buckets = %+v", dev.ProtocolBuckets)
	}
	// The E1 row has no route facts, so it is out of both breakdowns.
	sub := dev.Subtitles
	if sub.Sessions != 2 || sub.SubtitledSessions != 1 || sub.HearingImpairedSessions != 1 || sub.ForcedSessions != 0 ||
		!labeledCountsEqual(sub.Languages, map[string]int64{"es": 1}) || !labeledCountsEqual(sub.Deliveries, map[string]int64{"embedded": 1}) {
		t.Errorf("subtitles = %+v", sub)
	}
	audio := dev.Audio
	if audio.Sessions != 2 || !labeledCountsEqual(audio.Languages, map[string]int64{"en": 1, "ja": 1}) ||
		!labeledCountsEqual(audio.Decisions, map[string]int64{"direct": 1, "transcode": 1}) ||
		!labeledCountsEqual(audio.DeliveredChannels, map[string]int64{"stereo": 2}) {
		t.Errorf("audio = %+v", audio)
	}
}

func assertBreakdownsLibraryCoverageDB(t *testing.T, h *AdminHandler, s breakdownsSeed, month AdminInsightsWindowQuery) {
	lib, err := h.ReadAdminInsightsLibrary(t.Context(), month, false)
	if err != nil {
		t.Fatal(err)
	}
	sum := lib.Summary
	if sum.Titles != 4 || sum.PlayedTitles != 3 || sum.NeverStarted != 1 || sum.AddedTitles != 2 || sum.AddedPlayed != 1 ||
		sum.MedianDaysToFirstPlay == nil || *sum.MedianDaysToFirstPlay != 1.5 {
		t.Errorf("summary = %+v (median %v)", sum, sum.MedianDaysToFirstPlay)
	}
	if len(lib.Libraries) != 2 || lib.Libraries[0].LibraryID != s.movies || lib.Libraries[0].Titles != 3 || lib.Libraries[0].PlayedTitles != 2 ||
		lib.Libraries[0].NeverStarted != 1 || lib.Libraries[0].AddedTitles != 2 || lib.Libraries[1].LibraryID != s.tv || lib.Libraries[1].PlayedTitles != 1 {
		t.Errorf("libraries = %+v", lib.Libraries)
	}
	if !labeledCountsEqual(lib.TimeToFirstPlay, map[string]int64{"day": 0, "week": 1, "month": 0, "later": 0, "not_yet": 1}) {
		t.Errorf("time to first play = %+v", lib.TimeToFirstPlay)
	}
	if len(lib.RecentlyAddedUnplayed) != 1 || lib.RecentlyAddedUnplayed[0].ContentID != "M3" || lib.RecentlyAddedUnplayed[0].Title != "Untitled" {
		t.Errorf("recently added unplayed = %+v", lib.RecentlyAddedUnplayed)
	}
}

func assertBreakdownsLibraryPlaysDB(t *testing.T, h *AdminHandler, s breakdownsSeed, month AdminInsightsWindowQuery) {
	ctx := t.Context()
	lib, err := h.ReadAdminInsightsLibrary(ctx, month, false)
	if err != nil {
		t.Fatal(err)
	}
	if !labeledPlaysEqual(lib.Genres, map[string]int64{"Drama": 2, "Thriller": 1, "Comedy": 1}) || lib.Genres[0].Key != "Drama" || lib.Genres[0].Titles != 2 {
		t.Errorf("genres = %+v", lib.Genres)
	}
	if len(lib.Decades) != 3 || lib.Decades[0].Key != "1990" || lib.Decades[1].Key != "2010" || lib.Decades[2].Key != "2020" {
		t.Errorf("decades = %+v", lib.Decades)
	}
	if !labeledPlaysEqual(lib.ContentRatings, map[string]int64{"R": 1, "PG-13": 1, "TV-MA": 1}) {
		t.Errorf("content ratings = %+v", lib.ContentRatings)
	}

	// The library filter lists one library and keeps only its plays in the
	// breakdowns: Dark's play is in TV.
	movies := month
	movies.LibraryID = s.movies
	lib, err = h.ReadAdminInsightsLibrary(ctx, movies, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Libraries) != 1 || lib.Summary.Titles != 3 || !labeledPlaysEqual(lib.Genres, map[string]int64{"Drama": 1, "Thriller": 1, "Comedy": 1}) {
		t.Errorf("movies only: libraries=%+v genres=%+v", lib.Libraries, lib.Genres)
	}
}

func labeledCountsEqual(got []AdminInsightsLabeledCount, want map[string]int64) bool {
	if len(got) != len(want) {
		return false
	}
	for _, c := range got {
		if v, ok := want[c.Key]; !ok || v != c.Sessions {
			return false
		}
	}
	return true
}

func labeledPlaysEqual(got []AdminInsightsLabeledPlays, want map[string]int64) bool {
	if len(got) != len(want) {
		return false
	}
	for _, c := range got {
		if v, ok := want[c.Key]; !ok || v != c.Plays {
			return false
		}
	}
	return true
}
