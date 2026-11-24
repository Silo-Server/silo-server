package handlers

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func queryAdminInsightsPeopleSnapshot(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, insightsReadWorkMem)
	if err != nil {
		return nil, fmt.Errorf("beginning insights people read: %w", err)
	}
	defer done()

	oldestHist, oldestMeas, oldestRoute, oldestMetric, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, oldestHist)
	// People covers every account; it takes no account or profile filter.
	scope := newInsightsPlaysScope(win, AdminInsightsWindowQuery{LibraryID: q.LibraryID})
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, 0, "")

	out := &AdminInsightsPeople{
		From: win.From, To: win.To, TZ: win.TZ,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Accounts: make([]AdminInsightsPersonAccount, 0),
	}

	liveUsers, liveProfiles, err := queryInsightsLiveUsers(ctx, tx, q.LibraryID)
	if err != nil {
		return nil, err
	}
	out.LiveAccounts = int64(len(liveUsers))
	byUser, err := queryInsightsPeopleProfiles(ctx, tx, scope, liveUsers, liveProfiles, out)
	if err != nil {
		return nil, err
	}
	if err := queryInsightsPeopleTopClients(ctx, tx, q.LibraryID, whereSQL, whereArgs, byUser); err != nil {
		return nil, err
	}

	allAccounts := finalizeInsightsPeopleAccounts(byUser, q.Search, out)
	sortInsightsAccounts(allAccounts, q.Sort, q.Dir)
	out.Accounts = allAccounts
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights people read: %w", err)
	}
	return out, nil
}

func queryAdminInsightsPeople(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	full, err := queryAdminInsightsPeopleSnapshot(ctx, pool, q)
	if err != nil {
		return nil, err
	}
	return sliceAdminInsightsPeople(full, q.Limit, q.Offset), nil
}

func sliceAdminInsightsPeople(full *AdminInsightsPeople, rawLimit, rawOffset int) *AdminInsightsPeople {
	if full == nil {
		return nil
	}
	cp := *full
	limit, offset := clampInsightsPeopleLimit(rawLimit), max(rawOffset, 0)
	if offset >= len(full.Accounts) {
		cp.Accounts = make([]AdminInsightsPersonAccount, 0)
	} else {
		end := min(offset+limit, len(full.Accounts))
		cp.Accounts = append([]AdminInsightsPersonAccount(nil), full.Accounts[offset:end]...)
	}
	return &cp
}

