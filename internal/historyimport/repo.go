package historyimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

var (
	ErrSourceNotFound         = errors.New("history import source not found")
	ErrRunNotFound            = errors.New("history import run not found")
	ErrProfileNotFound        = errors.New("profile not found")
	ErrConnectSessionNotFound = errors.New("history import connect session not found")
	ErrConnectSessionExpired  = errors.New("history import connect session expired")
	ErrConnectSessionUsed     = errors.New("history import connect session already used")
	ErrPlexSessionNotFound    = errors.New("plex session not found")
	ErrPlexSessionExpired     = errors.New("plex session expired")
	ErrPlexSessionUsed        = errors.New("plex session already used")
)

type Repository struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

func NewRepository(pool *pgxpool.Pool, cipher *secret.Cipher) *Repository {
	return &Repository{pool: pool, cipher: cipher}
}

// AAD builders binding each history-import secret column to its row id.
func sourceAdminTokenAAD(sourceID int) string {
	return secret.RowAAD("history_import_sources", "admin_token", strconv.Itoa(sourceID))
}
func connectServerAccessKeyAAD(sessionID string, index int, server ConnectServer) string {
	return secret.RowAAD("history_import_connect_sessions", "servers_json.access_key", sessionID+":"+strconv.Itoa(index)+":"+server.ID)
}
func connectSessionTokenAAD(sessionID string) string {
	return secret.RowAAD("history_import_connect_sessions", "connect_access_token", sessionID)
}
func plexServerAccessTokenAAD(sessionID string, index int, server PlexServer) string {
	return secret.RowAAD("history_import_plex_sessions", "servers_json.access_token", sessionID+":"+strconv.Itoa(index)+":"+server.ClientIdentifier)
}
func plexSessionTokenAAD(sessionID string) string {
	return secret.RowAAD("history_import_plex_sessions", "auth_token", sessionID)
}

func (r *Repository) encryptSourceAdminToken(sourceID int, token string) (string, error) {
	return r.cipher.Encrypt(token, sourceAdminTokenAAD(sourceID))
}

func (r *Repository) encryptConnectServers(sessionID string, servers []ConnectServer) ([]ConnectServer, error) {
	out := append([]ConnectServer(nil), servers...)
	for i := range out {
		if out[i].AccessKey == "" {
			continue
		}
		accessKey, err := r.cipher.Encrypt(out[i].AccessKey, connectServerAccessKeyAAD(sessionID, i, out[i]))
		if err != nil {
			return nil, fmt.Errorf("encrypt connect server access key: %w", err)
		}
		out[i].AccessKey = accessKey
	}
	return out, nil
}

func (r *Repository) encryptPlaintextConnectServers(sessionID string, servers []ConnectServer) ([]ConnectServer, bool, error) {
	out := append([]ConnectServer(nil), servers...)
	changed := false
	for i := range out {
		accessKey, didChange, err := r.cipher.EncryptIfPlaintext(out[i].AccessKey, connectServerAccessKeyAAD(sessionID, i, out[i]))
		if err != nil {
			return nil, false, fmt.Errorf("encrypt connect server access key: %w", err)
		}
		if didChange {
			out[i].AccessKey = accessKey
			changed = true
		}
	}
	return out, changed, nil
}

func (r *Repository) decryptConnectServers(sessionID string, servers []ConnectServer) ([]ConnectServer, error) {
	out := append([]ConnectServer(nil), servers...)
	for i := range out {
		accessKey, err := r.cipher.DecryptIfEncrypted(out[i].AccessKey, connectServerAccessKeyAAD(sessionID, i, out[i]))
		if err != nil {
			return nil, fmt.Errorf("decrypt connect server access key: %w", err)
		}
		out[i].AccessKey = accessKey
	}
	return out, nil
}

func (r *Repository) marshalConnectServersForWrite(sessionID string, servers []ConnectServer) ([]byte, error) {
	encrypted, err := r.encryptConnectServers(sessionID, servers)
	if err != nil {
		return nil, err
	}
	return json.Marshal(encrypted)
}

func (r *Repository) encryptPlexServers(sessionID string, servers []PlexServer) ([]PlexServer, error) {
	out := append([]PlexServer(nil), servers...)
	for i := range out {
		if out[i].AccessToken == "" {
			continue
		}
		accessToken, err := r.cipher.Encrypt(out[i].AccessToken, plexServerAccessTokenAAD(sessionID, i, out[i]))
		if err != nil {
			return nil, fmt.Errorf("encrypt plex server access token: %w", err)
		}
		out[i].AccessToken = accessToken
	}
	return out, nil
}

func (r *Repository) encryptPlaintextPlexServers(sessionID string, servers []PlexServer) ([]PlexServer, bool, error) {
	out := append([]PlexServer(nil), servers...)
	changed := false
	for i := range out {
		accessToken, didChange, err := r.cipher.EncryptIfPlaintext(out[i].AccessToken, plexServerAccessTokenAAD(sessionID, i, out[i]))
		if err != nil {
			return nil, false, fmt.Errorf("encrypt plex server access token: %w", err)
		}
		if didChange {
			out[i].AccessToken = accessToken
			changed = true
		}
	}
	return out, changed, nil
}

