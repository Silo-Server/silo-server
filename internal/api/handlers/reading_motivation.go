package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
)

// requestLocation resolves the requester's timezone from the "tz" query
// param (an IANA zone name, typically Intl.DateTimeFormat().resolvedOptions().timeZone
// from the client), falling back to UTC when the param is absent, invalid,
// or "Local". "Local" is rejected explicitly even though time.LoadLocation
// would resolve it successfully, since it means "the server's zone", not a
// meaningful request-scoped default.
func requestLocation(r *http.Request) *time.Location {
	name := strings.TrimSpace(r.URL.Query().Get("tz"))
	if name == "" || name == "Local" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ReadingGoals is a profile's yearly reading goals. A nil field is unset.
type ReadingGoals struct {
	BooksPerYear *int
	HoursPerYear *int
}

// GenreSeconds is a per-genre reading-time rollup. A book's duration is
// split evenly across its genres.
type GenreSeconds struct {
	Genre   string
	Seconds int
}

// AuthorSeconds is a per-author reading-time rollup.
type AuthorSeconds struct {
	Author  string
	Seconds int
}

// ReadingMotivationStore persists reading goals and achievement unlocks, and
// supplies the raw aggregates the motivation endpoint's pure math (streaks,
// challenge, DNA, badge criteria) is built from.
type ReadingMotivationStore interface {
	GetGoals(ctx context.Context, userID int, profileID string) (*ReadingGoals, error)
	PutGoals(ctx context.Context, userID int, profileID string, g ReadingGoals) error

	// AchievedAt returns the profile's unlocked achievement ids mapped to
	// when they were unlocked.
	AchievedAt(ctx context.Context, userID int, profileID string) (map[string]time.Time, error)
	// PersistAchievement records a newly satisfied badge. Implementations
	// must be idempotent (a badge, once unlocked, is never revoked or
	// re-timestamped).
	PersistAchievement(ctx context.Context, userID int, profileID, achievementID string, at time.Time) error

	// SessionsSince returns raw session rows since the given time for the
	// pure aggregation math to consume.
	SessionsSince(ctx context.Context, userID int, profileID string, since time.Time) ([]ReadingSession, error)
	// FinishedBooksInRange counts ebooks whose progress crossed
	// models.EbookFinishedProgressThreshold with the progress row's
	// updated_at in [from, to).
	FinishedBooksInRange(ctx context.Context, userID int, profileID string, from, to time.Time) (int, error)
	// GenreSeconds returns all-time per-genre reading-time totals,
	// highest first.
	GenreSeconds(ctx context.Context, userID int, profileID string) ([]GenreSeconds, error)
	// AuthorSeconds returns all-time per-author reading-time totals,
	// highest first.
	AuthorSeconds(ctx context.Context, userID int, profileID string) ([]AuthorSeconds, error)
}

// ReadingMotivationHandler serves the reading-goals PUT endpoint (and, from
// a later task, the reading-motivation GET endpoint). Now is injected so
// achievement-unlock and goal-year math is deterministic under test.
type ReadingMotivationHandler struct {
	Store ReadingMotivationStore
	Now   func() time.Time
}

const (
	// minYearlyGoal and maxYearlyGoal bound both books_per_year and
	// hours_per_year, per spec.
	minYearlyGoal = 1
	maxYearlyGoal = 100000
)

type putGoalsRequest struct {
	BooksPerYear *int `json:"books_per_year"`
	HoursPerYear *int `json:"hours_per_year"`
}

// HandlePutGoals replaces the profile's reading goals wholesale: this is PUT
// semantics, not PATCH, so an absent field decodes to nil and clears that
// goal rather than leaving the stored value untouched.
func (h *ReadingMotivationHandler) HandlePutGoals(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reading goals are not configured")
		return
	}

	var req putGoalsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid goals body")
		return
	}
	if req.BooksPerYear != nil && (*req.BooksPerYear < minYearlyGoal || *req.BooksPerYear > maxYearlyGoal) {
		writeError(w, http.StatusBadRequest, "invalid_goal", "books_per_year must be between 1 and 100000")
		return
	}
	if req.HoursPerYear != nil && (*req.HoursPerYear < minYearlyGoal || *req.HoursPerYear > maxYearlyGoal) {
		writeError(w, http.StatusBadRequest, "invalid_goal", "hours_per_year must be between 1 and 100000")
		return
	}

	goals := ReadingGoals{BooksPerYear: req.BooksPerYear, HoursPerYear: req.HoursPerYear}
	if err := h.Store.PutGoals(r.Context(), userID, profileID, goals); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save reading goals")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PGReadingMotivationStore is the Postgres-backed ReadingMotivationStore.
type PGReadingMotivationStore struct {
	pool *pgxpool.Pool
}

// NewPGReadingMotivationStore constructs a PGReadingMotivationStore.
func NewPGReadingMotivationStore(pool *pgxpool.Pool) *PGReadingMotivationStore {
	return &PGReadingMotivationStore{pool: pool}
}

