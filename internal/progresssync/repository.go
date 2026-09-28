package progresssync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool         *pgxpool.Pool
	installation string
	visibility   Visibility
}

func New(pool *pgxpool.Pool, installation string, visibility Visibility) (*Repository, error) {
	if pool == nil || installation == "" || visibility == nil {
		return nil, ErrInvalid
	}
	return &Repository{pool: pool, installation: installation, visibility: visibility}, nil
}

// RotateGeneration joins the importer's transaction. Imported state, generation,
// and receipt must commit together. Receipt replay must not call this function.
func RotateGeneration(ctx context.Context, tx pgx.Tx, userID int) (string, error) {
	if userID <= 0 {
		return "", ErrInvalid
	}
	generation := uuid.NewString()
	_, err := tx.Exec(ctx, `INSERT INTO user_progress_sync_state(user_id,generation) VALUES($1,$2)
 ON CONFLICT(user_id) DO UPDATE SET generation=excluded.generation,admission_revision=user_progress_sync_state.admission_revision+1`, userID, generation)
	if err != nil {
		return "", err
	}
	// Invalidated snapshots must not consume the new generation's admission
	// quota. Keep their metadata so an old request cannot become a new intent.
	_, err = tx.Exec(ctx, `UPDATE progress_bootstrap_snapshots SET expires_at=LEAST(expires_at,clock_timestamp()) WHERE user_id=$1`, userID)
	return generation, err
}

const snapshotColumns = `id::text,user_id,profile_id,request_id::text,installation_id,generation::text,access_digest,page_size,captured_at,expires_at,item_count,byte_count`

func scanSnapshot(row pgx.Row) (Snapshot, error) {
	var s Snapshot
	err := row.Scan(&s.ID, &s.UserID, &s.ProfileID, &s.RequestID, &s.InstallationID, &s.Generation, &s.AccessDigest, &s.PageSize, &s.CapturedAt, &s.ExpiresAt, &s.ItemCount, &s.ByteCount)
	return s, err
}
func validIdentity(id Identity) bool { return id.UserID > 0 && id.ProfileID != "" }

