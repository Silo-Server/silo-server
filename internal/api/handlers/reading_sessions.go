package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

const (
	// heartbeatSessionGap is the maximum time between two heartbeats for
	// them to be coalesced into the same reading session. A heartbeat
	// arriving after a longer gap than this starts a new session instead.
	heartbeatSessionGap = 120 * time.Second
	// heartbeatMaxCredit caps how much duration a single heartbeat can add
	// to a session, regardless of the actual gap since the last heartbeat.
	// This bounds the reading-time credit given for e.g. a client that was
	// suspended and resumed just inside the session gap.
	heartbeatMaxCredit = 90 * time.Second
)

// ReadingSession is a coalesced span of reading activity for a single
// (user, profile, content), built up from a stream of heartbeats.
type ReadingSession struct {
	ID              int64
	UserID          int
	ProfileID       string
	ContentID       string
	StartedAt       time.Time
	LastHeartbeatAt time.Time
	DurationSeconds int
	StartFraction   float64
	EndFraction     float64
}

// DayTotal is a single day's reading-time rollup (UTC calendar day).
type DayTotal struct {
	Date    time.Time
	Seconds int
}

// BookTotal is a per-book reading-time rollup. Title is resolved via a LEFT
// JOIN against media_items and is empty when the book has been removed from
// the library; callers map that to a "Removed book" placeholder.
type BookTotal struct {
	ContentID  string
	Title      string
	Seconds    int
	LastReadAt time.Time
}

// SessionRow is a single reading session as surfaced in history/recent
// listings. Title has the same LEFT JOIN / empty-means-removed semantics as
// BookTotal.Title.
type SessionRow struct {
	ContentID       string
	Title           string
	StartedAt       time.Time
	DurationSeconds int
	StartFraction   float64
	EndFraction     float64
}

// ReadingSessionStore persists reading sessions and coalesces heartbeats
// into them.
type ReadingSessionStore interface {
	// LatestOpen returns the most recently touched session for (user,
	// profile, content) whose last heartbeat is at or after since, or nil
	// if none exists.
	LatestOpen(ctx context.Context, userID int, profileID, contentID string, since time.Time) (*ReadingSession, error)
	Insert(ctx context.Context, s ReadingSession) error
	Extend(ctx context.Context, id int64, lastHeartbeatAt time.Time, addSeconds int, endFraction float64) error

	// PaceWindow sums fraction deltas and durations for the profile's
	// sessions since the given time. contentID == "" sums across all of the
	// profile's books; otherwise it is scoped to that book.
	PaceWindow(ctx context.Context, userID int, profileID, contentID string, since time.Time) (fractions float64, seconds int, err error)
	// BookSeconds is the all-time total reading duration for one book.
	BookSeconds(ctx context.Context, userID int, profileID, contentID string) (int, error)
	// DailyRollup groups duration by calendar day (in loc) over [from, to]
	// (inclusive of both endpoint days).
	DailyRollup(ctx context.Context, userID int, profileID string, from, to time.Time, loc *time.Location) ([]DayTotal, error)
	// BookTotals returns all-time per-book totals, newest-read first.
	BookTotals(ctx context.Context, userID int, profileID string) ([]BookTotal, error)
	// RecentSessions returns the most recent sessions, newest-first,
	// capped at limit.
	RecentSessions(ctx context.Context, userID int, profileID string, limit int) ([]SessionRow, error)
	// TotalsSince sums duration for sessions started at or after since. A
	// zero since returns the all-time total. loc is accepted for interface
	// symmetry with DailyRollup; since is already an absolute instant (the
	// caller computes it from the requester's local day boundary), so it
	// does not affect the SQL comparison.
	TotalsSince(ctx context.Context, userID int, profileID string, since time.Time, loc *time.Location) (int, error)
}

// readingProgressGetter is the minimal progress lookup the per-book
// reading-stats handler needs: the profile's current fraction through a
// book. It is satisfied by EbookProgressAdapter, which wraps the existing
// EbookReaderProgressStore so this package doesn't need its full interface.
type readingProgressGetter interface {
	GetProgress(ctx context.Context, userID int, profileID, contentID string) (progress float64, found bool, err error)
}

// EbookProgressAdapter adapts an EbookReaderProgressStore (the store the
// ebook reader already uses for position tracking) to the narrower
// readingProgressGetter interface the reading-stats handlers need.
type EbookProgressAdapter struct {
	Store EbookReaderProgressStore
}