func (r *Repository) decryptPlexServers(sessionID string, servers []PlexServer) ([]PlexServer, error) {
	out := append([]PlexServer(nil), servers...)
	for i := range out {
		accessToken, err := r.cipher.DecryptIfEncrypted(out[i].AccessToken, plexServerAccessTokenAAD(sessionID, i, out[i]))
		if err != nil {
			return nil, fmt.Errorf("decrypt plex server access token: %w", err)
		}
		out[i].AccessToken = accessToken
	}
	return out, nil
}

func (r *Repository) marshalPlexServersForWrite(sessionID string, servers []PlexServer) ([]byte, error) {
	encrypted, err := r.encryptPlexServers(sessionID, servers)
	if err != nil {
		return nil, err
	}
	return json.Marshal(encrypted)
}

func (r *Repository) ListEnabledSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, source_type, '' AS base_url, COALESCE(system_id, ''), enabled, sort_order,
		       (admin_token IS NOT NULL) AS has_admin_token,
		       created_at, updated_at
		FROM history_import_sources
		WHERE enabled = TRUE
		ORDER BY sort_order ASC, name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing enabled history import sources: %w", err)
	}
	defer rows.Close()
	return scanSources(rows)
}

func (r *Repository) ListAdminSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, source_type, base_url, COALESCE(system_id, ''), enabled, sort_order,
		       (admin_token IS NOT NULL) AS has_admin_token,
		       created_at, updated_at
		FROM history_import_sources
		ORDER BY sort_order ASC, name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing admin history import sources: %w", err)
	}
	defer rows.Close()
	return scanSources(rows)
}

func (r *Repository) GetSourceByID(ctx context.Context, id int) (*Source, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, source_type, base_url, COALESCE(system_id, ''), enabled, sort_order,
		       (admin_token IS NOT NULL) AS has_admin_token,
		       created_at, updated_at
		FROM history_import_sources
		WHERE id = $1`, id)
	source, err := scanSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting history import source %d: %w", id, err)
	}
	return source, nil
}

func (r *Repository) CreateSource(ctx context.Context, input CreateSourceInput) (*Source, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin creating history import source: %w", err)
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		INSERT INTO history_import_sources (name, source_type, base_url, system_id, enabled, sort_order)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6)
		RETURNING id, name, source_type, base_url, COALESCE(system_id, ''), enabled, sort_order,
		          (admin_token IS NOT NULL) AS has_admin_token,
		          created_at, updated_at`,
		input.Name, input.SourceType, input.BaseURL, input.SystemID, input.Enabled, input.SortOrder,
	)
	source, err := scanSource(row)
	if err != nil {
		return nil, fmt.Errorf("creating history import source: %w", err)
	}
	if input.AdminToken != "" {
		encryptedToken, err := r.encryptSourceAdminToken(source.ID, input.AdminToken)
		if err != nil {
			return nil, fmt.Errorf("encrypt admin token for source %d: %w", source.ID, err)
		}
		result, err := tx.Exec(ctx, `
			UPDATE history_import_sources
			SET admin_token = $2, updated_at = NOW()
			WHERE id = $1`, source.ID, encryptedToken)
		if err != nil {
			return nil, fmt.Errorf("setting admin token for source %d: %w", source.ID, err)
		}
		if result.RowsAffected() == 0 {
			return nil, ErrSourceNotFound
		}
		source.HasAdminToken = true
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit creating history import source: %w", err)
	}
	return source, nil
}

func (r *Repository) UpdateSource(ctx context.Context, id int, input UpdateSourceInput) (*Source, error) {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_sources
		SET
			name = COALESCE($2::text, name),
			base_url = COALESCE($3::text, base_url),
			system_id = CASE
				WHEN $4::text IS NULL THEN system_id
				WHEN $4::text = '' THEN NULL
				ELSE $4::text
			END,
			enabled = COALESCE($5::boolean, enabled),
			sort_order = COALESCE($6::integer, sort_order),
			updated_at = NOW()
		WHERE id = $1`,
		id, input.Name, input.BaseURL, input.SystemID, input.Enabled, input.SortOrder,
	)
	if err != nil {
		return nil, fmt.Errorf("updating history import source %d: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return nil, ErrSourceNotFound
	}
	return r.GetSourceByID(ctx, id)
}

func (r *Repository) DeleteSource(ctx context.Context, id int) error {
	result, err := r.pool.Exec(ctx, `DELETE FROM history_import_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting history import source %d: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return ErrSourceNotFound
	}
	return nil
}

