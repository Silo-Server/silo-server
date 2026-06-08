package secret

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Executor is the minimal database surface the backfill needs; *pgxpool.Pool
// satisfies it.
type Executor interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// BackfillTarget describes one literal-credential column to sweep from plaintext
// to ciphertext in place. All field values are compile-time constants (table,
// column, and SQL key expressions), never user input, so interpolating them into
// the statements is safe.
type BackfillTarget struct {
	// Table is the owning table, e.g. "subtitle_provider_config".
	Table string
	// Column is the secret column, e.g. "api_key".
	Column string
	// KeyExpr is the SQL expression selecting the row's stable update key, used
	// in the UPDATE ... WHERE. Defaults to "id" when empty. It is rendered to
	// text for comparison, e.g. "id::text" or "provider_name".
	KeyExpr string
	// AADExpr is the SQL expression producing the AAD pk part. Empty means use
	// KeyExpr. For tables whose token AAD binds to a business key rather than the
	// surrogate id (watch_provider_connections), this differs from KeyExpr.
	AADExpr string
}

func (t BackfillTarget) keyExpr() string {
	if t.KeyExpr == "" {
		return "id"
	}
	return t.KeyExpr
}

func (t BackfillTarget) aadExpr() string {
	if t.AADExpr == "" {
		return t.keyExpr()
	}
	return t.AADExpr
}

// ColumnBackfillTargets is the set of literal-credential columns the startup
// backfill encrypts in place. The two arr api_key_ref columns are NOT here: they
// may hold a legacy server_settings reference rather than a literal key and need
// the separate resolve-then-encrypt path. The AAD expressions must match the
// repos' read-path AAD (secret.RowAAD with the same id string) exactly, or reads
// would fail to decrypt.
func ColumnBackfillTargets() []BackfillTarget {
	const watchTupleAAD = "provider || ':' || user_id::text || ':' || profile_id"
	return []BackfillTarget{
		// Subtitles — AAD keyed by provider_name.
		{Table: "subtitle_provider_config", Column: "api_key", KeyExpr: "provider_name"},
		{Table: "subtitle_provider_config", Column: "password", KeyExpr: "provider_name"},
		// Watch-sync — AAD keyed by the (provider, user_id, profile_id) tuple to
		// match watchsync.TokenAAD (the id is DB-assigned on upsert).
		{Table: "watch_provider_connections", Column: "access_token", KeyExpr: "id::text", AADExpr: watchTupleAAD},
		{Table: "watch_provider_connections", Column: "refresh_token", KeyExpr: "id::text", AADExpr: watchTupleAAD},
		// Webhook-sync — only access_token (webhook_secret is equality-looked-up).
		{Table: "webhook_sync_connections", Column: "access_token", KeyExpr: "id::text"},
		// History import — admin token + session tokens, keyed by row id.
		{Table: "history_import_sources", Column: "admin_token", KeyExpr: "id::text"},
		{Table: "history_import_connect_sessions", Column: "connect_access_token", KeyExpr: "id::text"},
		{Table: "history_import_plex_sessions", Column: "auth_token", KeyExpr: "id::text"},
		// Jellyfin-compat sessions — bridged Silo access/refresh tokens, keyed by
		// the session token (the table's primary key; itself left in plaintext).
		{Table: "jellycompat_sessions", Column: "streamapp_access_token", KeyExpr: "token"},
		{Table: "jellycompat_sessions", Column: "streamapp_refresh_token", KeyExpr: "token"},
	}
}

// BackfillColumns encrypts every plaintext value across the given targets in
// place. It is idempotent (rows already carrying an enc:v1: envelope are skipped
// by the SELECT filter, and the UPDATE's "AND col = original" guard makes
// concurrent multi-node boots converge without double-encrypting) and
// best-effort: per-row and per-target failures are collected and returned but
// never abort the sweep. Returns the count of newly encrypted rows.
func BackfillColumns(ctx context.Context, db Executor, cipher *Cipher, targets []BackfillTarget) (int, error) {
	total := 0
	var errs []error
	for _, t := range targets {
		n, err := backfillTarget(ctx, db, cipher, t)
		total += n
		if err != nil {
			errs = append(errs, fmt.Errorf("%s.%s: %w", t.Table, t.Column, err))
		}
	}
	return total, errors.Join(errs...)
}

// candidate is one plaintext row to encrypt.
type candidate struct {
	key   string // update WHERE key (rendered text)
	aadID string // AAD pk part
	value string // current plaintext value
}