func queryInsightsLiveUsers(ctx context.Context, tx pgx.Tx, libraryID int) (map[int]bool, map[string]bool, error) {
	liveUsers := make(map[int]bool)
	liveProfiles := make(map[string]bool)
	query := `SELECT DISTINCT s.user_id, COALESCE(s.profile_id, '') FROM playback_sessions_sync s`
	var args []any
	if libraryID > 0 {
		query = `
			SELECT DISTINCT s.user_id, COALESCE(s.profile_id, '')
			FROM playback_sessions_sync s
			LEFT JOIN media_files mf ON mf.id = s.media_file_id
			LEFT JOIN episodes ep ON ep.content_id = mf.episode_id
			WHERE mf.media_folder_id = $1
			   OR (mf.id IS NOT NULL AND EXISTS (
					SELECT 1 FROM media_item_libraries mil
					WHERE mil.content_id = COALESCE(ep.series_id, mf.content_id)
					  AND mil.media_folder_id = $1
			   ))`
		args = append(args, libraryID)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("querying live playback accounts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid int
		var pid string
		if err := rows.Scan(&uid, &pid); err != nil {
			return nil, nil, err
		}
		liveUsers[uid] = true
		liveProfiles[strconv.Itoa(uid)+":"+pid] = true
	}
	return liveUsers, liveProfiles, rows.Err()
}

// insightsPeopleProfilesSQL groups the window's sessions by profile. A
// profile appears when it has any session in the window; its play counts may
// be zero when it only continued a viewing begun before the window.
func insightsPeopleProfilesSQL(with, counted string) string {
	return with + `,
		counted AS (
			SELECT vr.*, ` + insightsIsPlaySQL("vr") + ` AS is_play FROM vr WHERE ` + counted + `
		),
		grouped AS (
			SELECT
				user_id, profile_id,
				COUNT(*) FILTER (WHERE is_play)::bigint AS plays,
				COUNT(*)::bigint AS sessions,
				COUNT(*) FILTER (WHERE is_play AND NOT is_episode AND item_type = 'movie')::bigint AS movies,
				COUNT(*) FILTER (WHERE is_play AND is_episode)::bigint AS episodes,
				COALESCE(SUM(watch_sec), 0)::double precision AS watch_seconds,
				COUNT(*) FILTER (WHERE measured)::bigint AS measured_sessions,
				COUNT(*) FILTER (WHERE route_facts_recorded)::bigint AS route_facts_sessions,
				COUNT(*) FILTER (WHERE is_play AND ` + insightsDirectSQL("viewing_play_method") + `)::bigint AS direct_plays,
				COUNT(*) FILTER (WHERE is_play AND ` + insightsRemuxSQL("viewing_play_method") + `)::bigint AS remux_plays,
				COUNT(*) FILTER (WHERE is_play AND ` + insightsTranscodeSQL("viewing_play_method") + `)::bigint AS transcode_plays,
				COUNT(*) FILTER (WHERE is_play AND viewing_completed)::bigint AS completed_plays,
				MAX(ended_at) AS last_seen_at
			FROM counted
			GROUP BY user_id, profile_id
		)
		SELECT
			g.user_id, COALESCE(u.username, ''), COALESCE(u.role, ''),
			g.profile_id, COALESCE(pn.profile_name, g.profile_id),
			g.plays, g.sessions, g.movies, g.episodes, g.watch_seconds,
			g.measured_sessions, g.route_facts_sessions,
			g.direct_plays, g.remux_plays, g.transcode_plays, g.completed_plays,
			g.last_seen_at
		FROM grouped g
		LEFT JOIN users u ON u.id = g.user_id
		LEFT JOIN LATERAL (` + insightsProfileNameSQL("g") + `) pn ON TRUE
	`
}

// insightsProfileNameSQL reads a profile's latest recorded display name from
// history: profile names live in the per-user stores, not in Postgres.
func insightsProfileNameSQL(alias string) string {
	return `SELECT p.profile_name
		FROM admin_playback_history p
		WHERE p.user_id = ` + alias + `.user_id AND p.profile_id = ` + alias + `.profile_id AND p.profile_name <> ''
		ORDER BY p.ended_at DESC
		LIMIT 1`
}

func queryInsightsPeopleProfiles(
	ctx context.Context,
	tx pgx.Tx,
	scope insightsPlaysScope,
	liveUsers map[int]bool,
	liveProfiles map[string]bool,
	out *AdminInsightsPeople,
) (map[int]*AdminInsightsPersonAccount, error) {
	var args []any
	with, counted := scope.viewingRowsSQL(&args)
	rows, err := tx.Query(ctx, insightsPeopleProfilesSQL(with, counted), args...)
	if err != nil {
		return nil, fmt.Errorf("querying insights people: %w", err)
	}
	defer rows.Close()

	byUser := make(map[int]*AdminInsightsPersonAccount)
	for rows.Next() {
		var uid int
		var username, role, profileID, profileName string
		var plays, sessions, movies, episodes, measured, routeFacts, direct, remux, transcode, completed int64
		var watchSecF float64
		var lastSeen *time.Time
		if err := rows.Scan(
			&uid, &username, &role, &profileID, &profileName,
			&plays, &sessions, &movies, &episodes, &watchSecF, &measured, &routeFacts,
			&direct, &remux, &transcode, &completed, &lastSeen,
		); err != nil {
			return nil, fmt.Errorf("scanning insights person profile: %w", err)
		}
		lastSeen = normalizeTimePtrUTC(lastSeen)
		watchSec := int64(math.Round(watchSecF))

		out.Coverage.TotalSessions += sessions
		out.Coverage.MeasuredSessions += measured
		out.Coverage.RouteFactsSessions += routeFacts
		out.ActiveProfiles++
		out.TotalWatchSeconds += watchSec

		acc, ok := byUser[uid]
		if !ok {
			if username == "" {
				username = fmt.Sprintf("User %d", uid)
			}
			acc = &AdminInsightsPersonAccount{
				UserID: uid, Username: username, Role: role, IsLive: liveUsers[uid],
				Profiles: make([]AdminInsightsPersonProfile, 0, 2),
			}
			byUser[uid] = acc
		}
		acc.Plays += plays
		acc.Sessions += sessions
		acc.Movies += movies
		acc.Episodes += episodes
		acc.WatchSeconds += watchSec
		acc.MeasuredSessions += measured
		acc.DirectPlays += direct
		acc.RemuxPlays += remux
		acc.TranscodePlays += transcode
		acc.CompletedPlays += completed
		if lastSeen != nil && (acc.LastSeenAt == nil || lastSeen.After(*acc.LastSeenAt)) {
			acc.LastSeenAt = lastSeen
		}
		acc.Profiles = append(acc.Profiles, AdminInsightsPersonProfile{
			ProfileID: profileID, ProfileName: profileName, IsLive: liveProfiles[strconv.Itoa(uid)+":"+profileID],
			Plays: plays, Sessions: sessions, WatchSeconds: watchSec, MeasuredSessions: measured,
			DirectPlays: direct, RemuxPlays: remux, TranscodePlays: transcode,
			CompletedPlays: completed, CompletionRate: roundInsightsRate(completed, plays), LastSeenAt: lastSeen,
		})
	}
	return byUser, rows.Err()
}

func queryInsightsPeopleTopClients(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, byUser map[int]*AdminInsightsPersonAccount) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (h.user_id)
			h.user_id, BTRIM(h.client_name) AS client_name
		FROM admin_playback_history h
		`+insightsLibraryJoinSQL(libraryID)+`
		WHERE `+whereSQL+`
		  AND NULLIF(BTRIM(COALESCE(h.client_name, '')), '') IS NOT NULL
		GROUP BY h.user_id, BTRIM(h.client_name)
		ORDER BY h.user_id, COUNT(*) DESC, BTRIM(h.client_name) ASC
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights top clients per account: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid int
		var clientName string
		if err := rows.Scan(&uid, &clientName); err != nil {
			return err
		}
		if acc, ok := byUser[uid]; ok && clientName != "" {
			c := clientName
			acc.TopClient = &c
		}
	}
	return rows.Err()
}

