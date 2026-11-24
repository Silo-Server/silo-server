package handlers

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type playGroupingSeed struct {
	alice, bob, carol, dave, erin int
	libX, libY, libTV             int
	day                           time.Time // UTC midnight three days ago
}

// seedPlayGrouping writes session sequences whose play counts the viewing rule
// decides (see admin_insights_plays.go). T is noon on seed.day.
//
//	alice p-a  M1: 20 min unfinished, resumed 3 h later and completed (1 play), a
//	           rewatch after that (1 play); M2: an 8 s false start (no play);
//	           S1: E1 and E2 completed (2 plays).
//	bob   p-b  M1: a completed session replaced by a method switch 10 s later
//	           (1 play); the next day, 40 min unfinished (1 play).
//	erin  p-e  M2: a completed 2 h session with a 5 min session overlapping it
//	           (1 play), then 20 min after it ended (1 play). M1: a completed
//	           session replaced by a method switch, the switched-to session
//	           completing 3 min later (1 play), and a restart a minute after
//	           that, before the replaced session's hand-off grace ends (1 play).
//	dave  p-d  M1: a 40 s transcode from library X resumed and completed by
//	           direct play from library Y (1 direct play, belonging to X).
//	carol p-c  M2: a 2 h unfinished session ending 23 h before day starts,
//	           resumed and completed 5 min into day (1 play, started before day).
func seedPlayGrouping(t *testing.T, pool *pgxpool.Pool) playGroupingSeed {
	t.Helper()
	ctx := t.Context()
	var s playGroupingSeed
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
	for name, dest := range map[string]*int{"alice": &s.alice, "bob": &s.bob, "carol": &s.carol, "dave": &s.dave, "erin": &s.erin} {
		row(dest, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, name)
	}
	row(&s.libX, `INSERT INTO media_folders (type, name) VALUES ('movies', 'Movies') RETURNING id`)
	row(&s.libY, `INSERT INTO media_folders (type, name) VALUES ('movies', 'Movies 4K') RETURNING id`)
	row(&s.libTV, `INSERT INTO media_folders (type, name) VALUES ('tv', 'TV') RETURNING id`)
	exec(`INSERT INTO media_items (content_id, type, title, year) VALUES ('M1', 'movie', 'Arrival', 2016), ('M2', 'movie', 'Heat', 1995), ('S1', 'series', 'Andor', 2022)`)
	exec(`INSERT INTO episodes (content_id, series_id, title, season_number, episode_number) VALUES ('E1', 'S1', 'Kassa', 1, 1), ('E2', 'S1', 'That Would Be Me', 1, 2)`)
	var m1x, m1y, m2, e1, e2 int
	row(&m1x, `INSERT INTO media_files (media_folder_id, file_path, content_id, resolution) VALUES ($1, '/x/arrival.mkv', 'M1', '1080p') RETURNING id`, s.libX)
	row(&m1y, `INSERT INTO media_files (media_folder_id, file_path, content_id, resolution) VALUES ($1, '/y/arrival.mkv', 'M1', '2160p') RETURNING id`, s.libY)
	row(&m2, `INSERT INTO media_files (media_folder_id, file_path, content_id, resolution) VALUES ($1, '/x/heat.mkv', 'M2', '1080p') RETURNING id`, s.libX)
	row(&e1, `INSERT INTO media_files (media_folder_id, file_path, episode_id, resolution) VALUES ($1, '/tv/e1.mkv', 'E1', '1080p') RETURNING id`, s.libTV)
	row(&e2, `INSERT INTO media_files (media_folder_id, file_path, episode_id, resolution) VALUES ($1, '/tv/e2.mkv', 'E2', '1080p') RETURNING id`, s.libTV)

	s.day = time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3)
	at := s.day.Add(12 * time.Hour)
	sessionVia := func(method string, user int, profile, item string, file int, start time.Time, dur time.Duration, played float64, completed bool, endReason string) {
		t.Helper()
		var reason any
		if endReason != "" {
			reason = endReason
		}
		exec(`INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at,
			 watched_seconds, played_seconds, measured, completed, end_reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, TRUE, $10, $11)`,
			"grouping-"+uuid.NewString(),
			user, profile, item, file, method, start, start.Add(dur), played, completed, reason)
	}
	session := func(user int, profile, item string, file int, start time.Time, dur time.Duration, played float64, completed bool, endReason string) {
		t.Helper()
		sessionVia("direct", user, profile, item, file, start, dur, played, completed, endReason)
	}
	session(s.alice, "p-a", "M1", m1x, at, 20*time.Minute, 1200, false, "")
	session(s.alice, "p-a", "M1", m1x, at.Add(3*time.Hour), 100*time.Minute, 6000, true, "")
	session(s.alice, "p-a", "M1", m1x, at.Add(5*time.Hour), 10*time.Minute, 600, false, "")
	session(s.alice, "p-a", "M2", m2, at.Add(time.Hour), 8*time.Second, 8, false, "")
	session(s.alice, "p-a", "E1", e1, at.Add(10*time.Hour), 40*time.Minute, 2400, true, "")
	session(s.alice, "p-a", "E2", e2, at.Add(11*time.Hour), 40*time.Minute, 2400, true, "")

	session(s.bob, "p-b", "M1", m1x, at, 110*time.Minute, 6500, true, "replaced")
	session(s.bob, "p-b", "M1", m1x, at.Add(110*time.Minute+10*time.Second), 10*time.Minute, 600, true, "")
	session(s.bob, "p-b", "M1", m1x, at.Add(30*time.Hour), 40*time.Minute, 2400, false, "")

	session(s.erin, "p-e", "M2", m2, at, 2*time.Hour, 7000, true, "")
	session(s.erin, "p-e", "M2", m2, at.Add(5*time.Minute), 5*time.Minute, 300, false, "")
	session(s.erin, "p-e", "M2", m2, at.Add(3*time.Hour), 20*time.Minute, 1200, false, "")
	switched := at.Add(6*time.Hour + 110*time.Minute)
	session(s.erin, "p-e", "M1", m1x, at.Add(6*time.Hour), 110*time.Minute, 6500, true, "replaced")
	session(s.erin, "p-e", "M1", m1x, switched.Add(10*time.Second), 3*time.Minute, 180, true, "")
	session(s.erin, "p-e", "M1", m1x, switched.Add(4*time.Minute+10*time.Second), 20*time.Minute, 1200, false, "")

	sessionVia("transcode", s.dave, "p-d", "M1", m1x, at, 40*time.Second, 40, false, "")
	session(s.dave, "p-d", "M1", m1y, at.Add(2*time.Hour), 2*time.Hour, 7000, true, "")

	session(s.carol, "p-c", "M2", m2, s.day.Add(-25*time.Hour), 2*time.Hour, 3600, false, "")
	session(s.carol, "p-c", "M2", m2, s.day.Add(5*time.Minute), 20*time.Minute, 1200, true, "")
	return s
}

