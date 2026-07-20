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

// ReadingSessionStore persists reading sessions and coalesces heartbeats
// into them.
type ReadingSessionStore interface {
	// LatestOpen returns the most recently touched session for (user,
	// profile, content) whose last heartbeat is at or after since, or nil
	// if none exists.
	LatestOpen(ctx context.Context, userID int, profileID, contentID string, since time.Time) (*ReadingSession, error)
	Insert(ctx context.Context, s ReadingSession) error
	Extend(ctx context.Context, id int64, lastHeartbeatAt time.Time, addSeconds int, endFraction float64) error
}

// ReadingSessionsHandler accepts reader heartbeats and coalesces them into
// reading_sessions rows. Now is injected so heartbeat gap/credit math is
// deterministic under test.
type ReadingSessionsHandler struct {
	Store ReadingSessionStore
	Now   func() time.Time
}

type readingHeartbeatRequest struct {
	Fraction float64 `json:"fraction"`
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
	if math.IsNaN(req.Fraction) || math.IsInf(req.Fraction, 0) || req.Fraction < 0 || req.Fraction > 1 {
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
		if err := h.Store.Extend(ctx, open.ID, now, int(credit.Seconds()), req.Fraction); err != nil {
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
			StartFraction:   req.Fraction,
			EndFraction:     req.Fraction,
		}
		if err := h.Store.Insert(ctx, session); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start reading session")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
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
