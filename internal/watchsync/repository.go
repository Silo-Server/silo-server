package watchsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

type Repository interface {
	GetServerSetting(ctx context.Context, key string) (string, error)
	UpsertAuthSession(ctx context.Context, session DeviceAuthSession) (DeviceAuthSession, error)
	GetAuthSession(ctx context.Context, id string) (DeviceAuthSession, error)
	UpsertConnection(ctx context.Context, conn Connection) (Connection, error)
	GetConnection(ctx context.Context, provider string, userID int, profileID string) (Connection, bool, error)
	GetConnectionByID(ctx context.Context, id string) (Connection, bool, error)
	DeleteConnection(ctx context.Context, provider string, userID int, profileID string) error
	ListConnectionsDueForSync(ctx context.Context, now time.Time) ([]Connection, error)
	DeferConnectionsForAccount(ctx context.Context, provider, providerAccountID string, until time.Time, lastError string) (int, error)
	CreateSyncRun(ctx context.Context, run SyncRun) (SyncRun, error)
	CompleteSyncRun(ctx context.Context, run SyncRun) (SyncRun, error)
	GetLatestSyncRun(ctx context.Context, connectionID string) (SyncRun, bool, error)
	GetActiveSyncRun(ctx context.Context, connectionID string) (SyncRun, bool, error)
	ListSyncRuns(ctx context.Context, connectionID string, limit int) ([]SyncRun, error)
	ListLocalWatchEventConnections(ctx context.Context, userID int, profileID string, kind LocalWatchEventKind) ([]Connection, error)
	ListListEventConnections(ctx context.Context, userID int, profileID string, list ListKind) ([]Connection, error)
	UpsertHistoryExports(ctx context.Context, exports []HistoryExport) error
	ListPendingHistoryExports(ctx context.Context, connectionID string, limit int) ([]HistoryExport, error)
	MarkHistoryExportStatus(ctx context.Context, id string, status string, lastError string) error
	UpsertListItemStates(ctx context.Context, states []ListItemState) error
	ListListItemStates(ctx context.Context, connectionID string, kind ListKind) ([]ListItemState, error)
	ListPendingListItemExports(ctx context.Context, connectionID string, kind ListKind, limit int) ([]ListItemState, error)
	ListPendingListItemRemovals(ctx context.Context, connectionID string, kind ListKind, limit int) ([]ListItemState, error)
	MarkListItemExported(ctx context.Context, connectionID string, kind ListKind, mediaItemID string, exportedAt time.Time) error
	MarkListItemRemoteRemoved(ctx context.Context, connectionID string, kind ListKind, mediaItemID string, removedAt time.Time) error
	MarkListItemLocalRemoved(ctx context.Context, connectionID string, kind ListKind, mediaItemID string, removedAt time.Time) error
	MarkListItemError(ctx context.Context, connectionID string, kind ListKind, mediaItemID, lastError string) error
	ListScrobbleConnections(ctx context.Context, userID int, profileID string) ([]Connection, error)
	UpsertScrobbleSession(ctx context.Context, event ScrobbleEvent, connectionID string, action string) error
	PrepareConfirmedScrobbleStop(ctx context.Context, event ScrobbleEvent, connectionID string, staleBefore time.Time) (confirmedStopPreparation, time.Time, error)
	CompleteConfirmedScrobbleStop(ctx context.Context, playbackSessionID string, connectionID string, progress float64, historyID string, claimVersion time.Time, stopSentAt time.Time) error
	FailConfirmedScrobbleStop(ctx context.Context, playbackSessionID string, connectionID string, progress float64, historyID string, claimVersion time.Time, lastError string) error
	UpdateScrobbleSession(ctx context.Context, playbackSessionID string, connectionID string, action string, progress float64, historyID string, lastError string, stopSentAt *time.Time) error
	ListOpenScrobbleSessions(ctx context.Context) ([]ScrobbleSession, error)
}

type confirmedStopPreparation uint8

const (
	confirmedStopPrepared confirmedStopPreparation = iota
	confirmedStopAlreadySent
	confirmedStopInProgress
)

var errConfirmedStopClaimLost = errors.New("confirmed scrobble stop claim lost")

// connectionColumns is the canonical select/returning column list for
// watch_provider_connections, in the exact order scanConnection reads. Sharing
// it across every read query keeps the column set and scan order in lockstep.
const connectionColumns = `
	id::text, provider, user_id, profile_id, provider_account_id, provider_username,
	access_token, refresh_token, token_expires_at, import_watched_enabled,
	import_progress_enabled, export_watched_enabled, export_unwatched_enabled,
	import_favorites_enabled, export_favorites_enabled, sync_favorite_removals_enabled,
	import_watchlist_enabled, export_watchlist_enabled, sync_watchlist_removals_enabled,
	sync_watchlist_order_enabled, scrobble_enabled, last_inbound_sync_at,
	last_progress_sync_at, last_outbound_sync_at, last_favorites_sync_at,
	last_watchlist_sync_at, last_scrobble_error_at, last_error,
	rate_limited_until, sync_cursors, created_at, updated_at`

// syncRunColumns is the canonical select/returning column list for
// watch_provider_sync_runs, in the exact order scanSyncRun reads.
const syncRunColumns = `
	id::text, connection_id::text, trigger, status, provider,
	inbound_watched_found, inbound_watched_imported,
	inbound_progress_found, inbound_progress_imported,
	outbound_found, outbound_sent, inbound_favorites_found,
	inbound_favorites_imported, outbound_favorites_found,
	outbound_favorites_sent, favorite_removals_sent,
	inbound_watchlist_found, inbound_watchlist_imported,
	outbound_watchlist_found, outbound_watchlist_sent, watchlist_removals_sent,
	warning, error, started_at, completed_at, created_at`

// listItemStateColumns is the canonical select column list for
// watch_provider_list_items, in the exact order scanListItemStates reads.
const listItemStateColumns = `
	id::text, connection_id::text, list_kind, media_item_id, provider_item_key, kind, title, year,
	remote_present, local_present, last_seen_remote_at, last_seen_local_at,
	last_exported_at, last_removed_remote_at, last_removed_local_at, last_error, created_at, updated_at`

type PostgresRepository struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

