package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Transactional evidence persistence and refusal reconciliation.
//
// The best-effort saver commits the evidence write and then clears the
// exact-pin refusal mark in a separate statement. A cancellation or database
// interruption between the two leaves a stale threshold live, so the next
// foreign candidate inherits it toward a premature terminal; symmetrically, the
// refusal counter and the terminal verdict are recorded and read separately, so
// a concurrent success on another replica can lose to a stale decision.
//
// When a real database and the durable refusal store are both wired these steps
// run in one transaction, serialized on the row they describe:
//
//   - the row lock is taken first (media_files before virtual_adoption_refusals),
//     so every replica reconciles one row in the same order and cannot deadlock
//     or observe a half-applied decision;
//   - a successful write clears the exact-pin mark in the same transaction, so
//     there is no window between the evidence and the clear;
//   - a refused required adoption records the increment, re-reads the count
//     in-transaction, and installs the terminal verdict from that count, not
//     from the caller's earlier read;
//   - the terminal stamp is fenced on the observed evidence revision
//     (updated_at/probe_updated_at), so a newer evidence write that advanced the
//     row wins instead of being condemned by a stale terminal;
//   - the collision retry re-runs under a savepoint, so a unique-path race that
//     aborts the statement does not abort the reconciliation.

// lockVirtualFileRowSQL takes the row lock the transactional reconciliation
// serializes on. It is deliberately the first statement of the transaction so
// the lock order (media_files, then virtual_adoption_refusals) is identical on
// every replica.
const lockVirtualFileRowSQL = `SELECT id FROM media_files WHERE id = $1 FOR UPDATE`

// savepointVirtualAdoptAttempt guards the adoption attempt that can fail with a
// unique-path collision. A statement error aborts a Postgres transaction until
// it is rolled back to a savepoint, so the metadata-only retry must rewind to
// this point before it can run.
const (
	savepointVirtualAdoptAttempt = "virtual_adopt_attempt"
	savepointVirtualAdoptSQL     = "SAVEPOINT virtual_adopt_attempt"
	rollbackVirtualAdoptSQL      = "ROLLBACK TO SAVEPOINT virtual_adopt_attempt"
)