func (r *Repository) CreateConnectSession(ctx context.Context, session ConnectSession) (*ConnectSession, error) {
	serversJSON, err := r.marshalConnectServersForWrite(session.ID, session.Servers)
	if err != nil {
		return nil, fmt.Errorf("marshaling connect servers: %w", err)
	}
	accessToken, err := r.cipher.Encrypt(session.ConnectAccessToken, connectSessionTokenAAD(session.ID))
	if err != nil {
		return nil, fmt.Errorf("encrypt connect access token: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO history_import_connect_sessions (
			id, user_id, connect_user_id, connect_access_token, servers_json, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, user_id, connect_user_id, connect_access_token, servers_json, expires_at, consumed_at, created_at, updated_at`,
		session.ID, session.UserID, session.ConnectUserID, accessToken, serversJSON, session.ExpiresAt,
	)
	stored, err := r.scanConnectSession(row)
	if err != nil {
		return nil, fmt.Errorf("creating connect session: %w", err)
	}
	return stored, nil
}

func (r *Repository) GetConnectSession(ctx context.Context, userID int, sessionID string) (*ConnectSession, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, connect_user_id, connect_access_token, servers_json, expires_at, consumed_at, created_at, updated_at
		FROM history_import_connect_sessions
		WHERE id = $1 AND user_id = $2`, sessionID, userID)
	session, err := r.scanConnectSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConnectSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting connect session %s: %w", sessionID, err)
	}
	if session.ExpiresAt.Before(time.Now().UTC()) {
		return nil, ErrConnectSessionExpired
	}
	if session.ConsumedAt != nil {
		return nil, ErrConnectSessionUsed
	}
	return session, nil
}

func (r *Repository) ConsumeConnectSession(ctx context.Context, sessionID string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_connect_sessions
		SET consumed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND consumed_at IS NULL`, sessionID)
	if err != nil {
		return fmt.Errorf("consuming connect session %s: %w", sessionID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrConnectSessionNotFound
	}
	return nil
}

func (r *Repository) DeleteExpiredConnectSessions(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM history_import_connect_sessions
		WHERE expires_at < NOW() OR consumed_at IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("deleting expired connect sessions: %w", err)
	}
	return nil
}

func (r *Repository) CreatePlexSession(ctx context.Context, session PlexSession) (*PlexSession, error) {
	serversJSON, err := r.marshalPlexServersForWrite(session.ID, session.Servers)
	if err != nil {
		return nil, fmt.Errorf("marshaling plex servers: %w", err)
	}
	authToken, err := r.cipher.Encrypt(session.AuthToken, plexSessionTokenAAD(session.ID))
	if err != nil {
		return nil, fmt.Errorf("encrypt plex auth token: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO history_import_plex_sessions (
			id, user_id, pin_id, pin_code, auth_token, servers_json, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, user_id, pin_id, pin_code, auth_token, servers_json, expires_at, consumed_at, created_at, updated_at`,
		session.ID, session.UserID, session.PinID, session.PinCode, nilIfEmpty(authToken), serversJSON, session.ExpiresAt,
	)
	stored, err := r.scanPlexSession(row)
	if err != nil {
		return nil, fmt.Errorf("creating plex session: %w", err)
	}
	return stored, nil
}

func (r *Repository) GetPlexSession(ctx context.Context, userID int, sessionID string) (*PlexSession, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, pin_id, pin_code, COALESCE(auth_token, ''), servers_json, expires_at, consumed_at, created_at, updated_at
		FROM history_import_plex_sessions
		WHERE id = $1 AND user_id = $2`, sessionID, userID)
	session, err := r.scanPlexSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPlexSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting plex session %s: %w", sessionID, err)
	}
	if session.ExpiresAt.Before(time.Now().UTC()) {
		return nil, ErrPlexSessionExpired
	}
	if session.ConsumedAt != nil {
		return nil, ErrPlexSessionUsed
	}
	return session, nil
}

func (r *Repository) UpdatePlexSessionAuth(ctx context.Context, sessionID, authToken string, servers []PlexServer) error {
	serversJSON, err := r.marshalPlexServersForWrite(sessionID, servers)
	if err != nil {
		return fmt.Errorf("marshaling plex servers: %w", err)
	}
	encryptedToken, err := r.cipher.Encrypt(authToken, plexSessionTokenAAD(sessionID))
	if err != nil {
		return fmt.Errorf("encrypt plex auth token: %w", err)
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_plex_sessions
		SET auth_token = $2, servers_json = $3, updated_at = NOW()
		WHERE id = $1`, sessionID, encryptedToken, serversJSON)
	if err != nil {
		return fmt.Errorf("updating plex session auth %s: %w", sessionID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrPlexSessionNotFound
	}
	return nil
}