func NewPostgresRepository(pool *pgxpool.Pool, cipher *secret.Cipher) *PostgresRepository {
	return &PostgresRepository{pool: pool, cipher: cipher}
}

// TokenAAD binds an access/refresh token ciphertext to its connection. It uses
// the stable UNIQUE business key (provider, user_id, profile_id) rather than the
// surrogate id, because UpsertConnection lets Postgres assign/keep the id (ON
// CONFLICT), so the id is not known before the write — the tuple is, and it
// identifies the row just as uniquely. Exported so the (raw-SQL) Trakt
// collection-token resolver binds tokens identically.
func TokenAAD(column, provider string, userID int, profileID string) string {
	return secret.RowAAD("watch_provider_connections", column, provider+":"+strconv.Itoa(userID)+":"+profileID)
}

func (r *PostgresRepository) GetServerSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := r.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("server_settings get %q: %w", key, err)
	}
	// This repo reads watchsync.<provider>.client_id/client_secret (sensitive
	// settings) directly, bypassing the settings decorator, so apply the same
	// read-path decryption here.
	out, err := r.cipher.DecryptIfEncrypted(value, secret.SettingsAAD(key))
	if err != nil {
		return "", fmt.Errorf("decrypt server_settings %q: %w", key, err)
	}
	return out, nil
}

func (r *PostgresRepository) UpsertAuthSession(
	ctx context.Context,
	session DeviceAuthSession,
) (DeviceAuthSession, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO watch_provider_auth_sessions (
			id, provider, user_id, profile_id, device_code, user_code,
			verification_url, interval_seconds, expires_at, completed_at
		)
		VALUES (
			COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()),
			$2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		ON CONFLICT (id) DO UPDATE SET
			provider = EXCLUDED.provider,
			user_id = EXCLUDED.user_id,
			profile_id = EXCLUDED.profile_id,
			device_code = EXCLUDED.device_code,
			user_code = EXCLUDED.user_code,
			verification_url = EXCLUDED.verification_url,
			interval_seconds = EXCLUDED.interval_seconds,
			expires_at = EXCLUDED.expires_at,
			completed_at = EXCLUDED.completed_at,
			updated_at = now()
		RETURNING
			id::text, provider, user_id, profile_id, device_code, user_code,
			verification_url, interval_seconds, expires_at, completed_at
	`,
		session.ID,
		session.Provider,
		session.UserID,
		session.ProfileID,
		session.DeviceCode,
		session.UserCode,
		session.VerificationURL,
		session.IntervalSeconds,
		session.ExpiresAt,
		session.CompletedAt,
	)
	saved, err := scanDeviceAuthSession(row)
	if err != nil {
		return DeviceAuthSession{}, fmt.Errorf("upsert watch provider auth session: %w", err)
	}
	return saved, nil
}

func (r *PostgresRepository) GetAuthSession(ctx context.Context, id string) (DeviceAuthSession, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT
			id::text, provider, user_id, profile_id, device_code, user_code,
			verification_url, interval_seconds, expires_at, completed_at
		FROM watch_provider_auth_sessions
		WHERE id = $1::uuid
	`, id)
	session, err := scanDeviceAuthSession(row)
	if err != nil {
		return DeviceAuthSession{}, fmt.Errorf("get watch provider auth session %q: %w", id, err)
	}
	return session, nil
}