func TestAdminInsightsPlayGroupingDB(t *testing.T) {
	pool := insightsTestPool(t)
	s := seedPlayGrouping(t, pool)
	h := &AdminHandler{pool: pool}
	t.Run("viewings", func(t *testing.T) { assertPlayGroupingViewingsDB(t, h, s) })
	t.Run("look-back keys on ended_at", func(t *testing.T) { assertPlayGroupingLookBackDB(t, h, s) })
	t.Run("library filter applies after grouping", func(t *testing.T) { assertPlayGroupingLibraryDB(t, h, s) })
	t.Run("profile filter stays within its account", func(t *testing.T) { assertPlayGroupingProfileFilterDB(t, h, s) })
	t.Run("titles", func(t *testing.T) { assertPlayGroupingTitlesDB(t, h, s) })
	t.Run("title detail", func(t *testing.T) { assertPlayGroupingTitleDetailDB(t, h, s) })
}

var playGroupingWeek = AdminInsightsWindowQuery{Range: InsightsRange7d, TZ: "UTC"}

func playGroupingActivity(t *testing.T, h *AdminHandler, q AdminInsightsWindowQuery) AdminInsightsActivitySummary {
	t.Helper()
	act, err := h.ReadAdminInsightsActivity(t.Context(), q, false)
	if err != nil {
		t.Fatal(err)
	}
	return act.Summary
}

