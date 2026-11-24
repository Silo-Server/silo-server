package handlers

import (
	"fmt"
	"strings"
)

// Insights counts plays, not sessions. Every admin_playback_history row is one
// playback session; a person who stops and resumes a movie writes several, a
// Jellyfin client that switches play method mid-play writes one per method,
// and a false start writes one too. Insights groups those rows into viewings
// at read time and counts a viewing as a play when it is worth counting.
//
// Viewing. Sessions of one profile (user_id, profile_id) on one item
// (media_item_id, a movie or an episode), ordered by started_at, ended_at,
// session_id. Session S starts a new viewing when:
//
//   - S or the item is unknown (empty media_item_id), or S is the first
//     session of that profile on that item in the scope;
//   - S started more than 24 hours after every earlier session ended; or
//   - the previous session completed before S started, or an earlier session
//     completed between the previous session's start and S's start. A
//     completed session marks its ended_at as that boundary, or ended_at plus
//     five minutes when its end reason is "replaced": a Jellyfin method switch
//     after the watched threshold hands the same play to the next session.
//
// Otherwise S continues the viewing in progress. Overlapping sessions (two
// devices on one profile) therefore merge, and a rewatch after a completed
// viewing starts a new one.
//
// The boundary check reads the previous session's own boundary and the latest
// boundary of every earlier session; a completion hidden behind both (three
// or more completed sessions overlapping) is not seen.
//
// Play. A viewing that completed, or whose sessions add up to at least
// insightsMinPlaySeconds of watch time (adminPlaybackWatchTimeSQL: measured
// time when recorded, otherwise the capped end-position estimate, which can
// include pauses on rows from before measurement). A play takes its start time
// (window, buckets, heatmap) and library from its first session and its play
// method from the session with the most watch time. Watch time is summed over
// every session that started in the window, including the later sittings of a
// viewing that began just before it.
//
// These are Silo's rules. Tautulli applies a per-session minimum and groups by
// view offset instead; the counts are comparable, not identical.
//
// Why at read time: a session's row is upserted by every finalization of it,
// from any replica and in any order, and a session resumed after expiry
// rewrites its row (see PlaybackAdminStore). Deriving viewings from the rows as
// they stand has no ordering hazard, and a late or recovered finalization
// simply shows up on the next read. Counts are current facts attributed by
// session start, so a past window can change when a late row lands.
//
// Look-back. Whether an in-window session starts a viewing depends only on
// earlier sessions that ended at most 24 hours before it, so the scope reads
// rows that ended at or after from-24h and started before to (ended_at never
// precedes started_at), and only in_window rows are counted.
const (
	insightsMinPlaySeconds       = 30
	insightsViewingGap           = "24 hours"
	insightsReplacedHandoffGrace = "5 minutes"
	// insightsReadWorkMem lets a play-counting read sort the range's sessions
	// in memory. The viewing grouping sorts every session in scope, which
	// spills to disk at the default work_mem on year-long ranges.
	insightsReadWorkMem = "64MB"
)

// insightsPlaysScope is the row filter of a play-counting read. Account,
// profile and item filters apply before grouping, which keeps every session of
// a viewing in scope. The library filter applies after grouping, so a viewing
// whose sittings used files from two libraries is still one play.
type insightsPlaysScope struct {
	win       resolvedInsightsWindow
	userID    int
	profileID string
	libraryID int
	itemIDs   []string // nil: every item
}

func newInsightsPlaysScope(win resolvedInsightsWindow, q AdminInsightsWindowQuery) insightsPlaysScope {
	return insightsPlaysScope{win: win, userID: q.UserID, profileID: strings.TrimSpace(q.ProfileID), libraryID: q.LibraryID}
}