func (r *PostgresRepository) UpsertConnection(ctx context.Context, conn Connection) (Connection, error) {
	accessToken, err := r.cipher.Encrypt(conn.AccessToken, TokenAAD("access_token", conn.Provider, conn.UserID, conn.ProfileID))
	if err != nil {
		return Connection{}, fmt.Errorf("encrypt watch access token: %w", err)
	}
	refreshToken, err := r.cipher.Encrypt(conn.RefreshToken, TokenAAD("refresh_token", conn.Provider, conn.UserID, conn.ProfileID))
	if err != nil {
		return Connection{}, fmt.Errorf("encrypt watch refresh token: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO watch_provider_connections (
			id, provider, user_id, profile_id, provider_account_id, provider_username,
			access_token, refresh_token, token_expires_at, import_watched_enabled,
			import_progress_enabled, export_watched_enabled, export_unwatched_enabled,
			import_favorites_enabled, export_favorites_enabled, sync_favorite_removals_enabled,
			import_watchlist_enabled, export_watchlist_enabled, sync_watchlist_removals_enabled,
			sync_watchlist_order_enabled, scrobble_enabled, last_inbound_sync_at, last_progress_sync_at,
			last_outbound_sync_at, last_favorites_sync_at, last_watchlist_sync_at, last_scrobble_error_at,
			last_error, rate_limited_until, sync_cursors
		)
		VALUES (
			COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()),
			$2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30::jsonb
		)
		ON CONFLICT (provider, user_id, profile_id) DO UPDATE SET
			provider_account_id = EXCLUDED.provider_account_id,
			provider_username = EXCLUDED.provider_username,
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			token_expires_at = EXCLUDED.token_expires_at,
			import_watched_enabled = EXCLUDED.import_watched_enabled,
			import_progress_enabled = EXCLUDED.import_progress_enabled,
			export_watched_enabled = EXCLUDED.export_watched_enabled,
			export_unwatched_enabled = EXCLUDED.export_unwatched_enabled,
			import_favorites_enabled = EXCLUDED.import_favorites_enabled,
			export_favorites_enabled = EXCLUDED.export_favorites_enabled,
			sync_favorite_removals_enabled = EXCLUDED.sync_favorite_removals_enabled,
			import_watchlist_enabled = EXCLUDED.import_watchlist_enabled,
			export_watchlist_enabled = EXCLUDED.export_watchlist_enabled,
			sync_watchlist_removals_enabled = EXCLUDED.sync_watchlist_removals_enabled,
			sync_watchlist_order_enabled = EXCLUDED.sync_watchlist_order_enabled,
			scrobble_enabled = EXCLUDED.scrobble_enabled,
			last_inbound_sync_at = EXCLUDED.last_inbound_sync_at,
			last_progress_sync_at = EXCLUDED.last_progress_sync_at,
			last_outbound_sync_at = EXCLUDED.last_outbound_sync_at,
			last_favorites_sync_at = EXCLUDED.last_favorites_sync_at,
			last_watchlist_sync_at = EXCLUDED.last_watchlist_sync_at,
			last_scrobble_error_at = EXCLUDED.last_scrobble_error_at,
			last_error = EXCLUDED.last_error,
			rate_limited_until = EXCLUDED.rate_limited_until,
			sync_cursors = EXCLUDED.sync_cursors,
			updated_at = now()
		RETURNING `+connectionColumns+`
	`,
		conn.ID,
		conn.Provider,
		conn.UserID,
		conn.ProfileID,
		conn.ProviderAccountID,
		conn.ProviderUsername,
		accessToken,
		refreshToken,
		conn.TokenExpiresAt,
		conn.ImportWatchedEnabled,
		conn.ImportProgressEnabled,
		conn.ExportWatchedEnabled,
		conn.ExportUnwatchedEnabled,
		conn.ImportFavoritesEnabled,
		conn.ExportFavoritesEnabled,
		conn.SyncFavoriteRemovalsEnabled,
		conn.ImportWatchlistEnabled,
		conn.ExportWatchlistEnabled,
		conn.SyncWatchlistRemovalsEnabled,
		conn.SyncWatchlistOrderEnabled,
		conn.ScrobbleEnabled,
		conn.LastInboundSyncAt,
		conn.LastProgressSyncAt,
		conn.LastOutboundSyncAt,
		conn.LastFavoritesSyncAt,
		conn.LastWatchlistSyncAt,
		conn.LastScrobbleErrorAt,
		conn.LastError,
		conn.RateLimitedUntil,
		encodeSyncCursors(conn.SyncCursors),
	)
	saved, err := r.scanConnection(row)
	if err != nil {
		return Connection{}, fmt.Errorf("upsert watch provider connection: %w", err)
	}
	return saved, nil
}

func (r *PostgresRepository) GetConnection(
	ctx context.Context,
	provider string,
	userID int,
	profileID string,
) (Connection, bool, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+connectionColumns+`
		FROM watch_provider_connections
		WHERE provider = $1 AND user_id = $2 AND profile_id = $3
	`, provider, userID, profileID)
	conn, err := r.scanConnection(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, false, nil
	}
	if err != nil {
		return Connection{}, false, fmt.Errorf("get watch provider connection: %w", err)
	}
	return conn, true, nil
}

func (r *PostgresRepository) GetConnectionByID(ctx context.Context, id string) (Connection, bool, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+connectionColumns+`
		FROM watch_provider_connections
		WHERE id = $1::uuid
	`, id)
	conn, err := r.scanConnection(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, false, nil
	}
	if err != nil {
		return Connection{}, false, fmt.Errorf("get watch provider connection by id: %w", err)
	}
	return conn, true, nil
}

func (r *PostgresRepository) DeleteConnection(
	ctx context.Context,
	provider string,
	userID int,
	profileID string,
) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM watch_provider_connections
		WHERE provider = $1 AND user_id = $2 AND profile_id = $3
	`, provider, userID, profileID)
	if err != nil {
		return fmt.Errorf("delete watch provider connection: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListConnectionsDueForSync(
	ctx context.Context,
	now time.Time,
) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+connectionColumns+`
		FROM watch_provider_connections
		WHERE provider <> ''
			AND (rate_limited_until IS NULL OR rate_limited_until <= $1)
			AND (
				import_watched_enabled
				OR import_progress_enabled
				OR export_watched_enabled
				OR export_unwatched_enabled
				OR import_favorites_enabled
				OR export_favorites_enabled
				OR sync_favorite_removals_enabled
				OR import_watchlist_enabled
				OR export_watchlist_enabled
				OR sync_watchlist_removals_enabled
				OR scrobble_enabled
			)
		ORDER BY provider, user_id, profile_id
	`, now)
	if err != nil {
		return nil, fmt.Errorf("list due watch provider connections: %w", err)
	}
	defer rows.Close()

	var conns []Connection
	for rows.Next() {
		conn, scanErr := r.scanConnection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan due watch provider connection: %w", scanErr)
		}
		conns = append(conns, conn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due watch provider connections: %w", err)
	}
	return conns, nil
}

// DeferConnectionsForAccount stamps rate_limited_until on every connection
// bound to the same provider account. Provider rate limits apply to the API
// key/account, not the Silo profile, so all sibling connections must sit out
// the same window.
func (r *PostgresRepository) DeferConnectionsForAccount(
	ctx context.Context,
	provider string,
	providerAccountID string,
	until time.Time,
	lastError string,
) (int, error) {
	if strings.TrimSpace(providerAccountID) == "" {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE watch_provider_connections
		SET rate_limited_until = $1, last_error = $2, updated_at = now()
		WHERE provider = $3 AND provider_account_id = $4
	`, until, lastError, provider, providerAccountID)
	if err != nil {
		return 0, fmt.Errorf("defer watch provider connections for account: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *PostgresRepository) CreateSyncRun(ctx context.Context, run SyncRun) (SyncRun, error) {
	if run.Status == "" {
		run.Status = string(SyncRunStatusRunning)
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO watch_provider_sync_runs (
			connection_id, trigger, status, provider,
			inbound_watched_found, inbound_watched_imported,
			inbound_progress_found, inbound_progress_imported,
			outbound_found, outbound_sent, inbound_favorites_found,
			inbound_favorites_imported, outbound_favorites_found,
			outbound_favorites_sent, favorite_removals_sent,
			inbound_watchlist_found, inbound_watchlist_imported,
			outbound_watchlist_found, outbound_watchlist_sent, watchlist_removals_sent,
			warning, error, started_at, completed_at
		)
		VALUES (
			$1::uuid, $2, $3, $4,
			$5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, $21, $22, $23, $24
		)
		RETURNING `+syncRunColumns+`
	`, run.ConnectionID, run.Trigger, run.Status, run.Provider,
		run.InboundWatchedFound, run.InboundWatchedImported,
		run.InboundProgressFound, run.InboundProgressImported,
		run.OutboundFound, run.OutboundSent, run.InboundFavoritesFound,
		run.InboundFavoritesImported, run.OutboundFavoritesFound,
		run.OutboundFavoritesSent, run.FavoriteRemovalsSent,
		run.InboundWatchlistFound, run.InboundWatchlistImported,
		run.OutboundWatchlistFound, run.OutboundWatchlistSent, run.WatchlistRemovalsSent,
		run.Warning, run.Error, run.StartedAt, run.CompletedAt)
	created, err := scanSyncRun(row)
	if err != nil {
		return SyncRun{}, fmt.Errorf("scan created watch provider sync run: %w", err)
	}
	return created, nil
}