func accountMatchesSearch(acc *AdminInsightsPersonAccount, searchLower string) bool {
	if searchLower == "" {
		return true
	}
	if strings.Contains(strings.ToLower(acc.Username), searchLower) {
		return true
	}
	for _, p := range acc.Profiles {
		if strings.Contains(strings.ToLower(p.ProfileName), searchLower) {
			return true
		}
	}
	return false
}

func finalizeInsightsPeopleAccounts(byUser map[int]*AdminInsightsPersonAccount, search string, out *AdminInsightsPeople) []AdminInsightsPersonAccount {
	searchLower := strings.ToLower(strings.TrimSpace(search))
	allAccounts := make([]AdminInsightsPersonAccount, 0, len(byUser))
	for _, acc := range byUser {
		acc.MeasuredShare = roundInsightsRate(acc.MeasuredSessions, acc.Sessions)
		acc.DirectRate = roundInsightsRate(acc.DirectPlays, acc.Plays)
		acc.CompletionRate = roundInsightsRate(acc.CompletedPlays, acc.Plays)
		slices.SortFunc(acc.Profiles, func(a, b AdminInsightsPersonProfile) int {
			if c := cmp.Compare(b.WatchSeconds, a.WatchSeconds); c != 0 {
				return c
			}
			if c := cmp.Compare(b.Plays, a.Plays); c != 0 {
				return c
			}
			return cmp.Compare(a.ProfileName, b.ProfileName)
		})
		out.ActiveAccounts++
		if accountMatchesSearch(acc, searchLower) {
			allAccounts = append(allAccounts, *acc)
		}
	}
	out.TotalAccounts = int64(len(allAccounts))
	return allAccounts
}

func sortInsightsAccounts(accounts []AdminInsightsPersonAccount, sortRaw, dirRaw string) {
	sortField := strings.ToLower(strings.TrimSpace(sortRaw))
	if sortField == "" {
		sortField = insightsSortWatchTime
	}
	asc := strings.EqualFold(strings.TrimSpace(dirRaw), "asc")
	slices.SortFunc(accounts, func(a, b AdminInsightsPersonAccount) int {
		var c int
		switch sortField {
		case insightsSortPlays:
			c = cmp.Compare(a.Plays, b.Plays)
		case insightsSortDirectPct:
			c = cmp.Compare(a.DirectRate, b.DirectRate)
		case insightsSortCompletionPct:
			c = cmp.Compare(a.CompletionRate, b.CompletionRate)
		case insightsSortLastSeen:
			c = compareTimePtr(a.LastSeenAt, b.LastSeenAt)
		default:
			c = cmp.Compare(a.WatchSeconds, b.WatchSeconds)
		}
		if c != 0 {
			if asc {
				return c
			}
			return -c
		}
		if c := cmp.Compare(b.Plays, a.Plays); c != 0 {
			return c
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
}

func compareTimePtr(a, b *time.Time) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	return a.Compare(*b)
}