// GetProgress returns the profile's current progress fraction for a book,
// or found=false if no progress has been recorded yet.
func (a EbookProgressAdapter) GetProgress(ctx context.Context, userID int, profileID, contentID string) (float64, bool, error) {
	if a.Store == nil {
		return 0, false, nil
	}
	progress, err := a.Store.Get(ctx, userID, profileID, contentID)
	if err != nil {
		return 0, false, err
	}
	if progress == nil {
		return 0, false, nil
	}
	return progress.Progress, true, nil
}

// ReadingSessionsHandler accepts reader heartbeats and coalesces them into
// reading_sessions rows, and serves the pace/time-left and history read
// endpoints built on top of them. Now is injected so heartbeat gap/credit
// math and time-window boundaries are deterministic under test. Progress is
// nil-tolerant: without it, the per-book handler falls back to progress 0.
type ReadingSessionsHandler struct {
	Store    ReadingSessionStore
	Now      func() time.Time
	Progress readingProgressGetter
}

type readingHeartbeatRequest struct {
	Fraction *float64 `json:"fraction"`
}

// HandleHeartbeat records a reading heartbeat for the active profile,
// extending the most recent still-open session if the last heartbeat was
// within heartbeatSessionGap, or starting a new one otherwise. Responds 204
// on success regardless of whether a session was extended or created, since
// the client does not act on the response body.
func (h *ReadingSessionsHandler) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil || h.Now == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reading sessions are not configured")
		return
	}

	contentID := chi.URLParam(r, "content_id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}

	var req readingHeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_fraction", "Invalid heartbeat body")
		return
	}
	if req.Fraction == nil {
		writeError(w, http.StatusBadRequest, "invalid_fraction", "fraction is required")
		return
	}
	if math.IsNaN(*req.Fraction) || math.IsInf(*req.Fraction, 0) || *req.Fraction < 0 || *req.Fraction > 1 {
		writeError(w, http.StatusBadRequest, "invalid_fraction", "fraction must be a finite number between 0 and 1")
		return
	}

	ctx := r.Context()
	now := h.Now()

	open, err := h.Store.LatestOpen(ctx, userID, profileID, contentID, now.Add(-heartbeatSessionGap))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading session")
		return
	}

	if open != nil {
		credit := now.Sub(open.LastHeartbeatAt)
		if credit > heartbeatMaxCredit {
			credit = heartbeatMaxCredit
		}
		if credit < 0 {
			credit = 0
		}
		if err := h.Store.Extend(ctx, open.ID, now, int(credit.Seconds()), *req.Fraction); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update reading session")
			return
		}
	} else {
		session := ReadingSession{
			UserID:          userID,
			ProfileID:       profileID,
			ContentID:       contentID,
			StartedAt:       now,
			LastHeartbeatAt: now,
			DurationSeconds: 0,
			StartFraction:   *req.Fraction,
			EndFraction:     *req.Fraction,
		}
		if err := h.Store.Insert(ctx, session); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start reading session")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

const (
	// paceLookbackWindow is how far back pace calculations look for
	// sessions, per spec.
	paceLookbackWindow = 14 * 24 * time.Hour
	// paceThisBookMinSeconds is the minimum this-book reading time in the
	// lookback window before this-book pace is trusted.
	paceThisBookMinSeconds = 600
	// paceAllBooksMinSeconds is the minimum all-books reading time in the
	// lookback window before the all-books fallback pace is trusted.
	paceAllBooksMinSeconds = 1800
	// maxRecentSessions caps the history endpoint's sessions list.
	maxRecentSessions = 50
	// defaultHistoryRangeDays is the default from/to span (in days) when
	// the history endpoint is called with no range params.
	defaultHistoryRangeDays = 365
)

// paceAndTimeLeft computes a reading pace estimate and, from it, an
// estimated time remaining in the book. bookF/bookSec are the summed
// fraction delta and duration over the lookback window for this book;
// allF/allSec are the same summed across all of the profile's books.
// progress is the profile's current fraction through the book.
//
// This-book pace is used if it clears paceThisBookMinSeconds and has a
// positive fraction delta (a zero or negative delta is guarded against,
// since it would produce a zero, infinite, or backwards pace). Otherwise
// the all-books pace is used under the same conditions with
// paceAllBooksMinSeconds. If neither qualifies, both return values are nil.
func paceAndTimeLeft(bookF float64, bookSec int, allF float64, allSec int, progress float64) (paceFPH *float64, timeLeftSec *int64) {
	var pace float64
	switch {
	case bookSec >= paceThisBookMinSeconds && bookF > 0:
		pace = bookF / float64(bookSec) * 3600
	case allSec >= paceAllBooksMinSeconds && allF > 0:
		pace = allF / float64(allSec) * 3600
	default:
		return nil, nil
	}

	remaining := 1 - progress
	if remaining < 0 {
		remaining = 0
	}
	paceFractionPerSecond := pace / 3600
	seconds := int64(math.Round(remaining / paceFractionPerSecond))

	return &pace, &seconds
}

type bookReadingStatsResponse struct {
	PaceFractionPerHour *float64 `json:"pace_fraction_per_hour"`
	TimeLeftSeconds     *int64   `json:"time_left_seconds"`
	BookSeconds         int      `json:"book_seconds"`
}

// HandleBookStats returns the pace and time-left estimate for one book, plus
// the all-time reading duration for it.
func (h *ReadingSessionsHandler) HandleBookStats(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil || h.Now == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reading sessions are not configured")
		return
	}

	contentID := chi.URLParam(r, "content_id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}

	ctx := r.Context()
	since := h.Now().Add(-paceLookbackWindow)

	bookF, bookSec, err := h.Store.PaceWindow(ctx, userID, profileID, contentID, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading pace")
		return
	}
	allF, allSec, err := h.Store.PaceWindow(ctx, userID, profileID, "", since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading pace")
		return
	}
	bookSeconds, err := h.Store.BookSeconds(ctx, userID, profileID, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load book reading time")
		return
	}

	var progress float64
	if h.Progress != nil {
		p, found, err := h.Progress.GetProgress(ctx, userID, profileID, contentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading progress")
			return
		}
		if found {
			progress = p
		}
	}

	pace, timeLeft := paceAndTimeLeft(bookF, bookSec, allF, allSec, progress)
	writeJSON(w, http.StatusOK, bookReadingStatsResponse{
		PaceFractionPerHour: pace,
		TimeLeftSeconds:     timeLeft,
		BookSeconds:         bookSeconds,
	})
}

type readingStatsTotalsResponse struct {
	TodaySeconds   int `json:"today_seconds"`
	WeekSeconds    int `json:"week_seconds"`
	MonthSeconds   int `json:"month_seconds"`
	AllTimeSeconds int `json:"all_time_seconds"`
}

type readingStatsDayResponse struct {
	Date    string `json:"date"`
	Seconds int    `json:"seconds"`
}

type readingStatsBookResponse struct {
	ContentID  string `json:"content_id"`
	Title      string `json:"title"`
	Seconds    int    `json:"seconds"`
	LastReadAt string `json:"last_read_at"`
}

type readingStatsSessionResponse struct {
	ContentID       string  `json:"content_id"`
	Title           string  `json:"title"`
	StartedAt       string  `json:"started_at"`
	DurationSeconds int     `json:"duration_seconds"`
	StartFraction   float64 `json:"start_fraction"`
	EndFraction     float64 `json:"end_fraction"`
}

type readingStatsHistoryResponse struct {
	Totals   readingStatsTotalsResponse    `json:"totals"`
	Days     []readingStatsDayResponse     `json:"days"`
	Books    []readingStatsBookResponse    `json:"books"`
	Sessions []readingStatsSessionResponse `json:"sessions"`
}

// readingStatsTitle maps an empty LEFT-JOIN-resolved title (book removed
// from the library) to a stable placeholder.
func readingStatsTitle(title string) string {
	if title == "" {
		return "Removed book"
	}
	return title
}

// HandleHistory returns the profile's reading history: fixed-window totals
// (today/week/month/all-time), a daily rollup over an optional [from, to]
// range (defaulting to the last defaultHistoryRangeDays+1 days), all-time
// per-book totals, and the most recent sessions.
func (h *ReadingSessionsHandler) HandleHistory(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil || h.Now == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reading sessions are not configured")
		return
	}

	loc := requestLocation(r)
	now := h.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	q := r.URL.Query()
	to := today
	if v := q.Get("to"); v != "" {
		parsed, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "to must be a valid date (YYYY-MM-DD)")
			return
		}
		to = parsed
	}
	from := to.AddDate(0, 0, -defaultHistoryRangeDays)
	if v := q.Get("from"); v != "" {
		parsed, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "from must be a valid date (YYYY-MM-DD)")
			return
		}
		from = parsed
	}
	if from.After(to) {
		from = to
	}

	ctx := r.Context()

	todaySeconds, err := h.Store.TotalsSince(ctx, userID, profileID, today, loc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading totals")
		return
	}
	weekSeconds, err := h.Store.TotalsSince(ctx, userID, profileID, today.AddDate(0, 0, -6), loc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading totals")
		return
	}
	monthSeconds, err := h.Store.TotalsSince(ctx, userID, profileID, today.AddDate(0, 0, -29), loc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading totals")
		return
	}
	allTimeSeconds, err := h.Store.TotalsSince(ctx, userID, profileID, time.Time{}, loc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reading totals")
		return
	}

	days, err := h.Store.DailyRollup(ctx, userID, profileID, from, to, loc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load daily reading rollup")
		return
	}
	books, err := h.Store.BookTotals(ctx, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load book reading totals")
		return
	}
	sessions, err := h.Store.RecentSessions(ctx, userID, profileID, maxRecentSessions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load recent reading sessions")
		return
	}
	if len(sessions) > maxRecentSessions {
		sessions = sessions[:maxRecentSessions]
	}

	resp := readingStatsHistoryResponse{
		Totals: readingStatsTotalsResponse{
			TodaySeconds:   todaySeconds,
			WeekSeconds:    weekSeconds,
			MonthSeconds:   monthSeconds,
			AllTimeSeconds: allTimeSeconds,
		},
		Days:     make([]readingStatsDayResponse, 0, len(days)),
		Books:    make([]readingStatsBookResponse, 0, len(books)),
		Sessions: make([]readingStatsSessionResponse, 0, len(sessions)),
	}
	for _, d := range days {
		resp.Days = append(resp.Days, readingStatsDayResponse{
			Date:    d.Date.Format("2006-01-02"),
			Seconds: d.Seconds,
		})
	}
	for _, b := range books {
		resp.Books = append(resp.Books, readingStatsBookResponse{
			ContentID:  b.ContentID,
			Title:      readingStatsTitle(b.Title),
			Seconds:    b.Seconds,
			LastReadAt: b.LastReadAt.Format(time.RFC3339),
		})
	}
	for _, s := range sessions {
		resp.Sessions = append(resp.Sessions, readingStatsSessionResponse{
			ContentID:       s.ContentID,
			Title:           readingStatsTitle(s.Title),
			StartedAt:       s.StartedAt.Format(time.RFC3339),
			DurationSeconds: s.DurationSeconds,
			StartFraction:   s.StartFraction,
			EndFraction:     s.EndFraction,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// PGReadingSessionStore is the Postgres-backed ReadingSessionStore.
type PGReadingSessionStore struct {
	pool *pgxpool.Pool
}

// NewPGReadingSessionStore constructs a PGReadingSessionStore.
func NewPGReadingSessionStore(pool *pgxpool.Pool) *PGReadingSessionStore {
	return &PGReadingSessionStore{pool: pool}
}

func (s *PGReadingSessionStore) LatestOpen(ctx context.Context, userID int, profileID, contentID string, since time.Time) (*ReadingSession, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading session store is not configured")
	}
	var session ReadingSession
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, content_id, started_at, last_heartbeat_at,
		       duration_seconds, start_fraction, end_fraction
		FROM reading_sessions
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3 AND last_heartbeat_at >= $4
		ORDER BY last_heartbeat_at DESC
		LIMIT 1`,
		userID, profileID, contentID, since,
	).Scan(
		&session.ID,
		&session.UserID,
		&session.ProfileID,
		&session.ContentID,
		&session.StartedAt,
		&session.LastHeartbeatAt,
		&session.DurationSeconds,
		&session.StartFraction,
		&session.EndFraction,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("latest open reading session: %w", err)
	}
	return &session, nil
}

func (s *PGReadingSessionStore) Insert(ctx context.Context, session ReadingSession) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("reading session store is not configured")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reading_sessions
			(user_id, profile_id, content_id, started_at, last_heartbeat_at, duration_seconds, start_fraction, end_fraction)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		session.UserID, session.ProfileID, session.ContentID,
		session.StartedAt, session.LastHeartbeatAt, session.DurationSeconds,
		session.StartFraction, session.EndFraction,
	)
	if err != nil {
		return fmt.Errorf("insert reading session: %w", err)
	}
	return nil
}

func (s *PGReadingSessionStore) Extend(ctx context.Context, id int64, lastHeartbeatAt time.Time, addSeconds int, endFraction float64) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("reading session store is not configured")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE reading_sessions
		SET last_heartbeat_at = $2, duration_seconds = duration_seconds + $3, end_fraction = $4
		WHERE id = $1`,
		id, lastHeartbeatAt, addSeconds, endFraction,
	)
	if err != nil {
		return fmt.Errorf("extend reading session: %w", err)
	}
	return nil
}

func (s *PGReadingSessionStore) PaceWindow(ctx context.Context, userID int, profileID, contentID string, since time.Time) (float64, int, error) {
	if s == nil || s.pool == nil {
		return 0, 0, fmt.Errorf("reading session store is not configured")
	}
	query := `
		SELECT COALESCE(SUM(end_fraction - start_fraction), 0), COALESCE(SUM(duration_seconds), 0)
		FROM reading_sessions
		WHERE user_id = $1 AND profile_id = $2 AND last_heartbeat_at >= $3`
	args := []any{userID, profileID, since}
	if contentID != "" {
		query += " AND content_id = $4"
		args = append(args, contentID)
	}
	var fractions float64
	var seconds int
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&fractions, &seconds); err != nil {
		return 0, 0, fmt.Errorf("pace window: %w", err)
	}
	return fractions, seconds, nil
}