func (r *PostgresRepository) CompleteSyncRun(ctx context.Context, run SyncRun) (SyncRun, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE watch_provider_sync_runs
		SET status = $2,
			inbound_watched_found = $3,
			inbound_watched_imported = $4,
			inbound_progress_found = $5,
			inbound_progress_imported = $6,
			outbound_found = $7,
			outbound_sent = $8,
			inbound_favorites_found = $9,
			inbound_favorites_imported = $10,
			outbound_favorites_found = $11,
			outbound_favorites_sent = $12,
			favorite_removals_sent = $13,
			inbound_watchlist_found = $14,
			inbound_watchlist_imported = $15,
			outbound_watchlist_found = $16,
			outbound_watchlist_sent = $17,
			watchlist_removals_sent = $18,
			warning = $19,
			error = $20,
			completed_at = $21
		WHERE id = $1::uuid
		RETURNING `+syncRunColumns+`
	`, run.ID, run.Status, run.InboundWatchedFound, run.InboundWatchedImported,
		run.InboundProgressFound, run.InboundProgressImported, run.OutboundFound, run.OutboundSent,
		run.InboundFavoritesFound, run.InboundFavoritesImported, run.OutboundFavoritesFound,
		run.OutboundFavoritesSent, run.FavoriteRemovalsSent,
		run.InboundWatchlistFound, run.InboundWatchlistImported, run.OutboundWatchlistFound,
		run.OutboundWatchlistSent, run.WatchlistRemovalsSent,
		run.Warning, run.Error, run.CompletedAt)
	completed, err := scanSyncRun(row)
	if err != nil {
		return SyncRun{}, fmt.Errorf("complete watch provider sync run: %w", err)
	}
	return completed, nil
}

func (r *PostgresRepository) GetLatestSyncRun(ctx context.Context, connectionID string) (SyncRun, bool, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+syncRunColumns+`
		FROM watch_provider_sync_runs
		WHERE connection_id = $1::uuid
		ORDER BY started_at DESC, created_at DESC
		LIMIT 1
	`, connectionID)
	run, err := scanSyncRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncRun{}, false, nil
	}
	if err != nil {
		return SyncRun{}, false, fmt.Errorf("get latest watch provider sync run: %w", err)
	}
	return run, true, nil
}

func (r *PostgresRepository) GetActiveSyncRun(ctx context.Context, connectionID string) (SyncRun, bool, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+syncRunColumns+`
		FROM watch_provider_sync_runs
		WHERE connection_id = $1::uuid
		  AND status IN ('queued', 'running')
		ORDER BY started_at DESC, created_at DESC
		LIMIT 1
	`, connectionID)
	run, err := scanSyncRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncRun{}, false, nil
	}
	if err != nil {
		return SyncRun{}, false, fmt.Errorf("get active watch provider sync run: %w", err)
	}
	return run, true, nil
}

func (r *PostgresRepository) ListSyncRuns(ctx context.Context, connectionID string, limit int) ([]SyncRun, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+syncRunColumns+`
		FROM watch_provider_sync_runs
		WHERE connection_id = $1::uuid
		ORDER BY started_at DESC, created_at DESC
		LIMIT $2
	`, connectionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list watch provider sync runs: %w", err)
	}
	defer rows.Close()
	var runs []SyncRun
	for rows.Next() {
		run, err := scanSyncRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan watch provider sync run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate watch provider sync runs: %w", err)
	}
	return runs, nil
}

func (r *PostgresRepository) ListLocalWatchEventConnections(
	ctx context.Context,
	userID int,
	profileID string,
	kind LocalWatchEventKind,
) ([]Connection, error) {
	var predicate string
	switch kind {
	case LocalWatchEventMarkedWatched:
		predicate = "export_watched_enabled = true"
	case LocalWatchEventMarkedUnwatched:
		predicate = "export_unwatched_enabled = true"
	default:
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+connectionColumns+`
		FROM watch_provider_connections
		WHERE user_id = $1 AND profile_id = $2 AND `+predicate+`
			AND (rate_limited_until IS NULL OR rate_limited_until <= now())
		ORDER BY provider
	`, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list local watch event connections: %w", err)
	}
	defer rows.Close()

	var conns []Connection
	for rows.Next() {
		conn, scanErr := r.scanConnection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan local watch event connection: %w", scanErr)
		}
		conns = append(conns, conn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local watch event connections: %w", err)
	}
	return conns, nil
}

// ListListEventConnections returns connections that export the given list kind,
// i.e. should mirror a local add/remove on that list to the provider.
func (r *PostgresRepository) ListListEventConnections(
	ctx context.Context,
	userID int,
	profileID string,
	list ListKind,
) ([]Connection, error) {
	column := "export_favorites_enabled"
	if list == ListKindWatchlist {
		column = "export_watchlist_enabled"
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+connectionColumns+`
		FROM watch_provider_connections
		WHERE user_id = $1 AND profile_id = $2 AND `+column+` = true
			AND (rate_limited_until IS NULL OR rate_limited_until <= now())
		ORDER BY provider
	`, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list %s event connections: %w", list, err)
	}
	defer rows.Close()

	var conns []Connection
	for rows.Next() {
		conn, scanErr := r.scanConnection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan %s event connection: %w", list, scanErr)
		}
		conns = append(conns, conn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s event connections: %w", list, err)
	}
	return conns, nil
}

func (r *PostgresRepository) GetMediaDuration(ctx context.Context, mediaItemID string) (float64, error) {
	var duration float64
	err := r.pool.QueryRow(ctx, mediaDurationQuery, mediaItemID).Scan(&duration)
	if err != nil {
		return 0, fmt.Errorf("get media duration: %w", err)
	}
	return duration, nil
}

func (r *PostgresRepository) GetListMediaItems(ctx context.Context, mediaItemIDs []string) (map[string]LocalFavorite, error) {
	result := make(map[string]LocalFavorite, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, type, title, COALESCE(year, 0), COALESCE(imdb_id, ''), COALESCE(tmdb_id, ''), COALESCE(tvdb_id, '')
		FROM media_items
		WHERE content_id = ANY($1)
	`, mediaItemIDs)
	if err != nil {
		return nil, fmt.Errorf("get list media items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fav LocalFavorite
		if err := rows.Scan(&fav.MediaItemID, &fav.Kind, &fav.Title, &fav.Year, &fav.IMDbID, &fav.TMDBID, &fav.TVDBID); err != nil {
			return nil, fmt.Errorf("scan list media item: %w", err)
		}
		fav.ProviderItemKey = providerItemKeyForLocalFavorite(fav)
		result[fav.MediaItemID] = fav
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate list media items: %w", err)
	}
	return result, nil
}