// insightsViewingCTE is the WITH clause viewingRowsSQL fills in: %[1]s the
// watch-time expression, %[2]s in_window, %[3]s the scope conditions, %[4]s the
// replaced hand-off grace, %[5]s the viewing gap, %[6]s..%[8]s the direct,
// remux and transcode predicates over q.play_method.
const insightsViewingCTE = `
		WITH scoped AS (
			SELECT
				h.session_id, h.user_id, h.profile_id, h.media_item_id, h.started_at, h.ended_at,
				h.completed, h.end_reason, h.play_method, h.measured, h.played_seconds, h.watched_seconds,
				h.route_facts_recorded,
				(%[1]s)::double precision AS watch_sec,
				(%[2]s) AS in_window,
				ep.series_id, (ep.content_id IS NOT NULL) AS is_episode, mi.type AS item_type,
				mf.id AS file_id, mf.media_folder_id AS file_library_id
			FROM admin_playback_history h
			LEFT JOIN media_items mi ON mi.content_id = h.media_item_id
			LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
			LEFT JOIN media_files mf ON mf.id = COALESCE(h.effective_media_file_id, h.media_file_id)
			WHERE %[3]s
		),
		running AS (
			-- Running maxima up to and including each session; the next step
			-- reads the previous session's values with LAG. (A frame ending at
			-- 1 PRECEDING re-aggregates every row and is far slower.)
			SELECT b.*,
				MAX(b.ended_at) OVER w AS run_max_ended_at,
				MAX(b.boundary_at) OVER w AS run_boundary_at
			FROM (
				SELECT s.*, CASE WHEN s.completed THEN s.ended_at
					+ CASE WHEN s.end_reason = 'replaced' THEN interval '%[4]s' ELSE interval '0' END END AS boundary_at
				FROM scoped s
			) b
			WINDOW w AS (PARTITION BY b.user_id, b.profile_id, b.media_item_id
				ORDER BY b.started_at, b.ended_at, b.session_id ROWS UNBOUNDED PRECEDING)
		),
		marked AS (
			SELECT r.*,
				LAG(r.started_at) OVER w AS prev_started_at,
				LAG(r.run_max_ended_at) OVER w AS prev_max_ended_at,
				LAG(r.boundary_at) OVER w AS prev_own_boundary_at,
				LAG(r.run_boundary_at) OVER w AS prev_boundary_at
			FROM running r
			WINDOW w AS (PARTITION BY r.user_id, r.profile_id, r.media_item_id ORDER BY r.started_at, r.ended_at, r.session_id)
		),
		flagged AS (
			-- The previous session's own completion counts whenever it is at or
			-- before S's start; an earlier session's (the running maximum) only
			-- when it falls after the previous session started, since one before
			-- that already split an earlier session.
			SELECT m.*,
				(m.media_item_id = '' OR m.prev_started_at IS NULL
					OR m.started_at > m.prev_max_ended_at + interval '%[5]s'
					OR m.prev_own_boundary_at <= m.started_at
					OR (m.prev_boundary_at > m.prev_started_at AND m.prev_boundary_at <= m.started_at)) AS starts_viewing
			FROM marked m
		),
		sequenced AS (
			SELECT f.*,
				COUNT(*) FILTER (WHERE f.starts_viewing) OVER (
					PARTITION BY f.user_id, f.profile_id, f.media_item_id
					ORDER BY f.started_at, f.ended_at, f.session_id ROWS UNBOUNDED PRECEDING) AS viewing_no
			FROM flagged f
		),
		viewings AS (
			-- longest is the viewing's largest [watch_sec, -start epoch, method
			-- code]: the longest session, the earliest on a tie. Folding the
			-- method into an array MAX keeps it in this window pass instead of
			-- a separately sorted FIRST_VALUE.
			SELECT q.*,
				BOOL_OR(q.completed) OVER v AS viewing_completed,
				SUM(q.watch_sec) OVER v AS viewing_watch_sec,
				MAX(ARRAY[q.watch_sec, -EXTRACT(EPOCH FROM q.started_at)::double precision,
					CASE WHEN %[6]s THEN 1 WHEN %[7]s THEN 2 WHEN %[8]s THEN 3 ELSE 0 END]) OVER v AS longest
			FROM sequenced q
			WINDOW v AS (PARTITION BY q.user_id, q.profile_id, q.media_item_id, q.viewing_no)
		),
		vr AS (
			SELECT w.*,
				CASE w.longest[3] WHEN 1 THEN 'direct' WHEN 2 THEN 'remux' WHEN 3 THEN 'transcode' ELSE '' END AS viewing_play_method
			FROM viewings w
		)`