// Create synchronously commits a bounded snapshot or returns no partial result.
// Repeating requestID replays the same first page without extending its lifetime.
func (r *Repository) Create(ctx context.Context, id Identity, requestID string, pageSize int) (Page, error) {
	if !validIdentity(id) || pageSize < 1 || pageSize > MaxPageSize {
		return Page{}, ErrInvalid
	}
	parsed, err := uuid.Parse(requestID)
	if err != nil {
		return Page{}, ErrInvalid
	}
	requestID = parsed.String()
	ctx, cancel := context.WithTimeout(ctx, AdmissionTimeout)
	defer cancel()
	for {
		page, err := r.create(ctx, id, requestID, pageSize)
		if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok && pgerr.Code == "40001" && ctx.Err() == nil {
			continue
		}
		return page, err
	}
}
func (r *Repository) create(ctx context.Context, id Identity, requestID string, pageSize int) (Page, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The write is intentional: a waiter with an older RR snapshot must restart,
	// otherwise it could admit against a stale active-session count.
	var generation string
	err = tx.QueryRow(ctx, `INSERT INTO user_progress_sync_state(user_id,generation) VALUES($1,$2)
 ON CONFLICT(user_id) DO UPDATE SET admission_revision=user_progress_sync_state.admission_revision+1 RETURNING generation::text`, id.UserID, uuid.NewString()).Scan(&generation)
	if err != nil {
		return Page{}, err
	}
	digest, _, err := r.visibility(ctx, tx, id, nil)
	if err != nil {
		return Page{}, err
	}
	if digest == "" {
		return Page{}, ErrInvalid
	}
	existing, err := scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM progress_bootstrap_snapshots WHERE user_id=$1 AND profile_id=$2 AND request_id=$3`, id.UserID, id.ProfileID, requestID))
	if err == nil {
		if existing.PageSize != pageSize {
			return Page{}, ErrRequestConflict
		}
		page, err := r.readPage(ctx, tx, existing, id, generation, digest, 0)
		if err != nil {
			return Page{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return Page{}, err
		}
		return page, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Page{}, err
	}
	var active int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM progress_bootstrap_snapshots WHERE user_id=$1 AND expires_at>clock_timestamp()`, id.UserID).Scan(&active)
	if err != nil {
		return Page{}, err
	}
	if active >= MaxActiveSnapshots {
		return Page{}, ErrQuota
	}
	s, err := scanSnapshot(tx.QueryRow(ctx, `INSERT INTO progress_bootstrap_snapshots(id,user_id,profile_id,request_id,installation_id,generation,access_digest,page_size,captured_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,transaction_timestamp(),clock_timestamp()+interval '15 minutes') RETURNING `+snapshotColumns, uuid.NewString(), id.UserID, id.ProfileID, requestID, r.installation, generation, digest, pageSize))
	if err != nil {
		return Page{}, err
	}
	if err = r.materialize(ctx, tx, &s); err != nil {
		return Page{}, err
	}
	page, err := r.readPage(ctx, tx, s, id, generation, digest, 0)
	if err != nil {
		return Page{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Page{}, err
	}
	return page, nil
}
func (r *Repository) materialize(ctx context.Context, tx pgx.Tx, s *Snapshot) error {
	after := ""
	for {
		rows, err := tx.Query(ctx, `SELECT p.media_item_id,p.position_seconds,p.duration_seconds,p.completed,p.updated_at FROM user_watch_progress p
 WHERE p.user_id=$1 AND p.profile_id=$2 AND p.media_item_id>$3 AND NOT EXISTS(SELECT 1 FROM user_history_hidden_items h WHERE h.user_id=p.user_id AND h.profile_id=p.profile_id AND h.media_item_id=p.media_item_id AND p.updated_at<=h.hidden_before)
 ORDER BY p.media_item_id LIMIT 200`, s.UserID, s.ProfileID, after)
		if err != nil {
			return err
		}
		batch := []Entry{}
		ids := []string{}
		for rows.Next() {
			var entry Entry
			if err = rows.Scan(&entry.MediaItemID, &entry.PositionSeconds, &entry.DurationSeconds, &entry.Completed, &entry.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, entry)
			ids = append(ids, entry.MediaItemID)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		digest, visible, err := r.visibility(ctx, tx, s.Identity, ids)
		if err != nil {
			return err
		}
		if digest != s.AccessDigest {
			return ErrResetRequired
		}
		start := s.ItemCount
		values := make([][]any, 0, len(batch))
		for _, entry := range batch {
			if !visible[entry.MediaItemID] {
				continue
			}
			s.ItemCount++
			if s.ItemCount > MaxSnapshotItems {
				return ErrTooLarge
			}
			payload, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("encode progress: %w", err)
			}
			values = append(values, []any{s.ID, s.ItemCount, json.RawMessage(payload)})
		}
		if len(values) > 0 {
			if _, err = tx.CopyFrom(ctx, pgx.Identifier{"progress_bootstrap_items"}, []string{"snapshot_id", "ordinal", "payload"}, pgx.CopyFromRows(values)); err != nil {
				return err
			}
			// Count the normalized JSON payload actually retained, not Go struct sizes
			// or compressed physical storage (which depends on database settings).
			var size int64
			if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(octet_length(payload::text)),0) FROM progress_bootstrap_items WHERE snapshot_id=$1 AND ordinal>$2`, s.ID, start).Scan(&size); err != nil {
				return err
			}
			s.ByteCount += size
			if s.ByteCount > MaxSnapshotBytes {
				return ErrTooLarge
			}
		}
		after = batch[len(batch)-1].MediaItemID
	}
	_, err := tx.Exec(ctx, `UPDATE progress_bootstrap_snapshots SET item_count=$2,byte_count=$3 WHERE id=$1`, s.ID, s.ItemCount, s.ByteCount)
	return err
}

func (r *Repository) Read(ctx context.Context, id Identity, pos Position) (Page, error) {
	if !validIdentity(id) || pos.Identity != id || pos.After < 0 || pos.PageSize < 1 || pos.PageSize > MaxPageSize {
		return Page{}, ErrInvalid
	}
	if _, err := uuid.Parse(pos.SnapshotID); err != nil {
		return Page{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, AdmissionTimeout)
	defer cancel()
	for {
		page, err := r.read(ctx, id, pos)
		if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok && pgerr.Code == "40001" && ctx.Err() == nil {
			continue
		}
		return page, err
	}
}
func (r *Repository) read(ctx context.Context, id Identity, pos Position) (Page, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var generation string
	err = tx.QueryRow(ctx, `SELECT generation::text FROM user_progress_sync_state WHERE user_id=$1 FOR SHARE`, id.UserID).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return Page{}, ErrNotFound
	}
	if err != nil {
		return Page{}, err
	}
	s, err := scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM progress_bootstrap_snapshots WHERE id=$1 AND user_id=$2 AND profile_id=$3`, pos.SnapshotID, id.UserID, id.ProfileID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Page{}, ErrNotFound
	}
	if err != nil {
		return Page{}, err
	}
	if s.InstallationID != pos.InstallationID || s.Generation != pos.Generation || s.AccessDigest != pos.AccessDigest || s.PageSize != pos.PageSize || pos.After > s.ItemCount {
		return Page{}, ErrResetRequired
	}
	digest, _, err := r.visibility(ctx, tx, id, nil)
	if err != nil {
		return Page{}, err
	}
	page, err := r.readPage(ctx, tx, s, id, generation, digest, pos.After)
	if err != nil {
		return Page{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Page{}, err
	}
	return page, nil
}
func (r *Repository) readPage(ctx context.Context, tx pgx.Tx, s Snapshot, id Identity, generation, digest string, after int) (Page, error) {
	var expired bool
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, s.ExpiresAt).Scan(&expired); err != nil {
		return Page{}, err
	}
	if expired || s.InstallationID != r.installation || s.Generation != generation || s.AccessDigest != digest {
		return Page{}, ErrResetRequired
	}
	rows, err := tx.Query(ctx, `SELECT payload FROM progress_bootstrap_items WHERE snapshot_id=$1 AND ordinal>$2 ORDER BY ordinal LIMIT $3`, s.ID, after, s.PageSize)
	if err != nil {
		return Page{}, err
	}
	page := Page{Snapshot: s, Items: []Entry{}}
	ids := []string{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return Page{}, err
		}
		var entry Entry
		if err = json.Unmarshal(raw, &entry); err != nil {
			rows.Close()
			return Page{}, err
		}
		page.Items = append(page.Items, entry)
		ids = append(ids, entry.MediaItemID)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) != min(s.PageSize, s.ItemCount-after) {
		return Page{}, ErrResetRequired
	}
	current, visible, err := r.visibility(ctx, tx, id, ids)
	if err != nil {
		return Page{}, err
	}
	if current != digest {
		return Page{}, ErrResetRequired
	}
	for _, item := range ids {
		if !visible[item] {
			return Page{}, ErrResetRequired
		}
	}
	if end := after + len(page.Items); end < s.ItemCount {
		page.Next = &Position{SnapshotID: s.ID, InstallationID: s.InstallationID, Generation: s.Generation, AccessDigest: s.AccessDigest, Identity: id, PageSize: s.PageSize, After: end}
	}
	return page, nil
}

// Cleanup enforces no authority decision; readers and admission check expiry even
// if this idempotent maintenance call has not run on any node yet.
func (r *Repository) Cleanup(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM progress_bootstrap_items i USING progress_bootstrap_snapshots s WHERE i.snapshot_id=s.id AND s.expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM progress_bootstrap_snapshots WHERE expires_at<=clock_timestamp()-interval '24 hours'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