const mediaDurationQuery = `
		SELECT COALESCE(MAX(duration), 0)
		FROM media_files
		WHERE content_id = $1 AND missing_since IS NULL
	`

func (r *PostgresRepository) UpsertHistoryExports(ctx context.Context, exports []HistoryExport) error {
	for _, export := range exports {
		_, err := r.pool.Exec(ctx, `
			INSERT INTO watch_provider_history_exports (
				connection_id, history_id, media_item_id, watched_at, provider_item_key, status,
				attempt_count, last_attempt_at, last_error
			)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (connection_id, history_id) DO UPDATE SET
				provider_item_key = EXCLUDED.provider_item_key,
				status = CASE
					WHEN watch_provider_history_exports.status IN ('sent', 'satisfied_by_scrobble') THEN watch_provider_history_exports.status
					ELSE EXCLUDED.status
				END,
				updated_at = now()
		`, export.ConnectionID, export.HistoryID, export.MediaItemID, export.WatchedAt, export.ProviderItemKey,
			export.Status, export.AttemptCount, export.LastAttemptAt, export.LastError)
		if err != nil {
			return fmt.Errorf("upsert history export: %w", err)
		}
	}
	return nil
}

func (r *PostgresRepository) ListPendingHistoryExports(ctx context.Context, connectionID string, limit int) ([]HistoryExport, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, connection_id::text, history_id, media_item_id, watched_at,
			provider_item_key, status, attempt_count, last_attempt_at, last_error, created_at, updated_at
		FROM watch_provider_history_exports
		WHERE connection_id = $1::uuid
		  AND status IN ('pending', 'failed')
		  AND attempt_count < 5
		ORDER BY watched_at ASC
		LIMIT $2
	`, connectionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending history exports: %w", err)
	}
	defer rows.Close()
	var exports []HistoryExport
	for rows.Next() {
		var export HistoryExport
		if err := rows.Scan(
			&export.ID,
			&export.ConnectionID,
			&export.HistoryID,
			&export.MediaItemID,
			&export.WatchedAt,
			&export.ProviderItemKey,
			&export.Status,
			&export.AttemptCount,
			&export.LastAttemptAt,
			&export.LastError,
			&export.CreatedAt,
			&export.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pending history export: %w", err)
		}
		exports = append(exports, export)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending history exports: %w", err)
	}
	return exports, nil
}

func (r *PostgresRepository) MarkHistoryExportStatus(ctx context.Context, id string, status string, lastError string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE watch_provider_history_exports
		SET status = $2,
			attempt_count = attempt_count + 1,
			last_attempt_at = now(),
			last_error = $3,
			updated_at = now()
		WHERE id = $1::uuid
	`, id, status, lastError)
	if err != nil {
		return fmt.Errorf("mark history export status: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpsertListItemStates(ctx context.Context, states []ListItemState) error {
	for _, state := range states {
		kind := state.ListKind
		if kind == "" {
			kind = ListKindFavorites
		}
		_, err := r.pool.Exec(ctx, `
			INSERT INTO watch_provider_list_items (
				connection_id, list_kind, media_item_id, provider_item_key, kind, title, year,
				remote_present, local_present, last_seen_remote_at, last_seen_local_at,
				last_exported_at, last_removed_remote_at, last_removed_local_at, last_error
			)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (connection_id, list_kind, media_item_id) DO UPDATE SET
				provider_item_key = CASE
					WHEN EXCLUDED.provider_item_key <> '' THEN EXCLUDED.provider_item_key
					ELSE watch_provider_list_items.provider_item_key
				END,
				kind = CASE WHEN EXCLUDED.kind <> '' THEN EXCLUDED.kind ELSE watch_provider_list_items.kind END,
				title = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE watch_provider_list_items.title END,
				year = CASE WHEN EXCLUDED.year <> 0 THEN EXCLUDED.year ELSE watch_provider_list_items.year END,
				remote_present = watch_provider_list_items.remote_present OR EXCLUDED.remote_present,
				local_present = EXCLUDED.local_present,
				last_seen_remote_at = COALESCE(EXCLUDED.last_seen_remote_at, watch_provider_list_items.last_seen_remote_at),
				last_seen_local_at = COALESCE(EXCLUDED.last_seen_local_at, watch_provider_list_items.last_seen_local_at),
				last_exported_at = COALESCE(EXCLUDED.last_exported_at, watch_provider_list_items.last_exported_at),
				last_removed_remote_at = COALESCE(EXCLUDED.last_removed_remote_at, watch_provider_list_items.last_removed_remote_at),
				last_removed_local_at = COALESCE(EXCLUDED.last_removed_local_at, watch_provider_list_items.last_removed_local_at),
				last_error = EXCLUDED.last_error,
				updated_at = now()
		`, state.ConnectionID, string(kind), state.MediaItemID, state.ProviderItemKey, state.Kind, state.Title, state.Year,
			state.RemotePresent, state.LocalPresent, state.LastSeenRemoteAt, state.LastSeenLocalAt,
			state.LastExportedAt, state.LastRemovedRemoteAt, state.LastRemovedLocalAt, state.LastError)
		if err != nil {
			return fmt.Errorf("upsert list item state: %w", err)
		}
	}
	return nil
}

func (r *PostgresRepository) ListListItemStates(ctx context.Context, connectionID string, kind ListKind) ([]ListItemState, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+listItemStateColumns+`
		FROM watch_provider_list_items
		WHERE connection_id = $1::uuid AND list_kind = $2
	`, connectionID, string(kind))
	if err != nil {
		return nil, fmt.Errorf("list list item states: %w", err)
	}
	defer rows.Close()
	return scanListItemStates(rows)
}