func (s *PGReadingSessionStore) BookSeconds(ctx context.Context, userID int, profileID, contentID string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("reading session store is not configured")
	}
	var seconds int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(duration_seconds), 0)
		FROM reading_sessions
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3`,
		userID, profileID, contentID,
	).Scan(&seconds)
	if err != nil {
		return 0, fmt.Errorf("book seconds: %w", err)
	}
	return seconds, nil
}

func (s *PGReadingSessionStore) DailyRollup(ctx context.Context, userID int, profileID string, from, to time.Time, loc *time.Location) ([]DayTotal, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading session store is not configured")
	}
	// Bucketing is pinned to an explicit zone via "AT TIME ZONE $5" rather
	// than a bare date_trunc('day', started_at): date_trunc on a timestamptz
	// truncates in the DB session's timezone, which this pool never sets, so
	// the bucket boundary would otherwise depend on server/connection config
	// instead of being deterministic. The zone name comes from the
	// requester's tz param (loc), defaulting to "UTC".
	rows, err := s.pool.Query(ctx, `
		SELECT date_trunc('day', started_at AT TIME ZONE $5)::date AS day, SUM(duration_seconds)
		FROM reading_sessions
		WHERE user_id = $1 AND profile_id = $2 AND started_at >= $3 AND started_at < $4
		GROUP BY 1
		ORDER BY 1`,
		userID, profileID, from, to.AddDate(0, 0, 1), loc.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("daily rollup: %w", err)
	}
	defer rows.Close()

	var out []DayTotal
	for rows.Next() {
		var d DayTotal
		if err := rows.Scan(&d.Date, &d.Seconds); err != nil {
			return nil, fmt.Errorf("scan daily rollup: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily rollup: %w", err)
	}
	return out, nil
}

func (s *PGReadingSessionStore) BookTotals(ctx context.Context, userID int, profileID string) ([]BookTotal, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading session store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT rs.content_id, COALESCE(mi.title, ''), SUM(rs.duration_seconds), MAX(rs.last_heartbeat_at)
		FROM reading_sessions rs
		LEFT JOIN media_items mi ON mi.content_id = rs.content_id
		WHERE rs.user_id = $1 AND rs.profile_id = $2
		GROUP BY rs.content_id, mi.title
		ORDER BY MAX(rs.last_heartbeat_at) DESC`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("book totals: %w", err)
	}
	defer rows.Close()

	var out []BookTotal
	for rows.Next() {
		var b BookTotal
		if err := rows.Scan(&b.ContentID, &b.Title, &b.Seconds, &b.LastReadAt); err != nil {
			return nil, fmt.Errorf("scan book totals: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate book totals: %w", err)
	}
	return out, nil
}

