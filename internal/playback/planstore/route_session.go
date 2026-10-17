package planstore

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const routeBackendLimitV3 = 8

type postgresRouteSession struct {
	store                       *Postgres
	sessionID                   string
	conn                        *pgx.Conn
	releaseGate, releaseBackend func()
	closed                      bool
}

func (s *Postgres) AcquireRouteSession(ctx context.Context, sessionID string) (playback.RouteSessionV3, error) {
	releaseGate, err := s.routeLocks.Acquire(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	s.routeBackendOnce.Do(func() {
		s.routeBackendSlots = make(chan struct{}, max(1, min(routeBackendLimitV3, int(s.db.Config().MaxConns))))
	})
	select {
	case s.routeBackendSlots <- struct{}{}:
	case <-ctx.Done():
		releaseGate()
		return nil, ctx.Err()
	}
	var once sync.Once
	owned := &postgresRouteSession{store: s, sessionID: sessionID, releaseGate: releaseGate, releaseBackend: func() { once.Do(func() { <-s.routeBackendSlots }) }}
	if err = owned.acquire(ctx); err != nil {
		owned.Close()
		return nil, err
	}
	return owned, nil
}

func (s *postgresRouteSession) acquire(ctx context.Context) error {
	if s.closed {
		return errors.New("route session closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := s.store.db.Acquire(ctx)
	if err != nil {
		return err
	}
	// A callback from the accepted worker may need this pool even when its
	// MaxConns is one. Detached backends are admitted BEFORE Acquire above,
	// consume no ordinary pool slot while owning G, and are always closed.
	// Existing pooled replan holders/waiters still need their usual headroom.
	s.conn = conn.Hijack()
	if _, err = s.conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, s.sessionID); err != nil {
		s.discard()
		return err
	}
	return nil
}

func (s *postgresRouteSession) discard() {
	if s.conn == nil {
		return
	}
	conn := s.conn
	s.conn = nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

func (s *postgresRouteSession) Close() {
	if s.closed {
		return
	}
	s.closed = true
	s.discard()
	s.releaseBackend()
	s.releaseGate()
}

func (s *postgresRouteSession) GetAttempt(ctx context.Context) (*playback.AttemptRecordV3, error) {
	if s.closed {
		return nil, errors.New("route session closed")
	}
	// A failed command discarded its original backend. Acquiring this same
	// advisory key joins any delayed commit before a fresh outcome is read.
	if s.conn == nil {
		if err := s.acquire(ctx); err != nil {
			return nil, err
		}
	}
	record, err := scanAttemptV3(s.conn.QueryRow(ctx, attemptSelectV3+` WHERE session_id=$1::uuid`, s.sessionID))
	if err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		s.discard()
	}
	return record, err
}

func (s *postgresRouteSession) CompareAndSwap(ctx context.Context, expected playback.AttemptRecordV3, next playback.TranscodeRoute) (bool, error) {
	if s.closed || s.conn == nil {
		return false, errors.New("route session ownership lost")
	}
	if expected.SessionID != s.sessionID {
		return false, nil
	}
	tag, err := s.conn.Exec(ctx, `
		UPDATE playback_v3_attempts SET frozen_recipe =
		 jsonb_set(jsonb_set(frozen_recipe,'{transcode_node_url}',to_jsonb($2::text),true),'{transcode_transport_id}',to_jsonb($3::text),true)
		WHERE session_id=$1::uuid AND playback_attempt_id=$4
		 AND current_plan_id=$5 AND current_replan_request_id=$6
		 AND user_id=$7 AND profile_id=$8 AND effective_media_file_id=$9
		 AND COALESCE(frozen_recipe->>'transcode_node_url','')=$10
		 AND COALESCE(frozen_recipe->>'transcode_transport_id','')=$11
		 AND stopped_at IS NULL AND expires_at>NOW()`,
		s.sessionID, next.NodeURL, next.TransportID, expected.PlaybackAttemptID, expected.CurrentPlanID, expected.CurrentReplanRequestID,
		expected.UserID, expected.ProfileID, expected.EffectiveMediaFileID, expected.FrozenRecipe.TranscodeNodeURL, expected.FrozenRecipe.TranscodeTransportID)
	if err != nil {
		// Do not infer rollback from a deadline/reset, or reuse a connection
		// whose last write may have committed without its acknowledgement.
		s.discard()
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *postgresRouteSession) WithLiveRoute(ctx context.Context, expected playback.AttemptRecordV3, run func(context.Context) error) (bool, error) {
	if s.closed || s.conn == nil {
		return false, errors.New("route session ownership lost")
	}
	if expected.SessionID != s.sessionID {
		return false, nil
	}
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		s.discard()
		return false, err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := tx.Rollback(releaseCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			s.discard()
		}
	}()
	// STOP does not take the route advisory lock. Own this row before the
	// callback can take a process-local lifecycle lock or restore authority.
	current, err := scanAttemptV3(tx.QueryRow(ctx, attemptSelectV3+` WHERE session_id=$1::uuid FOR UPDATE`, s.sessionID))
	if err != nil {
		return false, err
	}
	if !playback.SameLiveRouteV3(current, &expected) {
		return false, nil
	}
	callbackCtx, cancel := context.WithDeadline(ctx, current.ExpiresAt)
	defer cancel()
	return true, run(callbackCtx)
}