func (r *PostgresRepository) ListPendingListItemExports(ctx context.Context, connectionID string, kind ListKind, limit int) ([]ListItemState, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+listItemStateColumns+`
		FROM watch_provider_list_items
		WHERE connection_id = $1::uuid
		  AND list_kind = $2
		  AND local_present = true
		  AND remote_present = false
		  AND last_error = ''
		ORDER BY last_seen_local_at ASC NULLS LAST, created_at ASC
		LIMIT $3
	`, connectionID, string(kind), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending list item exports: %w", err)
	}
	defer rows.Close()
	return scanListItemStates(rows)
}

func (r *PostgresRepository) ListPendingListItemRemovals(ctx context.Context, connectionID string, kind ListKind, limit int) ([]ListItemState, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+listItemStateColumns+`
		FROM watch_provider_list_items
		WHERE connection_id = $1::uuid
		  AND list_kind = $2
		  AND local_present = false
		  AND remote_present = true
		  AND last_error = ''
		ORDER BY last_removed_local_at ASC NULLS LAST, updated_at ASC
		LIMIT $3
	`, connectionID, string(kind), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending list item removals: %w", err)
	}
	defer rows.Close()
	return scanListItemStates(rows)
}

func (r *PostgresRepository) MarkListItemExported(ctx context.Context, connectionID string, kind ListKind, mediaItemID string, exportedAt time.Time) error {
	return r.updateListItemState(ctx, connectionID, kind, mediaItemID, `
		remote_present = true,
		local_present = true,
		last_exported_at = $4,
		last_seen_remote_at = COALESCE(last_seen_remote_at, $4),
		last_error = ''
	`, exportedAt)
}

func (r *PostgresRepository) MarkListItemRemoteRemoved(ctx context.Context, connectionID string, kind ListKind, mediaItemID string, removedAt time.Time) error {
	return r.updateListItemState(ctx, connectionID, kind, mediaItemID, `
		remote_present = false,
		last_removed_remote_at = $4,
		last_error = ''
	`, removedAt)
}

func (r *PostgresRepository) MarkListItemLocalRemoved(ctx context.Context, connectionID string, kind ListKind, mediaItemID string, removedAt time.Time) error {
	return r.updateListItemState(ctx, connectionID, kind, mediaItemID, `
		local_present = false,
		last_removed_local_at = $4,
		last_error = ''
	`, removedAt)
}

