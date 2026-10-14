package jellycompat

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const recipeMutationTimeout = 10 * time.Second

var errRecipeWorkChanged = errors.New("compat recipe upstream work changed")

type recipeCommitState uint8

const (
	recipeCommitNotSent recipeCommitState = iota
	recipeCommitUnknown
)

func recipeWorkLockKey(work string) int64 {
	digest := sha256.Sum256([]byte("silo:compat-recipe-mutation:v1\x00" + work))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
func acceptedRecipe(session *PlaybackSession) *noderecipe.AcceptedPublication {
	accepted := &noderecipe.AcceptedPublication{Scope: session.ID, ID: session.RecipePublicationID, Started: session.TranscodeStarted}
	if session.Recipe != nil {
		card := *session.Recipe
		accepted.Card = &card
	}
	return accepted
}
func validateRecipeMutation(session *PlaybackSession, work string, now time.Time) error {
	if session == nil || session.Terminal || !session.ExpiresAt.After(now) {
		return ErrSessionNotFound
	}
	if work == "" || session.UpstreamSessionID != work {
		return errRecipeWorkChanged
	}
	return nil
}
func assignRecipe(session *PlaybackSession, card playback.RecipeCard, publication noderecipe.Publication, now time.Time) {
	session.Recipe = &card
	session.TranscodeStarted = true
	session.UpdatedAt = now
	session.RecipePublicationID = publication.ID()
	if session.RecipePublicationID == "" {
		session.RecipePublicationID = uuidNewString()
	}
}
func sameAcceptedRecipe(a, b *noderecipe.AcceptedPublication) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}
func undoRecipe(ctx context.Context, publication noderecipe.Publication, err error) error {
	if publication == nil {
		return err
	}
	undoCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if undoErr := publication.Undo(undoCtx); undoErr != nil {
		return errors.Join(err, undoErr, noderecipe.ErrPublicationUncertain)
	}
	return err
}
func recipePublicationUncertain(err error) bool {
	return errors.Is(err, noderecipe.ErrPublicationUncertain) || errors.Is(err, noderecipe.ErrPublicationBusy)
}

// Once the authoritative store accepts the candidate, acknowledgement failure
// must not become permission for callers to stop that transport or restore an
// older selection. Retain its receipt for the next serialized reconciliation.
func acknowledgeRecipe(ctx context.Context, work string, session *PlaybackSession, recipes recipeNodePutter, publication noderecipe.Publication) {
	err := recipes.ReconcileAccepted(ctx, work, acceptedRecipe(session))
	if err == nil {
		err = publication.Commit(ctx)
	}
	if err != nil {
		slog.WarnContext(ctx, "accepted compat recipe acknowledgement pending", "component", "jellycompat", "playback_session_id", session.ID, "error", err)
	}
}

// PublishRecipe runs once under the shared store mutex. It never reenters Get or
// queues a recipe callback for replay after a failed publication.
func (s *PlaybackSessionStore) PublishRecipe(ctx context.Context, id, work string, card playback.RecipeCard, recipes recipeNodePutter) error {
	if card.SessionID == "" {
		card.SessionID = work
	}
	if card.SessionID != work {
		return errors.New("compat recipe work identity mismatch")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recipeMutationTimeout)
	defer cancel()
	if recipeOwner(ctx, s, work) == nil {
		return s.WithRemoteStart(ctx, work, func(owned context.Context) error { return s.PublishRecipe(owned, id, work, card, recipes) })
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if err := validateRecipeMutation(&session, work, s.now()); err != nil {
		return err
	}
	publication, err := recipes.PublishWithUndo(ctx, work, card, acceptedRecipe(&session))
	if err != nil {
		return undoRecipe(ctx, publication, err)
	}
	assignRecipe(&session, card, publication, s.now())
	s.sessions[id] = session
	// The in-memory assignment is now authoritative. If acknowledgement is
	// unavailable, retain it; a later locked mutation can reconcile the receipt.
	acknowledgeRecipe(ctx, work, &session, recipes, publication)
	return nil
}

type recipeLockedConnection struct {
	conn *pgx.Conn
	key  int64
}

func acquireRecipeConnection(ctx context.Context, pool *pgxpool.Pool, work string) (*recipeLockedConnection, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	// Detach before waiting on the work lock. An owner can still need ordinary
	// pooled reads while another replica waits; a MaxConns1 pool must stay usable.
	raw := conn.Hijack()
	locked := &recipeLockedConnection{conn: raw, key: recipeWorkLockKey(work)}
	if _, err = raw.Exec(ctx, "SELECT pg_advisory_lock($1::bigint)", locked.key); err != nil {
		locked.discard()
		return nil, err
	}
	return locked, nil
}
func (c *recipeLockedConnection) discard() {
	if c == nil || c.conn == nil {
		return
	}
	raw := c.conn
	c.conn = nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = raw.Close(ctx)
}
func (c *recipeLockedConnection) release() {
	if c == nil || c.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var released bool
	if err := c.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1::bigint)", c.key).Scan(&released); err != nil || !released {
		c.discard()
		return
	}
	c.discard()
}
func rollbackRecipeTransaction(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func loadRecipeMutation(ctx context.Context, tx pgx.Tx, id, work string, now time.Time) (*PlaybackSession, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT data FROM jellycompat_playback_sessions WHERE id=$1 AND expires_at>$2 FOR UPDATE", id, now).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	var session PlaybackSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	if session.ID != id {
		return nil, errors.New("compat recipe row identity changed")
	}
	if err := validateRecipeMutation(&session, work, now); err != nil {
		return nil, err
	}
	return &session, nil
}