func (r *Repository) ConsumePlexSession(ctx context.Context, sessionID string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_plex_sessions
		SET consumed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND consumed_at IS NULL`, sessionID)
	if err != nil {
		return fmt.Errorf("consuming plex session %s: %w", sessionID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrPlexSessionNotFound
	}
	return nil
}

func (r *Repository) DeleteExpiredPlexSessions(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM history_import_plex_sessions
		WHERE expires_at < NOW() OR consumed_at IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("deleting expired plex sessions: %w", err)
	}
	return nil
}

// BackfillSessionServerSecrets encrypts credentials nested inside history-import
// session server JSON. The top-level session token columns are handled by the
// generic column backfill, but Emby Connect access keys and Plex per-server
// access tokens live inside servers_json and need a typed JSON pass.
func (r *Repository) BackfillSessionServerSecrets(ctx context.Context) (int, error) {
	connectN, connectErr := r.backfillConnectSessionServerSecrets(ctx)
	plexN, plexErr := r.backfillPlexSessionServerSecrets(ctx)
	return connectN + plexN, errors.Join(connectErr, plexErr)
}

func (r *Repository) backfillConnectSessionServerSecrets(ctx context.Context) (int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, servers_json::text
		FROM history_import_connect_sessions
		WHERE servers_json <> '[]'::jsonb`)
	if err != nil {
		return 0, fmt.Errorf("select connect sessions: %w", err)
	}
	type rec struct{ id, raw string }
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan connect session: %w", err)
		}
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate connect sessions: %w", err)
	}

	count := 0
	var errs []error
	for _, rec := range recs {
		var servers []ConnectServer
		if err := json.Unmarshal([]byte(rec.raw), &servers); err != nil {
			errs = append(errs, fmt.Errorf("decode connect session %s servers: %w", rec.id, err))
			continue
		}
		encrypted, changed, err := r.encryptPlaintextConnectServers(rec.id, servers)
		if err != nil {
			errs = append(errs, fmt.Errorf("encrypt connect session %s servers: %w", rec.id, err))
			continue
		}
		if !changed {
			continue
		}
		body, err := json.Marshal(encrypted)
		if err != nil {
			errs = append(errs, fmt.Errorf("encode connect session %s servers: %w", rec.id, err))
			continue
		}
		tag, err := r.pool.Exec(ctx, `
			UPDATE history_import_connect_sessions
			SET servers_json = $2::jsonb, updated_at = NOW()
			WHERE id = $1 AND servers_json = $3::jsonb`,
			rec.id, string(body), rec.raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("update connect session %s servers: %w", rec.id, err))
			continue
		}
		if tag.RowsAffected() > 0 {
			count++
		}
	}
	return count, errors.Join(errs...)
}

func (r *Repository) backfillPlexSessionServerSecrets(ctx context.Context) (int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, servers_json::text
		FROM history_import_plex_sessions
		WHERE servers_json <> '[]'::jsonb`)
	if err != nil {
		return 0, fmt.Errorf("select plex sessions: %w", err)
	}
	type rec struct{ id, raw string }
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan plex session: %w", err)
		}
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate plex sessions: %w", err)
	}

	count := 0
	var errs []error
	for _, rec := range recs {
		var servers []PlexServer
		if err := json.Unmarshal([]byte(rec.raw), &servers); err != nil {
			errs = append(errs, fmt.Errorf("decode plex session %s servers: %w", rec.id, err))
			continue
		}
		encrypted, changed, err := r.encryptPlaintextPlexServers(rec.id, servers)
		if err != nil {
			errs = append(errs, fmt.Errorf("encrypt plex session %s servers: %w", rec.id, err))
			continue
		}
		if !changed {
			continue
		}
		body, err := json.Marshal(encrypted)
		if err != nil {
			errs = append(errs, fmt.Errorf("encode plex session %s servers: %w", rec.id, err))
			continue
		}
		tag, err := r.pool.Exec(ctx, `
			UPDATE history_import_plex_sessions
			SET servers_json = $2::jsonb, updated_at = NOW()
			WHERE id = $1 AND servers_json = $3::jsonb`,
			rec.id, string(body), rec.raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("update plex session %s servers: %w", rec.id, err))
			continue
		}
		if tag.RowsAffected() > 0 {
			count++
		}
	}
	return count, errors.Join(errs...)
}

func (r *Repository) ProfileExistsForUser(ctx context.Context, userID int, profileID string) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_profiles WHERE user_id = $1 AND id = $2)`,
		userID, profileID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking profile ownership: %w", err)
	}
	return exists, nil
}