func playGroupingWeekFor(user int) AdminInsightsWindowQuery {
	q := playGroupingWeek
	q.UserID = user
	return q
}

func assertPlayGroupingViewingsDB(t *testing.T, h *AdminHandler, s playGroupingSeed) {
	for _, tc := range []struct {
		name                      string
		user                      int
		plays, sessions, complete int64
	}{
		{"resume, rewatch, false start, episodes", s.alice, 4, 6, 3},
		{"method switch after the threshold, next-day resume", s.bob, 2, 3, 1},
		{"overlapping sessions merge; a session after any completion is new", s.erin, 4, 6, 2},
		{"viewing across libraries", s.dave, 1, 2, 1},
		{"viewing that started inside the window", s.carol, 1, 2, 1},
	} {
		sum := playGroupingActivity(t, h, playGroupingWeekFor(tc.user))
		if sum.Plays != tc.plays || sum.Sessions != tc.sessions || sum.CompletedPlays != tc.complete {
			t.Errorf("%s: plays=%d sessions=%d completed=%d, want %d %d %d", tc.name, sum.Plays, sum.Sessions, sum.CompletedPlays, tc.plays, tc.sessions, tc.complete)
		}
	}
	// A play's method is its longest session's: dave's short transcode was
	// finished by direct play.
	if sum := playGroupingActivity(t, h, playGroupingWeekFor(s.dave)); sum.DirectPlays != 1 || sum.TranscodePlays != 0 {
		t.Errorf("dave play method: direct=%d transcode=%d, want 1 0", sum.DirectPlays, sum.TranscodePlays)
	}
}

// Carol's first session started 25 h before the day but ended 23 h before it,
// so her in-day session continues it: no play starts in the day, yet its
// watch time counts.
func assertPlayGroupingLookBackDB(t *testing.T, h *AdminHandler, s playGroupingSeed) {
	day := s.day.Format("2006-01-02")
	sum := playGroupingActivity(t, h, AdminInsightsWindowQuery{Range: InsightsRangeCustom, FromDate: day, ToDate: day, TZ: "UTC", UserID: s.carol})
	if sum.Plays != 0 || sum.Sessions != 1 || sum.WatchSeconds != 1200 {
		t.Fatalf("carol on day: %+v, want 0 plays, 1 session, 1200 s", sum)
	}
}

// Dave's play started in library X; library Y holds its later sitting.
func assertPlayGroupingLibraryDB(t *testing.T, h *AdminHandler, s playGroupingSeed) {
	inX, inY := playGroupingWeekFor(s.dave), playGroupingWeekFor(s.dave)
	inX.LibraryID, inY.LibraryID = s.libX, s.libY
	x, y := playGroupingActivity(t, h, inX), playGroupingActivity(t, h, inY)
	if x.Plays != 1 || x.Sessions != 1 || y.Plays != 0 || y.Sessions != 1 || y.WatchSeconds != 7000 {
		t.Fatalf("dave by library: X=%+v Y=%+v", x, y)
	}
}

// A profile filter applies within its account: bob's profile id under
// alice's account matches nothing.
func assertPlayGroupingProfileFilterDB(t *testing.T, h *AdminHandler, s playGroupingSeed) {
	q := playGroupingWeekFor(s.alice)
	q.ProfileID = "p-b"
	if sum := playGroupingActivity(t, h, q); sum.Sessions != 0 {
		t.Fatalf("another account's profile id matched: %+v", sum)
	}
}