// viewingRowsSQL returns a WITH clause ending in the CTE "vr", one row per
// session in scope (look-back rows included), and the predicate that keeps
// the counted rows of vr: started in the window and, when filtered, in the
// library. Its parameters are appended to args.
//
// vr carries the session columns used by Insights plus:
//
//	watch_sec            the session's watch time (adminPlaybackWatchTimeSQL)
//	in_window            started inside the window
//	series_id, is_episode, item_type, file_id, file_library_id
//	starts_viewing       first session of its viewing
//	viewing_completed    any session of the viewing completed
//	viewing_watch_sec    watch time of the viewing's sessions in scope
//	viewing_play_method  play method of the viewing's longest session
//
// insightsIsPlaySQL("vr") is true on the one row that counts a play.
func (s insightsPlaysScope) viewingRowsSQL(args *[]any) (with, counted string) {
	param := func(v any) string {
		*args = append(*args, v)
		return fmt.Sprintf("$%d", len(*args))
	}
	conds := []string{"h.started_at < " + param(s.win.To)}
	inWindow := "TRUE"
	if !s.win.AllTime {
		from := param(s.win.From)
		conds = append(conds, fmt.Sprintf("h.ended_at >= %s::timestamptz - interval '%s'", from, insightsViewingGap))
		inWindow = "h.started_at >= " + from
	}
	if s.userID > 0 {
		conds = append(conds, "h.user_id = "+param(s.userID))
	}
	if s.profileID != "" {
		conds = append(conds, "h.profile_id = "+param(s.profileID))
	}
	if s.itemIDs != nil {
		conds = append(conds, "h.media_item_id = ANY("+param(s.itemIDs)+"::text[])")
	}

	with = fmt.Sprintf(insightsViewingCTE,
		adminPlaybackWatchTimeSQL("h"), inWindow, strings.Join(conds, " AND "),
		insightsReplacedHandoffGrace, insightsViewingGap,
		insightsDirectSQL("q.play_method"), insightsRemuxSQL("q.play_method"), insightsTranscodeSQL("q.play_method"))

	counted = "vr.in_window"
	if s.libraryID > 0 {
		counted += " AND " + insightsLibraryOfRowSQL("vr") + " = " + param(s.libraryID)
	}
	return with, counted
}

// insightsIsPlaySQL is true on the first session of a viewing that counts as
// a play.
func insightsIsPlaySQL(alias string) string {
	return fmt.Sprintf("(%[1]s.starts_viewing AND (%[1]s.viewing_completed OR %[1]s.viewing_watch_sec >= %[2]d))", alias, insightsMinPlaySeconds)
}

// insightsLibraryAttributionSQL is the one library a session belongs to: its
// file's, or, for a session whose file is gone, its title's library when the
// title belongs to exactly one. A removed file whose title sits in several
// libraries belongs to none rather than to each. Insights uses it for both the
// library filter and the library leaderboard, so they agree.
func insightsLibraryAttributionSQL(fileLibraryCol, fileIDCol, titleIDExpr string) string {
	return fmt.Sprintf(`COALESCE(%[1]s, CASE WHEN %[2]s IS NULL THEN (
		SELECT MIN(mil.media_folder_id) FROM media_item_libraries mil
		WHERE mil.content_id = %[3]s
		HAVING COUNT(*) = 1) END)`, fileLibraryCol, fileIDCol, titleIDExpr)
}

// insightsLibraryOfRowSQL attributes a vr row (insightsLibraryAttributionSQL).
func insightsLibraryOfRowSQL(alias string) string {
	return insightsLibraryAttributionSQL(alias+".file_library_id", alias+".file_id",
		"COALESCE("+alias+".series_id, "+alias+".media_item_id)")
}

// Play method predicates over a play_method column.
func insightsDirectSQL(col string) string    { return col + " IN ('direct', 'direct_play')" }
func insightsRemuxSQL(col string) string     { return col + " IN ('remux', 'direct_stream')" }
func insightsTranscodeSQL(col string) string { return col + " = 'transcode'" }