func (r *Repository) CreateRun(ctx context.Context, run Run) (*Run, error) {
	warningsJSON, err := json.Marshal(nonNilStrings(run.Warnings))
	if err != nil {
		return nil, fmt.Errorf("marshaling run warnings: %w", err)
	}
	unmatchedJSON, err := json.Marshal(nonNilUnmatchedSamples(run.UnmatchedSamples))
	if err != nil {
		return nil, fmt.Errorf("marshaling run unmatched samples: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO history_import_runs (
			id, user_id, profile_id, source_type, connection_mode, status,
			mapping_id,
			fetched, matched, unmatched, progress_updated, history_created, watchlist_added, skipped,
			warnings, unmatched_samples, error_message
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7,
			$8, $9, $10, $11, $12, $13, $14,
			$15, $16, NULLIF($17, '')
		)
		RETURNING id, user_id, profile_id, source_type, connection_mode, status,
			mapping_id,
			fetched, matched, unmatched, progress_updated, history_created, watchlist_added, skipped,
			warnings, unmatched_samples, COALESCE(error_message, ''), created_at, started_at, completed_at`,
		run.ID, run.UserID, run.ProfileID, run.SourceType, run.ConnectionMode, run.Status,
		run.MappingID,
		run.Fetched, run.Matched, run.Unmatched, run.ProgressUpdated, run.HistoryCreated, run.WatchlistAdded, run.Skipped,
		warningsJSON, unmatchedJSON, run.ErrorMessage,
	)
	created, err := scanRunWithMappingID(row)
	if err != nil {
		return nil, fmt.Errorf("creating history import run: %w", err)
	}
	return created, nil
}

func (r *Repository) MarkRunStarted(ctx context.Context, runID string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_runs
		SET status = $2, started_at = NOW(), last_heartbeat_at = NOW()
		WHERE id = $1`, runID, RunStatusRunning)
	if err != nil {
		return fmt.Errorf("marking run %s started: %w", runID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *Repository) TouchRunHeartbeat(ctx context.Context, runID string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_runs
		SET last_heartbeat_at = NOW()
		WHERE id = $1 AND status = $2`, runID, RunStatusRunning)
	if err != nil {
		return fmt.Errorf("touching run %s heartbeat: %w", runID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *Repository) CompleteRun(ctx context.Context, runID string, summary ExecutionSummary) error {
	warningsJSON, err := json.Marshal(trimWarnings(persistedWarnings(summary)))
	if err != nil {
		return fmt.Errorf("marshaling run warnings: %w", err)
	}
	unmatchedJSON, err := json.Marshal(trimUnmatchedSamples(summary.UnmatchedSamples))
	if err != nil {
		return fmt.Errorf("marshaling unmatched samples: %w", err)
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_runs
		SET
			status = $2,
			fetched = $3,
			matched = $4,
			unmatched = $5,
			progress_updated = $6,
			history_created = $7,
			watchlist_added = $8,
			skipped = $9,
			warnings = $10,
			unmatched_samples = $11,
			error_message = NULL,
			completed_at = NOW(),
			last_heartbeat_at = NOW()
		WHERE id = $1`,
		runID, RunStatusCompleted, summary.Fetched, summary.Matched, summary.Unmatched,
		summary.ProgressUpdated, summary.HistoryCreated, summary.WatchlistAdded, summary.Skipped, warningsJSON, unmatchedJSON,
	)
	if err != nil {
		return fmt.Errorf("completing run %s: %w", runID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *Repository) UpdateRunProgress(ctx context.Context, runID string, summary ExecutionSummary) error {
	warningsJSON, err := json.Marshal(trimWarnings(persistedWarnings(summary)))
	if err != nil {
		return fmt.Errorf("marshaling run warnings: %w", err)
	}
	unmatchedJSON, err := json.Marshal(trimUnmatchedSamples(summary.UnmatchedSamples))
	if err != nil {
		return fmt.Errorf("marshaling unmatched samples: %w", err)
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_runs
		SET
			fetched = $2,
			matched = $3,
			unmatched = $4,
			progress_updated = $5,
			history_created = $6,
			watchlist_added = $7,
			skipped = $8,
			warnings = $9,
			unmatched_samples = $10,
			last_heartbeat_at = NOW()
		WHERE id = $1 AND status = $11`,
		runID,
		summary.Fetched,
		summary.Matched,
		summary.Unmatched,
		summary.ProgressUpdated,
		summary.HistoryCreated,
		summary.WatchlistAdded,
		summary.Skipped,
		warningsJSON,
		unmatchedJSON,
		RunStatusRunning,
	)
	if err != nil {
		return fmt.Errorf("updating run %s progress: %w", runID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *Repository) FailStaleRuns(ctx context.Context, staleBefore time.Time, errorMessage string) (int64, error) {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_runs
		SET
			status = $1,
			error_message = NULLIF($2, ''),
			completed_at = NOW()
		WHERE status = $3
			AND completed_at IS NULL
			AND COALESCE(last_heartbeat_at, started_at, created_at) < $4`,
		RunStatusFailed,
		errorMessage,
		RunStatusRunning,
		staleBefore,
	)
	if err != nil {
		return 0, fmt.Errorf("failing stale history import runs: %w", err)
	}
	return result.RowsAffected(), nil
}

func (r *Repository) FailRun(ctx context.Context, runID string, summary ExecutionSummary, errorMessage string) error {
	warningsJSON, err := json.Marshal(trimWarnings(persistedWarnings(summary)))
	if err != nil {
		return fmt.Errorf("marshaling run warnings: %w", err)
	}
	unmatchedJSON, err := json.Marshal(trimUnmatchedSamples(summary.UnmatchedSamples))
	if err != nil {
		return fmt.Errorf("marshaling unmatched samples: %w", err)
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_runs
		SET
			status = $2,
			fetched = $3,
			matched = $4,
			unmatched = $5,
			progress_updated = $6,
			history_created = $7,
			watchlist_added = $8,
			skipped = $9,
			warnings = $10,
			unmatched_samples = $11,
			error_message = NULLIF($12, ''),
			completed_at = NOW(),
			last_heartbeat_at = NOW()
		WHERE id = $1`,
		runID, RunStatusFailed, summary.Fetched, summary.Matched, summary.Unmatched,
		summary.ProgressUpdated, summary.HistoryCreated, summary.WatchlistAdded, summary.Skipped, warningsJSON, unmatchedJSON, errorMessage,
	)
	if err != nil {
		return fmt.Errorf("failing run %s: %w", runID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *Repository) ListRunsForUser(ctx context.Context, userID, limit int) ([]Run, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, profile_id, source_type, connection_mode, status,
			mapping_id,
			fetched, matched, unmatched, progress_updated, history_created, watchlist_added, skipped,
			warnings, unmatched_samples, COALESCE(error_message, ''), created_at, started_at, completed_at
		FROM history_import_runs
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing history import runs: %w", err)
	}
	defer rows.Close()
	return scanRunsWithMappingID(rows)
}

func (r *Repository) GetRunForUser(ctx context.Context, userID int, runID string) (*Run, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, source_type, connection_mode, status,
			mapping_id,
			fetched, matched, unmatched, progress_updated, history_created, watchlist_added, skipped,
			warnings, unmatched_samples, COALESCE(error_message, ''), created_at, started_at, completed_at
		FROM history_import_runs
		WHERE id = $1 AND user_id = $2`, runID, userID)
	run, err := scanRunWithMappingID(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting run %s: %w", runID, err)
	}
	return run, nil
}

func (r *Repository) ListActiveRunsForUser(ctx context.Context, userID int) ([]Run, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, profile_id, source_type, connection_mode, status,
			mapping_id,
			fetched, matched, unmatched, progress_updated, history_created, watchlist_added, skipped,
			warnings, unmatched_samples, COALESCE(error_message, ''), created_at, started_at, completed_at
		FROM history_import_runs
		WHERE user_id = $1
		  AND status IN ($2, $3)
		ORDER BY created_at DESC`, userID, RunStatusQueued, RunStatusRunning)
	if err != nil {
		return nil, fmt.Errorf("listing active history import runs: %w", err)
	}
	defer rows.Close()
	return scanRunsWithMappingID(rows)
}

func (r *Repository) GetProgress(ctx context.Context, userID int, profileID, mediaItemID string) (*localProgressRow, error) {
	var row localProgressRow
	err := r.pool.QueryRow(ctx, `
		SELECT updated_at
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		userID, profileID, mediaItemID,
	).Scan(&row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting watch progress: %w", err)
	}
	return &row, nil
}

func (r *Repository) UpsertImportedProgress(
	ctx context.Context,
	userID int,
	profileID, mediaItemID string,
	positionSeconds, durationSeconds float64,
	completed bool,
	updatedAt time.Time,
) error {
	if durationSeconds < 0 {
		durationSeconds = 0
	}
	if positionSeconds < 0 {
		positionSeconds = 0
	}
	// Completed rows hold no resume point and `completed` is a one-way watched
	// latch — mirrors the userstore write paths so imported rows can't surface
	// as phantom Continue Watching entries.
	if completed {
		positionSeconds = 0
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (
			user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = user_watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at`,
		userID, profileID, mediaItemID, positionSeconds, durationSeconds, completed, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("upserting imported progress: %w", err)
	}
	return nil
}

func (r *Repository) InsertHistoryIfMissing(
	ctx context.Context,
	userID int,
	profileID, mediaItemID string,
	watchedAt time.Time,
	durationSeconds float64,
	completed bool,
) (bool, error) {
	if watchedAt.IsZero() {
		return false, nil
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM user_watch_history
			WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3 AND watched_at = $4
		)`,
		userID, profileID, mediaItemID, watchedAt,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking history row existence: %w", err)
	}
	if exists {
		return false, nil
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_watch_history (
			id, user_id, profile_id, media_item_id, watched_at, duration_seconds, completed
		) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		uuid.NewString(), userID, profileID, mediaItemID, watchedAt, durationSeconds, completed,
	)
	if err != nil {
		return false, fmt.Errorf("inserting history row: %w", err)
	}
	return true, nil
}

type mediaLookupRow struct {
	ContentID string
	Title     string
	Year      int
}

func (r *Repository) MatchMediaByExternalID(ctx context.Context, kind, column, value string) ([]mediaLookupRow, error) {
	if value == "" {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, title, COALESCE(year, 0)
		FROM media_items
		WHERE type = $1 AND status = 'matched' AND `+column+` = $2
		ORDER BY content_id ASC`,
		kind, value,
	)
	if err != nil {
		return nil, fmt.Errorf("matching media by %s: %w", column, err)
	}
	defer rows.Close()
	var matches []mediaLookupRow
	for rows.Next() {
		var row mediaLookupRow
		if err := rows.Scan(&row.ContentID, &row.Title, &row.Year); err != nil {
			return nil, fmt.Errorf("scanning media lookup row: %w", err)
		}
		matches = append(matches, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media lookup rows: %w", err)
	}
	return matches, nil
}

func (r *Repository) MatchMediaByTitleYear(ctx context.Context, kind, title string, year int) ([]mediaLookupRow, error) {
	if title == "" {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, title, COALESCE(year, 0)
		FROM media_items
		WHERE type = $1 AND status = 'matched' AND title = $2 AND ($3 = 0 OR COALESCE(year, 0) = $3)
		ORDER BY content_id ASC`,
		kind, title, year,
	)
	if err != nil {
		return nil, fmt.Errorf("matching media by title/year: %w", err)
	}
	defer rows.Close()
	var matches []mediaLookupRow
	for rows.Next() {
		var row mediaLookupRow
		if err := rows.Scan(&row.ContentID, &row.Title, &row.Year); err != nil {
			return nil, fmt.Errorf("scanning title/year match: %w", err)
		}
		matches = append(matches, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating title/year matches: %w", err)
	}
	return matches, nil
}

func (r *Repository) MatchEpisodeByExternalID(ctx context.Context, column, value string) ([]mediaLookupRow, error) {
	if value == "" {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT e.content_id, COALESCE(e.title, ''), COALESCE(series.year, 0)
		FROM episodes e
		LEFT JOIN media_items series ON series.content_id = e.series_id
		WHERE e.`+column+` = $1
		ORDER BY e.content_id ASC`,
		value,
	)
	if err != nil {
		return nil, fmt.Errorf("matching episode by %s: %w", column, err)
	}
	defer rows.Close()

	var matches []mediaLookupRow
	for rows.Next() {
		var row mediaLookupRow
		if err := rows.Scan(&row.ContentID, &row.Title, &row.Year); err != nil {
			return nil, fmt.Errorf("scanning episode lookup row: %w", err)
		}
		matches = append(matches, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating episode lookup rows: %w", err)
	}
	return matches, nil
}

func (r *Repository) MatchEpisodeBySeries(ctx context.Context, seriesID string, seasonNumber, episodeNumber int) (*Match, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT e.content_id, COALESCE(e.title, ''), COALESCE(series.year, 0)
		FROM episodes e
		LEFT JOIN media_items series ON series.content_id = e.series_id
		WHERE e.series_id = $1 AND e.season_number = $2 AND e.episode_number = $3`,
		seriesID, seasonNumber, episodeNumber,
	)
	var match Match
	if err := row.Scan(&match.MediaItemID, &match.Title, &match.Year); errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("matching episode: %w", err)
	}
	match.Kind = KindEpisode
	return &match, nil
}

func scanSource(scanner interface{ Scan(dest ...any) error }) (*Source, error) {
	var source Source
	if err := scanner.Scan(
		&source.ID, &source.Name, &source.SourceType, &source.BaseURL, &source.SystemID,
		&source.Enabled, &source.SortOrder, &source.HasAdminToken,
		&source.CreatedAt, &source.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &source, nil
}

func scanSources(rows pgx.Rows) ([]Source, error) {
	var sources []Source
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, *source)
	}
	return sources, rows.Err()
}

func (r *Repository) scanConnectSession(scanner interface{ Scan(dest ...any) error }) (*ConnectSession, error) {
	var session ConnectSession
	var serversJSON []byte
	if err := scanner.Scan(
		&session.ID, &session.UserID, &session.ConnectUserID, &session.ConnectAccessToken,
		&serversJSON, &session.ExpiresAt, &session.ConsumedAt, &session.CreatedAt, &session.UpdatedAt,
	); err != nil {
		return nil, err
	}
	token, err := r.cipher.DecryptIfEncrypted(session.ConnectAccessToken, connectSessionTokenAAD(session.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt connect access token: %w", err)
	}
	session.ConnectAccessToken = token
	if len(serversJSON) > 0 {
		if err := json.Unmarshal(serversJSON, &session.Servers); err != nil {
			return nil, fmt.Errorf("unmarshaling connect session servers: %w", err)
		}
		session.Servers, err = r.decryptConnectServers(session.ID, session.Servers)
		if err != nil {
			return nil, err
		}
	}
	return &session, nil
}

// scanRun is the legacy scanner for queries that do NOT include mapping_id.
// Kept for backward compatibility with UpdateRunProgress/CompleteRun/FailRun paths
// that use Exec and do not scan rows.
func scanRun(scanner interface{ Scan(dest ...any) error }) (*Run, error) {
	var run Run
	var warningsJSON []byte
	var unmatchedJSON []byte
	if err := scanner.Scan(
		&run.ID, &run.UserID, &run.ProfileID, &run.SourceType, &run.ConnectionMode, &run.Status,
		&run.Fetched, &run.Matched, &run.Unmatched, &run.ProgressUpdated, &run.HistoryCreated, &run.WatchlistAdded, &run.Skipped,
		&warningsJSON, &unmatchedJSON, &run.ErrorMessage, &run.CreatedAt, &run.StartedAt, &run.CompletedAt,
	); err != nil {
		return nil, err
	}
	return finalizeRunScan(&run, warningsJSON, unmatchedJSON)
}

// scanRunWithMappingID scans a run row that includes the mapping_id column.
func scanRunWithMappingID(scanner interface{ Scan(dest ...any) error }) (*Run, error) {
	var run Run
	var warningsJSON []byte
	var unmatchedJSON []byte
	if err := scanner.Scan(
		&run.ID, &run.UserID, &run.ProfileID, &run.SourceType, &run.ConnectionMode, &run.Status,
		&run.MappingID,
		&run.Fetched, &run.Matched, &run.Unmatched, &run.ProgressUpdated, &run.HistoryCreated, &run.WatchlistAdded, &run.Skipped,
		&warningsJSON, &unmatchedJSON, &run.ErrorMessage, &run.CreatedAt, &run.StartedAt, &run.CompletedAt,
	); err != nil {
		return nil, err
	}
	return finalizeRunScan(&run, warningsJSON, unmatchedJSON)
}

func finalizeRunScan(run *Run, warningsJSON, unmatchedJSON []byte) (*Run, error) {
	if len(warningsJSON) > 0 {
		if err := json.Unmarshal(warningsJSON, &run.Warnings); err != nil {
			return nil, fmt.Errorf("unmarshaling run warnings: %w", err)
		}
	}
	if len(unmatchedJSON) > 0 {
		if err := json.Unmarshal(unmatchedJSON, &run.UnmatchedSamples); err != nil {
			return nil, fmt.Errorf("unmarshaling unmatched samples: %w", err)
		}
	}
	if run.Warnings == nil {
		run.Warnings = []string{}
	}
	if run.UnmatchedSamples == nil {
		run.UnmatchedSamples = []UnmatchedSample{}
	}
	return run, nil
}

func scanRuns(rows pgx.Rows) ([]Run, error) {
	var runs []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

func scanRunsWithMappingID(rows pgx.Rows) ([]Run, error) {
	var runs []Run
	for rows.Next() {
		run, err := scanRunWithMappingID(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

func trimWarnings(warnings []string) []string {
	if len(warnings) <= maxStoredWarnings {
		return nonNilStrings(warnings)
	}
	return nonNilStrings(warnings[:maxStoredWarnings])
}

func persistedWarnings(summary ExecutionSummary) []string {
	warnings := append([]string{}, nonNilStrings(summary.Warnings)...)
	if len(summary.UnmatchedReasonCounts) == 0 {
		return warnings
	}

	type reasonCount struct {
		reason string
		count  int
	}
	reasons := make([]reasonCount, 0, len(summary.UnmatchedReasonCounts))
	for reason, count := range summary.UnmatchedReasonCounts {
		if count <= 0 || reason == "" {
			continue
		}
		reasons = append(reasons, reasonCount{reason: reason, count: count})
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].count != reasons[j].count {
			return reasons[i].count > reasons[j].count
		}
		return reasons[i].reason < reasons[j].reason
	})
	for _, reason := range reasons {
		warnings = append(warnings, fmt.Sprintf("unmatched items (%d): %s", reason.count, reason.reason))
	}
	return warnings
}

func trimUnmatchedSamples(samples []UnmatchedSample) []UnmatchedSample {
	if len(samples) <= maxUnmatchedSamples {
		return nonNilUnmatchedSamples(samples)
	}
	return nonNilUnmatchedSamples(samples[:maxUnmatchedSamples])
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilUnmatchedSamples(values []UnmatchedSample) []UnmatchedSample {
	if values == nil {
		return []UnmatchedSample{}
	}
	return values
}

func (r *Repository) scanPlexSession(scanner interface{ Scan(dest ...any) error }) (*PlexSession, error) {
	var session PlexSession
	var authToken *string
	var serversJSON []byte
	if err := scanner.Scan(
		&session.ID, &session.UserID, &session.PinID, &session.PinCode,
		&authToken, &serversJSON, &session.ExpiresAt, &session.ConsumedAt,
		&session.CreatedAt, &session.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if authToken != nil {
		session.AuthToken = *authToken
	}
	token, err := r.cipher.DecryptIfEncrypted(session.AuthToken, plexSessionTokenAAD(session.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt plex auth token: %w", err)
	}
	session.AuthToken = token
	if len(serversJSON) > 0 {
		if err := json.Unmarshal(serversJSON, &session.Servers); err != nil {
			return nil, fmt.Errorf("unmarshaling plex session servers: %w", err)
		}
		session.Servers, err = r.decryptPlexServers(session.ID, session.Servers)
		if err != nil {
			return nil, err
		}
	}
	return &session, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