func (r *PostgresRepository) MarkListItemError(ctx context.Context, connectionID string, kind ListKind, mediaItemID, lastError string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE watch_provider_list_items
		SET last_error = $4, updated_at = now()
		WHERE connection_id = $1::uuid AND list_kind = $2 AND media_item_id = $3
	`, connectionID, string(kind), mediaItemID, lastError)
	if err != nil {
		return fmt.Errorf("mark list item error: %w", err)
	}
	return nil
}

func (r *PostgresRepository) updateListItemState(ctx context.Context, connectionID string, kind ListKind, mediaItemID, setClause string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE watch_provider_list_items
		SET `+setClause+`,
			updated_at = now()
		WHERE connection_id = $1::uuid AND list_kind = $2 AND media_item_id = $3
	`, connectionID, string(kind), mediaItemID, at)
	if err != nil {
		return fmt.Errorf("update list item state: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListScrobbleConnections(ctx context.Context, userID int, profileID string) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+connectionColumns+`
		FROM watch_provider_connections
		WHERE user_id = $1 AND profile_id = $2 AND scrobble_enabled = true
			AND (rate_limited_until IS NULL OR rate_limited_until <= now())
		ORDER BY provider
	`, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list scrobble connections: %w", err)
	}
	defer rows.Close()
	var conns []Connection
	for rows.Next() {
		conn, err := r.scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan scrobble connection: %w", err)
		}
		conns = append(conns, conn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scrobble connections: %w", err)
	}
	return conns, nil
}

func (r *PostgresRepository) UpsertScrobbleSession(ctx context.Context, event ScrobbleEvent, connectionID string, action string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO watch_provider_scrobble_sessions (
			playback_session_id, connection_id, media_item_id, provider_item_key, kind,
			imdb_id, tmdb_id, tvdb_id, series_imdb_id, series_tmdb_id, series_tvdb_id,
			season_number, episode_number, history_id, started_at, last_progress,
			duration_seconds, completed, last_action, last_error
		)
		VALUES (
			$1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, ''
		)
		ON CONFLICT (playback_session_id, connection_id) DO UPDATE SET
			media_item_id = EXCLUDED.media_item_id,
			provider_item_key = EXCLUDED.provider_item_key,
			kind = EXCLUDED.kind,
			imdb_id = EXCLUDED.imdb_id,
			tmdb_id = EXCLUDED.tmdb_id,
			tvdb_id = EXCLUDED.tvdb_id,
			series_imdb_id = EXCLUDED.series_imdb_id,
			series_tmdb_id = EXCLUDED.series_tmdb_id,
			series_tvdb_id = EXCLUDED.series_tvdb_id,
			season_number = EXCLUDED.season_number,
			episode_number = EXCLUDED.episode_number,
			history_id = COALESCE(NULLIF(EXCLUDED.history_id, ''), watch_provider_scrobble_sessions.history_id),
			last_progress = EXCLUDED.last_progress,
			duration_seconds = EXCLUDED.duration_seconds,
			completed = EXCLUDED.completed,
			last_action = EXCLUDED.last_action,
			last_error = '',
			updated_at = now()
	`, event.PlaybackSessionID, connectionID, event.MediaItemID, event.ProviderItemKey, event.Kind,
		event.IMDbID, event.TMDBID, event.TVDBID, event.SeriesIMDbID, event.SeriesTMDBID,
		event.SeriesTVDBID, event.SeasonNumber, event.EpisodeNumber, event.HistoryID,
		event.OccurredAt, event.PositionSeconds, event.DurationSeconds, event.Completed, action)
	if err != nil {
		return fmt.Errorf("upsert scrobble session: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateScrobbleSession(ctx context.Context, playbackSessionID string, connectionID string, action string, progress float64, historyID string, lastError string, stopSentAt *time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE watch_provider_scrobble_sessions
		SET last_action = $3,
			last_progress = $4,
			history_id = COALESCE(NULLIF($5, ''), history_id),
			last_error = $6,
			stop_sent_at = COALESCE($7, stop_sent_at),
			updated_at = now()
		WHERE playback_session_id = $1 AND connection_id = $2::uuid
	`, playbackSessionID, connectionID, action, progress, historyID, lastError, stopSentAt)
	if err != nil {
		return fmt.Errorf("update scrobble session: %w", err)
	}
	return nil
}

func (r *PostgresRepository) PrepareConfirmedScrobbleStop(ctx context.Context, event ScrobbleEvent, connectionID string, staleBefore time.Time) (confirmedStopPreparation, time.Time, error) {
	var claimVersion time.Time
	err := r.pool.QueryRow(ctx, `
		INSERT INTO watch_provider_scrobble_sessions (
			playback_session_id, connection_id, media_item_id, provider_item_key, kind,
			imdb_id, tmdb_id, tvdb_id, series_imdb_id, series_tmdb_id, series_tvdb_id,
			season_number, episode_number, history_id, started_at, last_progress,
			duration_seconds, completed, last_action, last_error, stop_sent_at
		)
		VALUES (
			$1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, 'stop_confirming', '', NULL
		)
		ON CONFLICT (playback_session_id, connection_id) DO NOTHING
		RETURNING updated_at
	`, event.PlaybackSessionID, connectionID, event.MediaItemID, event.ProviderItemKey, event.Kind,
		event.IMDbID, event.TMDBID, event.TVDBID, event.SeriesIMDbID, event.SeriesTMDBID,
		event.SeriesTVDBID, event.SeasonNumber, event.EpisodeNumber, event.HistoryID,
		event.OccurredAt, event.PositionSeconds, event.DurationSeconds, event.Completed).Scan(&claimVersion)
	if err == nil {
		return confirmedStopPrepared, claimVersion, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return confirmedStopInProgress, time.Time{}, fmt.Errorf("insert confirmed scrobble stop: %w", err)
	}

	err = r.pool.QueryRow(ctx, `
		UPDATE watch_provider_scrobble_sessions
		SET last_action = 'stop_confirming',
			last_progress = $3,
			history_id = COALESCE(NULLIF($4, ''), history_id),
			duration_seconds = $5,
			completed = $6,
			last_error = '',
			stop_sent_at = NULL,
			updated_at = now()
		WHERE playback_session_id = $1 AND connection_id = $2::uuid
			-- A non-null stop_sent_at can be the provisional ActiveEncodings
			-- fallback. Confirmed delivery must replace it with the later
			-- authoritative Stopped position, so last_action owns deduplication.
			AND last_action IS DISTINCT FROM 'stop_confirmed'
			AND (last_action IS DISTINCT FROM 'stop_confirming' OR updated_at <= $7)
		RETURNING updated_at
	`, event.PlaybackSessionID, connectionID, event.PositionSeconds, event.HistoryID,
		event.DurationSeconds, event.Completed, staleBefore).Scan(&claimVersion)
	if err == nil {
		return confirmedStopPrepared, claimVersion, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return confirmedStopInProgress, time.Time{}, fmt.Errorf("prepare confirmed scrobble stop: %w", err)
	}

	var lastAction string
	if err := r.pool.QueryRow(ctx, `
		SELECT last_action
		FROM watch_provider_scrobble_sessions
		WHERE playback_session_id = $1 AND connection_id = $2::uuid
	`, event.PlaybackSessionID, connectionID).Scan(&lastAction); err != nil {
		return confirmedStopInProgress, time.Time{}, fmt.Errorf("read confirmed scrobble stop state: %w", err)
	}
	if lastAction == "stop_confirmed" {
		return confirmedStopAlreadySent, time.Time{}, nil
	}
	return confirmedStopInProgress, time.Time{}, nil
}

func (r *PostgresRepository) CompleteConfirmedScrobbleStop(ctx context.Context, playbackSessionID string, connectionID string, progress float64, historyID string, claimVersion time.Time, stopSentAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE watch_provider_scrobble_sessions
		SET last_action = 'stop_confirmed',
			last_progress = $3,
			history_id = COALESCE(NULLIF($4, ''), history_id),
			last_error = '',
			stop_sent_at = $5,
			updated_at = now()
		WHERE playback_session_id = $1 AND connection_id = $2::uuid
			AND last_action = 'stop_confirming' AND updated_at = $6
	`, playbackSessionID, connectionID, progress, historyID, stopSentAt, claimVersion)
	if err != nil {
		return fmt.Errorf("complete confirmed scrobble stop: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errConfirmedStopClaimLost
	}
	return nil
}

func (r *PostgresRepository) FailConfirmedScrobbleStop(ctx context.Context, playbackSessionID string, connectionID string, progress float64, historyID string, claimVersion time.Time, lastError string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE watch_provider_scrobble_sessions
		SET last_action = 'stop_retry',
			last_progress = $3,
			history_id = COALESCE(NULLIF($4, ''), history_id),
			last_error = $6,
			updated_at = now()
		WHERE playback_session_id = $1 AND connection_id = $2::uuid
			AND last_action = 'stop_confirming' AND updated_at = $5
	`, playbackSessionID, connectionID, progress, historyID, claimVersion, lastError)
	if err != nil {
		return fmt.Errorf("fail confirmed scrobble stop: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errConfirmedStopClaimLost
	}
	return nil
}

func (r *PostgresRepository) ListOpenScrobbleSessions(ctx context.Context) ([]ScrobbleSession, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT playback_session_id, connection_id::text, media_item_id, provider_item_key, kind,
			imdb_id, tmdb_id, tvdb_id, series_imdb_id, series_tmdb_id, series_tvdb_id,
			season_number, episode_number, history_id, started_at, last_progress,
			duration_seconds, completed, last_action, stop_sent_at, last_error
		FROM watch_provider_scrobble_sessions
		WHERE stop_sent_at IS NULL
			AND last_action NOT IN ('stop_confirming', 'stop_retry')
		ORDER BY started_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list open scrobble sessions: %w", err)
	}
	defer rows.Close()
	var sessions []ScrobbleSession
	for rows.Next() {
		var session ScrobbleSession
		if err := rows.Scan(
			&session.PlaybackSessionID,
			&session.ConnectionID,
			&session.MediaItemID,
			&session.ProviderItemKey,
			&session.Kind,
			&session.IMDbID,
			&session.TMDBID,
			&session.TVDBID,
			&session.SeriesIMDbID,
			&session.SeriesTMDBID,
			&session.SeriesTVDBID,
			&session.SeasonNumber,
			&session.EpisodeNumber,
			&session.HistoryID,
			&session.StartedAt,
			&session.LastProgress,
			&session.DurationSeconds,
			&session.Completed,
			&session.LastAction,
			&session.StopSentAt,
			&session.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan open scrobble session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open scrobble sessions: %w", err)
	}
	return sessions, nil
}

func scanDeviceAuthSession(row pgx.Row) (DeviceAuthSession, error) {
	var session DeviceAuthSession
	err := row.Scan(
		&session.ID,
		&session.Provider,
		&session.UserID,
		&session.ProfileID,
		&session.DeviceCode,
		&session.UserCode,
		&session.VerificationURL,
		&session.IntervalSeconds,
		&session.ExpiresAt,
		&session.CompletedAt,
	)
	if err != nil {
		return DeviceAuthSession{}, err
	}
	return session, nil
}

func scanSyncRun(row pgx.Row) (SyncRun, error) {
	var run SyncRun
	err := row.Scan(
		&run.ID,
		&run.ConnectionID,
		&run.Trigger,
		&run.Status,
		&run.Provider,
		&run.InboundWatchedFound,
		&run.InboundWatchedImported,
		&run.InboundProgressFound,
		&run.InboundProgressImported,
		&run.OutboundFound,
		&run.OutboundSent,
		&run.InboundFavoritesFound,
		&run.InboundFavoritesImported,
		&run.OutboundFavoritesFound,
		&run.OutboundFavoritesSent,
		&run.FavoriteRemovalsSent,
		&run.InboundWatchlistFound,
		&run.InboundWatchlistImported,
		&run.OutboundWatchlistFound,
		&run.OutboundWatchlistSent,
		&run.WatchlistRemovalsSent,
		&run.Warning,
		&run.Error,
		&run.StartedAt,
		&run.CompletedAt,
		&run.CreatedAt,
	)
	if err != nil {
		return SyncRun{}, err
	}
	return run, nil
}

func (r *PostgresRepository) scanConnection(row pgx.Row) (Connection, error) {
	var conn Connection
	var rawSyncCursors []byte
	err := row.Scan(
		&conn.ID,
		&conn.Provider,
		&conn.UserID,
		&conn.ProfileID,
		&conn.ProviderAccountID,
		&conn.ProviderUsername,
		&conn.AccessToken,
		&conn.RefreshToken,
		&conn.TokenExpiresAt,
		&conn.ImportWatchedEnabled,
		&conn.ImportProgressEnabled,
		&conn.ExportWatchedEnabled,
		&conn.ExportUnwatchedEnabled,
		&conn.ImportFavoritesEnabled,
		&conn.ExportFavoritesEnabled,
		&conn.SyncFavoriteRemovalsEnabled,
		&conn.ImportWatchlistEnabled,
		&conn.ExportWatchlistEnabled,
		&conn.SyncWatchlistRemovalsEnabled,
		&conn.SyncWatchlistOrderEnabled,
		&conn.ScrobbleEnabled,
		&conn.LastInboundSyncAt,
		&conn.LastProgressSyncAt,
		&conn.LastOutboundSyncAt,
		&conn.LastFavoritesSyncAt,
		&conn.LastWatchlistSyncAt,
		&conn.LastScrobbleErrorAt,
		&conn.LastError,
		&conn.RateLimitedUntil,
		&rawSyncCursors,
		&conn.CreatedAt,
		&conn.UpdatedAt,
	)
	if err != nil {
		return Connection{}, err
	}
	// Decrypt the tokens (read-path contract), bound to the connection's stable
	// business key — matching TokenAAD on the write path.
	if conn.AccessToken, err = r.cipher.DecryptIfEncrypted(conn.AccessToken, TokenAAD("access_token", conn.Provider, conn.UserID, conn.ProfileID)); err != nil {
		return Connection{}, fmt.Errorf("decrypt watch access token: %w", err)
	}
	if conn.RefreshToken, err = r.cipher.DecryptIfEncrypted(conn.RefreshToken, TokenAAD("refresh_token", conn.Provider, conn.UserID, conn.ProfileID)); err != nil {
		return Connection{}, fmt.Errorf("decrypt watch refresh token: %w", err)
	}
	conn.SyncCursors = decodeSyncCursors(rawSyncCursors)
	return conn, nil
}

type listItemStateRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanListItemStates(rows listItemStateRows) ([]ListItemState, error) {
	var states []ListItemState
	for rows.Next() {
		var state ListItemState
		if err := rows.Scan(
			&state.ID,
			&state.ConnectionID,
			&state.ListKind,
			&state.MediaItemID,
			&state.ProviderItemKey,
			&state.Kind,
			&state.Title,
			&state.Year,
			&state.RemotePresent,
			&state.LocalPresent,
			&state.LastSeenRemoteAt,
			&state.LastSeenLocalAt,
			&state.LastExportedAt,
			&state.LastRemovedRemoteAt,
			&state.LastRemovedLocalAt,
			&state.LastError,
			&state.CreatedAt,
			&state.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan list item state: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate list item states: %w", err)
	}
	return states, nil
}

func encodeSyncCursors(cursors map[string]string) []byte {
	if len(cursors) == 0 {
		return []byte(`{}`)
	}
	data, err := json.Marshal(cursors)
	if err != nil {
		return []byte(`{}`)
	}
	return data
}

func decodeSyncCursors(data []byte) map[string]string {
	if len(data) == 0 {
		return map[string]string{}
	}
	var cursors map[string]string
	if err := json.Unmarshal(data, &cursors); err != nil || cursors == nil {
		return map[string]string{}
	}
	return cursors
}