// execVirtualFileMetadataUpdateResultTx is the atomic production saver: the
// evidence write, the exact-pin clear, the refusal increment and the terminal
// decision all commit or roll back together.
func execVirtualFileMetadataUpdateResultTx(ctx context.Context, db VirtualFileMetadataTxBeginner, store VirtualAdoptionRefusalTxStore, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
	statement, err := newVirtualMetadataStatement(args)
	if err != nil {
		return VirtualFileMetadataUpdateResult{}, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("begin virtual metadata transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	// Serialize every reconciliation for this row on the row lock first. A
	// missing row is a plain no-op: there is nothing to write, and a refusal row
	// for a vanished file would only fail its foreign key.
	if args.FileID > 0 {
		var lockedID int
		lockErr := tx.QueryRow(ctx, lockVirtualFileRowSQL, args.FileID).Scan(&lockedID)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return VirtualFileMetadataUpdateResult{}, nil
		}
		if lockErr != nil {
			return VirtualFileMetadataUpdateResult{}, fmt.Errorf("lock virtual metadata row: %w", lockErr)
		}
	}

	// The adoption attempt can hit the media_files_virtual_file_owner_key
	// unique index; guard it with a savepoint so the metadata-only retry can
	// rewind the aborted statement instead of losing the whole reconciliation.
	if _, err := tx.Exec(ctx, savepointVirtualAdoptSQL); err != nil {
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("open virtual adoption savepoint: %w", err)
	}

	persistedPath, writeErr := statement.run(ctx, tx, args, args.AdoptPath)
	if writeErr == nil && persistedPath == "" {
		// A stale CAS snapshot is not an identity refusal. A provider re-list
		// advances updated_at without touching probe_updated_at, and a rematch
		// may churn the ?result= token. The row is locked for this transaction,
		// so re-read it and retry the same write against the state that is
		// actually current; only a row still inside the same item/profile is
		// re-based. A metadata-only write additionally requires the same exact
		// path, owner and probe stamp so a listing refresh cannot redirect the
		// evidence to another candidate.
		persistedPath, args, writeErr = retryVirtualMetadataUpdate(ctx, tx, args, statement)
	}
	if writeErr == nil {
		if persistedPath == "" {
			// Only a required adoption that matched no row is a refusal. An
			// ordinary metadata CAS miss is not: counting it would replenish
			// the exact-pin threshold after a clear, so a neutral-owner
			// non-required write could later terminal against a current
			// revision. This mirrors the guard in
			// execVirtualFileMetadataUpdateResultUntx; a zero-row non-required
			// write is a plain no-op (the transaction rolls back).
			if !args.RequireAdopt || args.AdoptPath == "" {
				return VirtualFileMetadataUpdateResult{}, nil
			}
			if args.SameReleaseRematch {
				// A resolver-certified same-release renumber the fence could not
				// adopt: the row keeps its release and its pin simply cannot move
				// onto a path the catalog already assigns to a sibling of it. Not
				// counting it is what keeps a healthy row out of the terminal
				// failed verdict a duplicate-owned id used to march it to.
				slog.WarnContext(ctx, "virtual same-release rematch could not adopt its new candidate id; leaving the row's pin unchanged",
					"component", "api", "file_id", args.FileID, "adopt_path", args.AdoptPath,
					"expected_path", args.ExpectedFilePath,
					virtualEvidenceLogKeyReason, "same_release_rematch_not_adopted")
				// Nothing was written; the deferred rollback ends the
				// transaction. The refusal is deliberately not counted and the
				// terminal verdict is deliberately not installed.
				return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s was not adopted", errVirtualAdoptIdentityNotPersisted, args.AdoptPath)
			}
			return txVirtualAdoptionRefusal(ctx, tx, store, args, &committed)
		}
		if args.RequireAdopt && args.AdoptPath != "" && persistedPath != args.AdoptPath {
			// The metadata and stamp landed, but the validated identity did
			// not. Rolling back keeps the row free of a substitute's evidence.
			return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s persisted as %q", errVirtualAdoptIdentityNotPersisted, args.AdoptPath, persistedPath)
		}
		// The evidence write landed. Clear the exact-pin mark in the same
		// transaction: a cancel between the two is impossible, so a stale
		// threshold can never outlive a successful write.
		if err := store.ClearVirtualAdoptionRefusalTx(ctx, tx, args.FileID, args.ExpectedFilePath); err != nil {
			return VirtualFileMetadataUpdateResult{}, fmt.Errorf("clear virtual adoption refusal with evidence: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return VirtualFileMetadataUpdateResult{}, fmt.Errorf("commit virtual metadata transaction: %w", err)
		}
		committed = true
		sweepVirtualAdoptionRefusalsBestEffort(ctx, store)
		return VirtualFileMetadataUpdateResult{
			RowsAffected:    1,
			MetadataUpdated: true,
			IdentityAdopted: args.AdoptPath != "" && persistedPath == args.AdoptPath,
		}, nil
	}

	var pgErr *pgconn.PgError
	if args.AdoptPath == "" || !errors.As(writeErr, &pgErr) || pgErr.Code != "23505" {
		return VirtualFileMetadataUpdateResult{}, writeErr
	}
	if args.RequireAdopt {
		// A collision is proof the validated identity was not adopted. A
		// metadata-only retry would stamp the substitute's tracks and probe
		// evidence on a row that does not own them, so refuse outright.
		slog.WarnContext(ctx, "virtual probe evidence persist adoption collided with an existing path owner; refusing without writing tracks",
			"component", "api", "file_id", args.FileID, "adopt_path", args.AdoptPath, "error", writeErr)
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s collided with an existing path owner", errVirtualAdoptIdentityNotPersisted, args.AdoptPath)
	}
	if _, err := tx.Exec(ctx, rollbackVirtualAdoptSQL); err != nil {
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("rewind virtual adoption savepoint: %w", err)
	}
	slog.WarnContext(ctx, "virtual probe evidence persist adoption raced an existing path owner; retrying without adoption",
		"component", "api", "file_id", args.FileID, "adopt_path", args.AdoptPath, "error", writeErr)
	// The retry deliberately retains evidence on the current row, but it did
	// not adopt the requested identity. Callers must not treat this as an
	// adoption success (metadata-only retries are not identity proof).
	retryPath, retryErr := statement.run(ctx, tx, args, "")
	if retryErr != nil {
		return VirtualFileMetadataUpdateResult{}, retryErr
	}
	if retryPath == "" {
		return VirtualFileMetadataUpdateResult{}, nil
	}
	// The retry wrote fresh evidence on the current row. The row now carries
	// evidence, so any consecutive-refusal count for this row and pin is stale
	// and must be cleared in the same transaction.
	if err := store.ClearVirtualAdoptionRefusalTx(ctx, tx, args.FileID, args.ExpectedFilePath); err != nil {
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("clear virtual adoption refusal after metadata-only retry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("commit virtual metadata transaction: %w", err)
	}
	committed = true
	sweepVirtualAdoptionRefusalsBestEffort(ctx, store)
	return VirtualFileMetadataUpdateResult{
		RowsAffected:    1,
		MetadataUpdated: true,
		IdentityAdopted: false,
	}, nil
}

// txVirtualAdoptionRefusal reconciles a required adoption that matched no row:
// it records the consecutive-refusal increment, re-reads the count inside the
// transaction, and installs the terminal verdict when the bound is reached. All
// three commit together with the row lock it already holds. A store error rolls
// the increment back and is reported as a recoverable infrastructure failure,
// never as a refusal and never as a reached bound.
func txVirtualAdoptionRefusal(ctx context.Context, tx pgx.Tx, store VirtualAdoptionRefusalTxStore, args models.VirtualFilePersistArgs, committed *bool) (VirtualFileMetadataUpdateResult, error) {
	reason := args.RefusalReason
	if reason == "" {
		reason = virtualProbeRefusalUnclassified
	}
	refusals, recordErr := store.RecordVirtualAdoptionRefusalTx(ctx, tx, args.FileID, args.ExpectedFilePath, virtualAdoptionRefusalTTL)
	if recordErr != nil {
		slog.WarnContext(ctx, "virtual adoption refusal count was not recorded; treating the store error as recoverable, not as a refusal",
			"component", "api", "file_id", args.FileID, "expected_path", args.ExpectedFilePath, virtualEvidenceLogKeyError, recordErr)
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s was not adopted", errVirtualAdoptIdentityNotPersisted, args.AdoptPath)
	}
	if refusals >= virtualAdoptionRefusalBound {
		// The in-transaction count is the authority, not the caller's earlier
		// read. Name the disposition from that count and stamp the durable
		// terminal; the stamp is fenced on the observed evidence revision, so an
		// evidence write that advanced the row after this decision was taken
		// wins over the stale terminal.
		reason = virtualProbeRefusalAdoptionBoundExhausted
		stamped, stampErr := store.MarkVirtualAdoptionTerminalRefusalTx(ctx, tx, args.FileID, args.ExpectedFilePath, nil, args.UpdatedAt, args.ProbeUpdatedAt)
		if stampErr != nil {
			slog.WarnContext(ctx, "virtual adoption terminal refusal was not persisted; the row keeps its own failed_at state",
				"component", "api", "file_id", args.FileID, "expected_path", args.ExpectedFilePath,
				virtualEvidenceLogKeyReason, reason, virtualEvidenceLogKeyError, stampErr)
		} else if stamped {
			slog.ErrorContext(ctx, "virtual adoption terminal refusal persisted",
				"component", "api", "file_id", args.FileID, "candidate_path", args.ExpectedFilePath, virtualEvidenceLogKeyReason, reason)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("commit virtual metadata transaction: %w", err)
	}
	*committed = true
	sweepVirtualAdoptionRefusalsBestEffort(ctx, store)
	slog.WarnContext(ctx, "virtual probe evidence persist refused: required identity adoption matched no row",
		"component", "api", "file_id", args.FileID, "adopt_path", args.AdoptPath,
		"expected_path", args.ExpectedFilePath,
		virtualEvidenceLogKeyReason, reason, "consecutive_refusals", refusals,
		"refusal_bound", virtualAdoptionRefusalBound,
		"bound_reached", refusals >= virtualAdoptionRefusalBound)
	return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s was not adopted", errVirtualAdoptIdentityNotPersisted, args.AdoptPath)
}

// sweepVirtualAdoptionRefusalsBestEffort reclaims expired refusal marks after a
// transactional record commits. A store that advertises no sweep, or a sweep
// failure, changes nothing about the count just committed.
func sweepVirtualAdoptionRefusalsBestEffort(ctx context.Context, store VirtualAdoptionRefusalTxStore) {
	sweeper, ok := store.(VirtualAdoptionRefusalSweeper)
	if !ok {
		return
	}
	if err := sweeper.SweepVirtualAdoptionRefusals(context.WithoutCancel(ctx)); err != nil {
		slog.DebugContext(ctx, "virtual adoption refusal sweep failed", "component", "api", virtualEvidenceLogKeyError, err)
	}
}