func backfillTarget(ctx context.Context, db Executor, cipher *Cipher, t BackfillTarget) (int, error) {
	// Select candidates first (can't UPDATE while iterating the same rows).
	selectSQL := fmt.Sprintf(
		`SELECT %s, %s, %s FROM %s WHERE %s IS NOT NULL AND %s <> '' AND %s NOT LIKE 'enc:v1:%%'`,
		t.keyExpr(), t.aadExpr(), t.Column, t.Table, t.Column, t.Column, t.Column,
	)
	rows, err := db.Query(ctx, selectSQL)
	if err != nil {
		return 0, fmt.Errorf("select candidates: %w", err)
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.key, &c.aadID, &c.value); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate candidates: %w", err)
	}

	updateSQL := fmt.Sprintf(
		`UPDATE %s SET %s = $1 WHERE %s = $2 AND %s = $3`,
		t.Table, t.Column, t.keyExpr(), t.Column,
	)
	count := 0
	var errs []error
	for _, c := range candidates {
		ct, changed, err := cipher.EncryptIfPlaintext(c.value, RowAAD(t.Table, t.Column, c.aadID))
		if err != nil {
			errs = append(errs, fmt.Errorf("encrypt key=%s: %w", c.key, err))
			continue
		}
		if !changed {
			continue
		}
		tag, err := db.Exec(ctx, updateSQL, ct, c.key, c.value)
		if err != nil {
			errs = append(errs, fmt.Errorf("update key=%s: %w", c.key, err))
			continue
		}
		if tag.RowsAffected() > 0 {
			count++
		}
	}
	return count, errors.Join(errs...)
}

// ReferenceResolver resolves a legacy api_key_ref value that may be a
// server_settings key name into the real credential. It returns "" when the
// value is not a known reference (i.e. the row already holds a literal key). In
// production this is the EncryptedSettingsRepo's Get, which decrypts a sensitive
// target and passes through a plaintext custom key.
type ReferenceResolver func(ctx context.Context, ref string) (string, error)

// ArrKeyBackfillTargets are the two arr api_key_ref columns that need the
// resolve-then-encrypt path (their legacy rows may reference server_settings).
func ArrKeyBackfillTargets() []BackfillTarget {
	return []BackfillTarget{
		{Table: "request_integrations", Column: "api_key_ref", KeyExpr: "id::text"},
		{Table: "autoscan_connections", Column: "api_key_ref", KeyExpr: "id::text"},
	}
}

// BackfillReferencedColumns encrypts the arr api_key_ref columns, first
// collapsing any legacy server_settings reference to the real credential. For
// each not-yet-encrypted row it calls resolve(value): a non-empty result means
// the row held a reference (encrypt the resolved credential), otherwise the row
// held a literal key (encrypt the value). Run this AFTER the sensitive-settings
// backfill so referenced settings are already consistent. Idempotent and
// best-effort. Returns the count of newly encrypted rows.
func BackfillReferencedColumns(ctx context.Context, db Executor, cipher *Cipher, resolve ReferenceResolver, targets []BackfillTarget) (int, error) {
	total := 0
	var errs []error
	for _, t := range targets {
		n, err := backfillReferencedTarget(ctx, db, cipher, resolve, t)
		total += n
		if err != nil {
			errs = append(errs, fmt.Errorf("%s.%s: %w", t.Table, t.Column, err))
		}
	}
	return total, errors.Join(errs...)
}

func backfillReferencedTarget(ctx context.Context, db Executor, cipher *Cipher, resolve ReferenceResolver, t BackfillTarget) (int, error) {
	selectSQL := fmt.Sprintf(
		`SELECT %s, %s FROM %s WHERE %s IS NOT NULL AND %s <> '' AND %s NOT LIKE 'enc:v1:%%'`,
		t.keyExpr(), t.Column, t.Table, t.Column, t.Column, t.Column,
	)
	rows, err := db.Query(ctx, selectSQL)
	if err != nil {
		return 0, fmt.Errorf("select candidates: %w", err)
	}
	type rec struct{ key, value string }
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.key, &r.value); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan candidate: %w", err)
		}
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate candidates: %w", err)
	}

	updateSQL := fmt.Sprintf(
		`UPDATE %s SET %s = $1 WHERE %s = $2 AND %s = $3`,
		t.Table, t.Column, t.keyExpr(), t.Column,
	)
	count := 0
	var errs []error
	for _, r := range recs {
		credential := r.value
		if resolved, err := resolve(ctx, r.value); err != nil {
			errs = append(errs, fmt.Errorf("resolve key=%s: %w", r.key, err))
			continue
		} else if resolved != "" {
			credential = resolved // the row held a reference, not a literal key
		}
		ct, err := cipher.Encrypt(credential, RowAAD(t.Table, t.Column, r.key))
		if err != nil {
			errs = append(errs, fmt.Errorf("encrypt key=%s: %w", r.key, err))
			continue
		}
		tag, err := db.Exec(ctx, updateSQL, ct, r.key, r.value)
		if err != nil {
			errs = append(errs, fmt.Errorf("update key=%s: %w", r.key, err))
			continue
		}
		if tag.RowsAffected() > 0 {
			count++
		}
	}
	return count, errors.Join(errs...)
}