// PublishRecipe keeps one session advisory lock across authoritative row read,
// Redis publication, DB commit and Redis acknowledgement. Replica death releases
// that lock after its backend resolves the transaction; the next publisher uses
// the committed row to resolve the orphan instead of guessing from a lease.
func (d *DurableCompatPlaybackStore) PublishRecipe(ctx context.Context, id, work string, card playback.RecipeCard, recipes recipeNodePutter) error {
	if card.SessionID == "" {
		card.SessionID = work
	}
	if card.SessionID != work {
		return errors.New("compat recipe work identity mismatch")
	}
	if d.pool == nil {
		return d.mem.PublishRecipe(ctx, id, work, card, recipes)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recipeMutationTimeout)
	defer cancel()
	owner := recipeOwner(ctx, d, work)
	if owner == nil {
		return d.WithRemoteStart(ctx, work, func(owned context.Context) error { return d.PublishRecipe(owned, id, work, card, recipes) })
	}
	if owner.locked == nil || owner.locked.conn == nil {
		return noderecipe.ErrPublicationUncertain
	}
	unlock := d.lockSessionMutation(id)
	defer unlock()
	d.cacheMutationMu.RLock()
	defer func() { d.finishCacheMutation(id, d.mem.compatTokenForID(id)) }()
	locked := owner.locked
	tx, err := locked.conn.Begin(ctx)
	if err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	defer rollbackRecipeTransaction(tx)
	previous, err := loadRecipeMutation(ctx, tx, id, work, d.now())
	if err != nil {
		return err
	}
	prior := acceptedRecipe(previous)
	pending := d.pendingUpdatesSnapshot(id)
	for _, update := range pending {
		if err = update.apply(previous); err != nil {
			return err
		}
	}
	if err = validateRecipeMutation(previous, work, d.now()); err != nil {
		return err
	}
	publication, err := recipes.PublishWithUndo(ctx, work, card, prior)
	if err != nil {
		return undoRecipe(ctx, publication, err)
	}
	candidate := *previous
	assignRecipe(&candidate, card, publication, d.now())
	data, err := marshalPlaybackSession(candidate)
	if err == nil {
		_, err = tx.Exec(ctx, upsertSessionQuery, candidate.ID, candidate.CompatToken, candidate.UserID, data, candidate.ExpiresAt)
	}
	if err != nil {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		rollbackErr := tx.Rollback(rollbackCtx)
		rollbackCancel()
		if rollbackErr == nil && !locked.conn.IsClosed() {
			reconcileCtx, reconcileCancel := context.WithTimeout(context.WithoutCancel(ctx), recipeMutationTimeout)
			defer reconcileCancel()
			return d.reconcileRecipeOnConnection(reconcileCtx, locked, id, work, prior, &candidate, recipes, publication, err, recipeCommitNotSent)
		}
		locked.discard()
		return d.reconcileRecipeCommit(ctx, id, work, prior, &candidate, recipes, publication, errors.Join(err, rollbackErr), recipeCommitNotSent)
	}
	if err = tx.Commit(ctx); err != nil {
		// The original backend may have committed despite losing its reply. Close
		// it BEFORE acquiring a new session lock; otherwise we could block ourselves.
		locked.discard()
		reconcileErr := d.reconcileRecipeCommit(ctx, id, work, prior, &candidate, recipes, publication, err, recipeCommitUnknown)
		if reconcileErr == nil && len(pending) > 0 {
			d.consumePendingUpdates(id, pending[len(pending)-1].sequence)
		}
		return reconcileErr
	}
	if len(pending) > 0 {
		d.consumePendingUpdates(id, pending[len(pending)-1].sequence)
	}
	return d.acknowledgeCommittedRecipe(ctx, locked, id, work, &candidate, recipes, publication)
}