func (s *PGReadingSessionStore) RecentSessions(ctx context.Context, userID int, profileID string, limit int) ([]SessionRow, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading session store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT rs.content_id, COALESCE(mi.title, ''), rs.started_at, rs.duration_seconds, rs.start_fraction, rs.end_fraction
		FROM reading_sessions rs
		LEFT JOIN media_items mi ON mi.content_id = rs.content_id
		WHERE rs.user_id = $1 AND rs.profile_id = $2
		ORDER BY rs.started_at DESC
		LIMIT $3`,
		userID, profileID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("recent sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionRow
	for rows.Next() {
		var s SessionRow
		if err := rows.Scan(&s.ContentID, &s.Title, &s.StartedAt, &s.DurationSeconds, &s.StartFraction, &s.EndFraction); err != nil {
			return nil, fmt.Errorf("scan recent sessions: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent sessions: %w", err)
	}
	return out, nil
}

func (s *PGReadingSessionStore) TotalsSince(ctx context.Context, userID int, profileID string, since time.Time, _ *time.Location) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("reading session store is not configured")
	}
	var seconds int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(duration_seconds), 0)
		FROM reading_sessions
		WHERE user_id = $1 AND profile_id = $2 AND started_at >= $3`,
		userID, profileID, since,
	).Scan(&seconds)
	if err != nil {
		return 0, fmt.Errorf("totals since: %w", err)
	}
	return seconds, nil
}