func (s *PGReadingMotivationStore) GetGoals(ctx context.Context, userID int, profileID string) (*ReadingGoals, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	var g ReadingGoals
	err := s.pool.QueryRow(ctx, `
		SELECT books_per_year, hours_per_year
		FROM reading_goals
		WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	).Scan(&g.BooksPerYear, &g.HoursPerYear)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get goals: %w", err)
	}
	return &g, nil
}

func (s *PGReadingMotivationStore) PutGoals(ctx context.Context, userID int, profileID string, g ReadingGoals) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("reading motivation store is not configured")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reading_goals (user_id, profile_id, books_per_year, hours_per_year, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, profile_id) DO UPDATE
		SET books_per_year = $3, hours_per_year = $4, updated_at = now()`,
		userID, profileID, g.BooksPerYear, g.HoursPerYear,
	)
	if err != nil {
		return fmt.Errorf("put goals: %w", err)
	}
	return nil
}

func (s *PGReadingMotivationStore) AchievedAt(ctx context.Context, userID int, profileID string) (map[string]time.Time, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT achievement_id, achieved_at
		FROM reading_achievements
		WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("achieved at: %w", err)
	}
	defer rows.Close()

	out := make(map[string]time.Time)
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("scan achieved at: %w", err)
		}
		out[id] = at
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate achieved at: %w", err)
	}
	return out, nil
}

func (s *PGReadingMotivationStore) PersistAchievement(ctx context.Context, userID int, profileID, achievementID string, at time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("reading motivation store is not configured")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reading_achievements (user_id, profile_id, achievement_id, achieved_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, profile_id, achievement_id) DO NOTHING`,
		userID, profileID, achievementID, at,
	)
	if err != nil {
		return fmt.Errorf("persist achievement: %w", err)
	}
	return nil
}

func (s *PGReadingMotivationStore) SessionsSince(ctx context.Context, userID int, profileID string, since time.Time) ([]ReadingSession, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, profile_id, content_id, started_at, last_heartbeat_at,
		       duration_seconds, start_fraction, end_fraction
		FROM reading_sessions
		WHERE user_id = $1 AND profile_id = $2 AND started_at >= $3
		ORDER BY started_at`,
		userID, profileID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("sessions since: %w", err)
	}
	defer rows.Close()

	var out []ReadingSession
	for rows.Next() {
		var sess ReadingSession
		if err := rows.Scan(
			&sess.ID,
			&sess.UserID,
			&sess.ProfileID,
			&sess.ContentID,
			&sess.StartedAt,
			&sess.LastHeartbeatAt,
			&sess.DurationSeconds,
			&sess.StartFraction,
			&sess.EndFraction,
		); err != nil {
			return nil, fmt.Errorf("scan sessions since: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions since: %w", err)
	}
	return out, nil
}

func (s *PGReadingMotivationStore) FinishedBooksInRange(ctx context.Context, userID int, profileID string, from, to time.Time) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("reading motivation store is not configured")
	}
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM ebook_reader_progress
		WHERE user_id = $1 AND profile_id = $2 AND progress >= $3 AND updated_at >= $4 AND updated_at < $5`,
		userID, profileID, models.EbookFinishedProgressThreshold, from, to,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("finished books in range: %w", err)
	}
	return count, nil
}

func (s *PGReadingMotivationStore) GenreSeconds(ctx context.Context, userID int, profileID string) ([]GenreSeconds, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT g.genre, SUM(rs.duration_seconds::float / GREATEST(cardinality(mi.genres), 1))::bigint
		FROM reading_sessions rs
		JOIN media_items mi ON mi.content_id = rs.content_id
		CROSS JOIN LATERAL unnest(mi.genres) AS g(genre)
		WHERE rs.user_id = $1 AND rs.profile_id = $2
		GROUP BY g.genre
		ORDER BY 2 DESC`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("genre seconds: %w", err)
	}
	defer rows.Close()

	var out []GenreSeconds
	for rows.Next() {
		var g GenreSeconds
		if err := rows.Scan(&g.Genre, &g.Seconds); err != nil {
			return nil, fmt.Errorf("scan genre seconds: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate genre seconds: %w", err)
	}
	return out, nil
}

func (s *PGReadingMotivationStore) AuthorSeconds(ctx context.Context, userID int, profileID string) ([]AuthorSeconds, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.name, SUM(rs.duration_seconds)
		FROM reading_sessions rs
		JOIN item_people ip ON ip.content_id = rs.content_id AND ip.kind = $3
		JOIN people p ON p.id = ip.person_id
		WHERE rs.user_id = $1 AND rs.profile_id = $2
		GROUP BY p.name
		ORDER BY 2 DESC
		LIMIT 8`,
		userID, profileID, int(models.PersonKindAuthor),
	)
	if err != nil {
		return nil, fmt.Errorf("author seconds: %w", err)
	}
	defer rows.Close()

	var out []AuthorSeconds
	for rows.Next() {
		var a AuthorSeconds
		if err := rows.Scan(&a.Author, &a.Seconds); err != nil {
			return nil, fmt.Errorf("scan author seconds: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate author seconds: %w", err)
	}
	return out, nil
}