func (d *DurableCompatPlaybackStore) acknowledgeCommittedRecipe(ctx context.Context, locked *recipeLockedConnection, id, work string, candidate *PlaybackSession, recipes recipeNodePutter, publication noderecipe.Publication) error {
	// Generic lifecycle updates may clear/move a recipe without taking the work
	// lock. Re-lock and validate the row after commit before resolving Redis.
	tx, err := locked.conn.Begin(ctx)
	if err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	defer rollbackRecipeTransaction(tx)
	current, err := loadRecipeMutation(ctx, tx, id, work, d.now())
	if err != nil {
		return errors.Join(err, noderecipe.ErrPublicationUncertain)
	}
	if !sameAcceptedRecipe(acceptedRecipe(current), acceptedRecipe(candidate)) {
		return noderecipe.ErrPublicationUncertain
	}
	d.mem.Put(*current)
	d.markIDValidated(id)
	acknowledgeRecipe(ctx, work, current, recipes, publication)
	return nil
}

func (d *DurableCompatPlaybackStore) reconcileRecipeCommit(ctx context.Context, id, work string, prior *noderecipe.AcceptedPublication, candidate *PlaybackSession, recipes recipeNodePutter, publication noderecipe.Publication, commitErr error, commitState recipeCommitState) error {
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recipeMutationTimeout)
	defer cancel()
	locked, err := acquireRecipeConnection(reconcileCtx, d.pool, work)
	if err != nil {
		return errors.Join(commitErr, err, noderecipe.ErrPublicationUncertain)
	}
	owner := recipeOwner(ctx, d, work)
	if owner == nil || owner.locked == nil {
		locked.release()
		return errors.Join(commitErr, noderecipe.ErrPublicationUncertain)
	}
	*owner.locked = *locked
	return d.reconcileRecipeOnConnection(reconcileCtx, owner.locked, id, work, prior, candidate, recipes, publication, commitErr, commitState)
}

func (d *DurableCompatPlaybackStore) reconcileRecipeOnConnection(reconcileCtx context.Context, locked *recipeLockedConnection, id, work string, prior *noderecipe.AcceptedPublication, candidate *PlaybackSession, recipes recipeNodePutter, publication noderecipe.Publication, commitErr error, commitState recipeCommitState) error {
	tx, err := locked.conn.Begin(reconcileCtx)
	if err != nil {
		return errors.Join(commitErr, err, noderecipe.ErrPublicationUncertain)
	}
	defer rollbackRecipeTransaction(tx)
	current, err := loadRecipeMutation(reconcileCtx, tx, id, work, d.now())
	if err != nil {
		if commitState == recipeCommitNotSent && (errors.Is(err, ErrSessionNotFound) || errors.Is(err, errRecipeWorkChanged)) {
			return retireRejectedRecipe(reconcileCtx, publication, errors.Join(commitErr, err))
		}
		return errors.Join(commitErr, err, noderecipe.ErrPublicationUncertain)
	}
	accepted := acceptedRecipe(current)
	if sameAcceptedRecipe(accepted, acceptedRecipe(candidate)) {
		d.mem.Put(*current)
		d.markIDValidated(id)
		acknowledgeRecipe(reconcileCtx, work, current, recipes, publication)
		return nil
	}
	if sameAcceptedRecipe(accepted, prior) {
		if err = recipes.ReconcileAccepted(reconcileCtx, work, accepted); err != nil {
			return errors.Join(commitErr, err, noderecipe.ErrPublicationUncertain)
		}
		return commitErr
	}
	if commitState == recipeCommitNotSent {
		return retireRejectedRecipe(reconcileCtx, publication, commitErr)
	}
	return fmt.Errorf("reconcile compat recipe commit: %w", errors.Join(commitErr, noderecipe.ErrPublicationUncertain))
}

func retireRejectedRecipe(ctx context.Context, publication noderecipe.Publication, cause error) error {
	if err := publication.Retire(ctx); err != nil {
		return errors.Join(cause, err, noderecipe.ErrPublicationUncertain)
	}
	return cause
}