func assertPlayGroupingTitlesDB(t *testing.T, h *AdminHandler, s playGroupingSeed) {
	titles, err := h.ReadAdminInsightsTitles(t.Context(), AdminInsightsTitlesQuery{AdminInsightsWindowQuery: playGroupingWeek, Sort: InsightsTitlesSortViewers}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(titles.Movies) != 2 || titles.Movies[0].ContentID != "M1" || titles.Movies[0].Plays != 7 || titles.Movies[0].Viewers != 4 ||
		titles.Movies[0].CompletedPlays != 4 || titles.Movies[1].ContentID != "M2" || titles.Movies[1].Plays != 3 || titles.Movies[1].Viewers != 2 {
		t.Fatalf("movies: %+v", titles.Movies)
	}
	if len(titles.Shows) != 1 || titles.Shows[0].ContentID != "S1" || titles.Shows[0].Plays != 2 || titles.Shows[0].Episodes != 2 || titles.Shows[0].Viewers != 1 {
		t.Fatalf("shows: %+v", titles.Shows)
	}
	// Dave's play belongs to library X, where it started; Y has watch time
	// but no play, so it does not rank.
	libs := map[int]int64{}
	for _, lib := range titles.Libraries {
		libs[lib.LibraryID] = lib.Plays
	}
	if len(libs) != 2 || libs[s.libX] != 10 || libs[s.libTV] != 2 {
		t.Fatalf("libraries: %+v", titles.Libraries)
	}
}

func assertPlayGroupingTitleDetailDB(t *testing.T, h *AdminHandler, s playGroupingSeed) {
	ctx := t.Context()
	series, err := h.ReadAdminInsightsTitle(ctx, AdminInsightsTitleQuery{AdminInsightsWindowQuery: playGroupingWeek, ContentID: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	if series.Kind != itemTypeSeries || series.Summary.Plays != 2 || len(series.Episodes) != 2 || series.Episodes[0].ContentID != "E1" ||
		len(series.Viewers) != 1 || series.Viewers[0].LastEpisode == nil || series.Viewers[0].LastEpisode.ContentID != "E2" {
		t.Fatalf("series detail: %+v", series)
	}
	alice := playGroupingWeekFor(s.alice)
	alice.ProfileID = "p-a"
	movie, err := h.ReadAdminInsightsTitle(ctx, AdminInsightsTitleQuery{AdminInsightsWindowQuery: alice, ContentID: "M1"})
	if err != nil {
		t.Fatal(err)
	}
	if movie.Kind != itemTypeMovie || movie.Summary.Plays != 2 || movie.Summary.Sessions != 3 || len(movie.Viewers) != 1 ||
		movie.Viewers[0].FurthestPositionSeconds == nil || *movie.Viewers[0].FurthestPositionSeconds != 6000 {
		t.Fatalf("movie detail: %+v", movie)
	}
	if _, err := h.ReadAdminInsightsTitle(ctx, AdminInsightsTitleQuery{AdminInsightsWindowQuery: playGroupingWeek, ContentID: "E1"}); !errors.Is(err, ErrInvalidInsightsQuery) {
		t.Fatalf("episode id: err = %v", err)
	}
	if _, err := h.ReadAdminInsightsTitle(ctx, AdminInsightsTitleQuery{AdminInsightsWindowQuery: playGroupingWeek, ContentID: "nope"}); !errors.Is(err, ErrInsightsTitleNotFound) {
		t.Fatalf("unknown id: err = %v", err)
	}
}

// TestPlaybackHistoryEntrySkipsTheReadGateDB pins that the session lookup the
// playback stop path makes never queues behind Insights reads, while a history
// page still waits for a slot.
func TestPlaybackHistoryEntrySkipsTheReadGateDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := &AdminHandler{pool: pool}
	for range adminHistoryMaxConcurrentReads {
		adminHistoryReadSlots <- struct{}{}
	}
	defer func() {
		for range adminHistoryMaxConcurrentReads {
			<-adminHistoryReadSlots
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := h.ReadAdminPlaybackHistoryEntry(ctx, "missing-"+uuid.NewString()); err != nil {
		t.Fatalf("entry lookup with every read slot taken: %v", err)
	}
	waiting, cancelWaiting := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelWaiting()
	if _, err := h.ListAdminPlaybackHistoryPage(waiting, AdminPlaybackHistoryFilter{}, nil, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("history page with every read slot taken = %v, want it to wait out its deadline", err)
	}
}
