package autoscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

type Repository struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

func NewRepository(pool *pgxpool.Pool, cipher *secret.Cipher) *Repository {
	return &Repository{pool: pool, cipher: cipher}
}

// connectionAPIKeyAAD binds an autoscan_connections api_key_ref ciphertext to
// its row id.
func connectionAPIKeyAAD(id string) string {
	return secret.RowAAD("autoscan_connections", "api_key_ref", id)
}

// encryptAPIKey encrypts a non-empty, trimmed key bound to the connection id;
// an empty key returns "" so NULL/keep-existing semantics are preserved.
func (r *Repository) encryptAPIKey(id, apiKey string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", nil
	}
	return r.cipher.Encrypt(apiKey, connectionAPIKeyAAD(id))
}

// --- Settings ---

func (r *Repository) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx, `
		SELECT enabled, default_poll_interval_seconds, debounce_seconds
		FROM autoscan_settings WHERE id = true`).
		Scan(&s.Enabled, &s.DefaultPollIntervalSeconds, &s.DebounceSeconds)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Settings{
				Enabled:                    false,
				DefaultPollIntervalSeconds: 600,
				DebounceSeconds:            60,
			}, nil
		}
		return Settings{}, fmt.Errorf("get autoscan settings: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSettings(ctx context.Context, s Settings) (Settings, error) {
	var out Settings
	err := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_settings (
			id, enabled, default_poll_interval_seconds, debounce_seconds, updated_at
		)
		VALUES (true, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			default_poll_interval_seconds = EXCLUDED.default_poll_interval_seconds,
			debounce_seconds = EXCLUDED.debounce_seconds,
			updated_at = now()
		RETURNING enabled, default_poll_interval_seconds, debounce_seconds`,
		s.Enabled, s.DefaultPollIntervalSeconds, s.DebounceSeconds).
		Scan(&out.Enabled, &out.DefaultPollIntervalSeconds, &out.DebounceSeconds)
	if err != nil {
		return Settings{}, fmt.Errorf("update autoscan settings: %w", err)
	}
	return out, nil
}

// --- Connections ---

const connectionColumns = `id, name, kind, base_url, api_key_ref, request_integration_id`

func (r *Repository) scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var baseURL, apiKeyRef, reqIntegrationID *string
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &baseURL, &apiKeyRef, &reqIntegrationID); err != nil {
		return Connection{}, err
	}
	if baseURL != nil {
		c.BaseURL = *baseURL
	}
	if apiKeyRef != nil {
		c.APIKeyRef = *apiKeyRef
	}
	c.RequestIntegrationID = reqIntegrationID
	// Decrypt the stored key (read-path contract): legacy plaintext passes
	// through, enc:v1: decrypts, corrupt ciphertext errors.
	apiKey, err := r.cipher.DecryptIfEncrypted(c.APIKeyRef, connectionAPIKeyAAD(c.ID))
	if err != nil {
		return Connection{}, fmt.Errorf("decrypt autoscan connection %s api key: %w", c.ID, err)
	}
	c.APIKeyRef = apiKey
	return c, nil
}

// nullable returns nil for empty strings so they map to SQL NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *Repository) CreateConnection(ctx context.Context, c Connection) (Connection, error) {
	// Generate the id in Go (rather than the DB default) so the api_key_ref
	// ciphertext can be AAD-bound to the row id at insert time.
	id := strings.TrimSpace(c.ID)
	if id == "" {
		id = uuid.NewString()
	}
	apiKeyRef, err := r.encryptAPIKey(id, c.APIKeyRef)
	if err != nil {
		return Connection{}, fmt.Errorf("encrypt autoscan api key: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_connections (id, name, kind, base_url, api_key_ref, request_integration_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+connectionColumns,
		id, c.Name, c.Kind, nullable(c.BaseURL), nullable(apiKeyRef), c.RequestIntegrationID)
	out, err := r.scanConnection(row)
	if err != nil {
		return Connection{}, fmt.Errorf("create autoscan connection: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateConnection(ctx context.Context, c Connection) (Connection, error) {
	if err := missingID("connection", c.ID); err != nil {
		return Connection{}, err
	}
	// A blank incoming api_key_ref KEEPS the existing stored value: the UI
	// deliberately omits the key on a metadata-only edit ("leave blank to keep
	// existing"), so unconditionally writing it would NULL the key and break the
	// next poll. Mirrors requests.UpdateIntegration's CASE-WHEN keep-semantics.
	// Pass the raw trimmed string (not nullable()) so the empty-string sentinel
	// reaches the CASE.
	// Encrypt the incoming key; an empty result preserves the keep-existing CASE.
	apiKeyRef, err := r.encryptAPIKey(c.ID, c.APIKeyRef)
	if err != nil {
		return Connection{}, fmt.Errorf("encrypt autoscan api key: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Connection{}, fmt.Errorf("begin autoscan connection update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Read the upstream identity under a row lock so the comparison below and
	// the marker reset see the same before-image as the update that replaces it.
	var old connectionUpstream
	if err := tx.QueryRow(ctx, `
		SELECT base_url, request_integration_id
		FROM autoscan_connections
		WHERE id = $1
		FOR UPDATE`, c.ID).Scan(&old.baseURL, &old.integrationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, fmt.Errorf("%w: connection %s", ErrNotFound, c.ID)
		}
		return Connection{}, fmt.Errorf("read autoscan connection: %w", err)
	}

	row := tx.QueryRow(ctx, `
		UPDATE autoscan_connections
		SET name = $2, kind = $3, base_url = $4,
		    api_key_ref = CASE WHEN $5 = '' THEN api_key_ref ELSE $5 END,
		    request_integration_id = $6, updated_at = now()
		WHERE id = $1
		RETURNING `+connectionColumns,
		c.ID, c.Name, c.Kind, nullable(c.BaseURL), apiKeyRef, c.RequestIntegrationID)
	out, err := r.scanConnection(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, fmt.Errorf("%w: connection %s", ErrNotFound, c.ID)
		}
		return Connection{}, fmt.Errorf("update autoscan connection: %w", err)
	}

	// A source's marker is a continuation token into the server this
	// connection points at. Repointing the connection (another URL, or another
	// linked Requests integration) makes every bound source's marker refer to a
	// different upstream, so they restart from now, matching UpdateSource.
	// Rotating the API key, renaming or relabelling the kind keeps them.
	// A source without a marker (its first poll failed, for example) is
	// reset too, so it retries the new upstream at the next cycle.
	if old.differsFrom(out) {
		if _, err := tx.Exec(ctx, `
			UPDATE autoscan_sources
			SET marker = NULL, last_run_at = NULL, updated_at = now()
			WHERE connection_id = $1 AND (marker IS NOT NULL OR last_run_at IS NOT NULL)`, c.ID); err != nil {
			return Connection{}, fmt.Errorf("reset autoscan source markers: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Connection{}, fmt.Errorf("commit autoscan connection update: %w", err)
	}
	return out, nil
}

// connectionUpstream is the part of a stored connection row that decides
// which server a bound source's marker points into. UpdateConnection resets
// markers when it changes, and AdvanceMarker refuses a poll's marker when it
// changed since the poll read the connection.
type connectionUpstream struct {
	baseURL       *string
	integrationID *string
}

// differsFrom reports whether c points at a different upstream than u. It
// compares what ConnectionResolver.Resolve hands the plugin: a linked Requests
// integration when there is one, otherwise the row's own base URL. The kind
// never reaches the plugin, and a linked row's stored base URL is ignored by
// Resolve, so changing either leaves the upstream (and the markers) alone.
func (u connectionUpstream) differsFrom(c Connection) bool {
	oldLink, newLink := linkedIntegration(u.integrationID), linkedIntegration(c.RequestIntegrationID)
	if oldLink != "" || newLink != "" {
		return oldLink != newLink
	}
	return comparableBaseURL(derefString(u.baseURL)) != comparableBaseURL(c.BaseURL)
}

// comparableBaseURL is the form in which two base URLs name the same server:
// the scheme and host are not case-sensitive, and a trailing slash or
// surrounding space changes nothing. Resetting markers for such an edit
// would skip whatever the upstream imports before the next poll.
func comparableBaseURL(raw string) string {
	value := strings.TrimSpace(raw)
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return strings.TrimRight(value, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	// Trim the escaped form so an escaped slash (%2F) still tells two paths
	// apart, including one at the end of the path.
	escaped := strings.TrimRight(u.EscapedPath(), "/")
	u.Path, _ = url.PathUnescape(escaped)
	u.RawPath = escaped
	return u.String()
}

// linkedIntegration returns the Requests integration a connection resolves
// through, or "" when it uses its own fields. It matches Resolve, which treats
// a blank link as no link.
func linkedIntegration(id *string) string {
	return strings.TrimSpace(derefString(id))
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (r *Repository) DeleteConnection(ctx context.Context, id string) error {
	if err := missingID("connection", id); err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM autoscan_connections WHERE id = $1`, id)
	if err != nil {
		// A source still references this connection (ON DELETE RESTRICT, 23503).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return fmt.Errorf("autoscan: connection %s is in use by a source", id)
		}
		return fmt.Errorf("delete autoscan connection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: connection %s", ErrNotFound, id)
	}
	return nil
}

func (r *Repository) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+connectionColumns+`
		FROM autoscan_connections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list autoscan connections: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := r.scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) GetConnection(ctx context.Context, id string) (Connection, error) {
	if err := missingID("connection", id); err != nil {
		return Connection{}, err
	}
	row := r.pool.QueryRow(ctx, `SELECT `+connectionColumns+`
		FROM autoscan_connections WHERE id = $1`, id)
	c, err := r.scanConnection(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, fmt.Errorf("%w: connection %s", ErrNotFound, id)
		}
		return Connection{}, fmt.Errorf("get autoscan connection: %w", err)
	}
	return c, nil
}

// --- Sources ---

const sourceColumns = `id, plugin_id, capability_id, connection_id, enabled, delivery_mode,
	poll_interval_seconds, path_rewrites, source_config, label, marker, last_run_at, last_error`

func scanSource(row interface{ Scan(...any) error }) (Source, error) {
	var s Source
	var pathRewrites []byte
	var sourceConfig []byte
	if err := row.Scan(&s.ID, &s.PluginID, &s.CapabilityID, &s.ConnectionID,
		&s.Enabled, &s.DeliveryMode, &s.PollIntervalSeconds, &pathRewrites, &sourceConfig, &s.Label, &s.Marker, &s.LastRunAt, &s.LastError); err != nil {
		return Source{}, err
	}
	rewrites, err := unmarshalPathRewrites(pathRewrites)
	if err != nil {
		return Source{}, err
	}
	s.PathRewrites = rewrites
	config, err := unmarshalSourceConfig(sourceConfig)
	if err != nil {
		return Source{}, err
	}
	s.SourceConfig = config
	return s, nil
}

// unmarshalPathRewrites decodes the jsonb path_rewrites column into a slice. A
// NULL/empty column maps to an empty (non-nil) slice so callers never see a nil.
func unmarshalPathRewrites(raw []byte) ([]PathRewrite, error) {
	if len(raw) == 0 {
		return []PathRewrite{}, nil
	}
	var out []PathRewrite
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode autoscan path_rewrites: %w", err)
	}
	if out == nil {
		out = []PathRewrite{}
	}
	return out, nil
}

// marshalPathRewrites encodes path rewrites for the jsonb column. A nil slice is
// stored as an empty JSON array (matching the column default '[]').
func marshalPathRewrites(rewrites []PathRewrite) ([]byte, error) {
	if rewrites == nil {
		rewrites = []PathRewrite{}
	}
	b, err := json.Marshal(rewrites)
	if err != nil {
		return nil, fmt.Errorf("encode autoscan path_rewrites: %w", err)
	}
	return b, nil
}

func unmarshalSourceConfig(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode autoscan source_config: %w", err)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

func marshalSourceConfig(config map[string]string) ([]byte, error) {
	normalized := make(map[string]string, len(config))
	for key, value := range config {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		normalized[key] = strings.TrimSpace(value)
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode autoscan source_config: %w", err)
	}
	return b, nil
}

// connectionIDArg maps a nullable connection id to a SQL value: nil pointer and
// whitespace-only ids map to SQL NULL.
func connectionIDArg(id *string) any {
	if id == nil {
		return nil
	}
	return nullable(*id)
}

// deliveryModeArg normalizes an unset delivery mode to the poll default so
// pre-webhook callers keep today's behavior.
func deliveryModeArg(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return DeliveryModePoll
	}
	return mode
}

// CreateSource inserts a new autoscan source row. One installed scan_source
// capability can back many sources (e.g. one Sonarr plugin install fronting 4
// arr servers, one source per connection), so this is a plain INSERT with a
// fresh uuid rather than an upsert. A non-existent connection trips the FK
// constraint and maps to ErrNotFound.
func (r *Repository) CreateSource(ctx context.Context, s Source) (Source, error) {
	if err := missingConnectionID(s.ConnectionID); err != nil {
		return Source{}, err
	}
	rewrites, err := marshalPathRewrites(s.PathRewrites)
	if err != nil {
		return Source{}, err
	}
	sourceConfig, err := marshalSourceConfig(s.SourceConfig)
	if err != nil {
		return Source{}, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_sources (
			plugin_id, capability_id, connection_id, enabled, delivery_mode, poll_interval_seconds, path_rewrites, source_config, label
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+sourceColumns,
		s.PluginID, s.CapabilityID, connectionIDArg(s.ConnectionID), s.Enabled, deliveryModeArg(s.DeliveryMode), s.PollIntervalSeconds, rewrites, sourceConfig, s.Label)
	out, err := scanSource(row)
	if err != nil {
		if connID, ok := connectionFKViolation(err, s.ConnectionID); ok {
			return Source{}, fmt.Errorf("%w: connection %s", ErrNotFound, connID)
		}
		return Source{}, fmt.Errorf("create autoscan source: %w", err)
	}
	return out, nil
}

// UpdateSource updates a source's binding/scheduling fields by id. Identity
// (plugin_id, capability_id) and last_error are left untouched, and
// last_run_at only changes with a marker reset (below). An unknown id maps to
// ErrNotFound; a non-existent connection trips the FK constraint and also maps
// to ErrNotFound.
//
// The stored marker is kept unless the update changes what it points into. A
// marker is the plugin's opaque continuation token for one upstream, so it is
// cleared when the bound connection or the plugin's source_config changes:
// handing Sonarr's marker to Radarr would replay or skip that server's history.
// An empty marker tells the plugin to start from now. last_run_at is cleared
// with it, so the next cycle polls the source at once instead of waiting out
// its interval: changes the upstream makes before that first poll are not
// reported. Label, enabled, delivery mode, interval and path rewrites only
// change how the host treats results, so they keep both. The comparison runs
// inside the UPDATE against the row's current values, so a concurrent update
// cannot slip between a read and the write.
func (r *Repository) UpdateSource(ctx context.Context, s Source) (Source, error) {
	if err := missingID("source", s.ID); err != nil {
		return Source{}, err
	}
	if err := missingConnectionID(s.ConnectionID); err != nil {
		return Source{}, err
	}
	rewrites, err := marshalPathRewrites(s.PathRewrites)
	if err != nil {
		return Source{}, err
	}
	sourceConfig, err := marshalSourceConfig(s.SourceConfig)
	if err != nil {
		return Source{}, err
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE autoscan_sources
		SET connection_id = $2,
		    enabled = $3,
		    delivery_mode = $4,
		    poll_interval_seconds = $5,
		    path_rewrites = $6,
		    source_config = $7,
		    label = $8,
		    marker = CASE
		        WHEN connection_id IS DISTINCT FROM $2::uuid
		          OR source_config IS DISTINCT FROM $7::jsonb
		        THEN NULL
		        ELSE marker
		    END,
		    last_run_at = CASE
		        WHEN connection_id IS DISTINCT FROM $2::uuid
		          OR source_config IS DISTINCT FROM $7::jsonb
		        THEN NULL
		        ELSE last_run_at
		    END,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+sourceColumns,
		s.ID, connectionIDArg(s.ConnectionID), s.Enabled, deliveryModeArg(s.DeliveryMode), s.PollIntervalSeconds, rewrites, sourceConfig, s.Label)
	out, err := scanSource(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Source{}, fmt.Errorf("%w: source %s", ErrNotFound, s.ID)
		}
		if connID, ok := connectionFKViolation(err, s.ConnectionID); ok {
			return Source{}, fmt.Errorf("%w: connection %s", ErrNotFound, connID)
		}
		return Source{}, fmt.Errorf("update autoscan source: %w", err)
	}
	return out, nil
}

// connectionFKViolation reports whether err is the connection FK constraint
// violation (23503), returning the offending connection id for the error
// message.
func connectionFKViolation(err error, connectionID *string) (string, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return "", false
	}
	connID := ""
	if connectionID != nil {
		connID = *connectionID
	}
	return connID, true
}

func (r *Repository) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sourceColumns+`
		FROM autoscan_sources ORDER BY plugin_id, capability_id`)
	if err != nil {
		return nil, fmt.Errorf("list autoscan sources: %w", err)
	}
	defer rows.Close()
	return collectSources(rows)
}

func (r *Repository) ListEnabledSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sourceColumns+`
		FROM autoscan_sources WHERE enabled = true
		ORDER BY plugin_id, capability_id`)
	if err != nil {
		return nil, fmt.Errorf("list enabled autoscan sources: %w", err)
	}
	defer rows.Close()
	return collectSources(rows)
}

func collectSources(rows pgx.Rows) ([]Source, error) {
	var out []Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) GetSource(ctx context.Context, id string) (Source, error) {
	if err := missingID("source", id); err != nil {
		return Source{}, err
	}
	row := r.pool.QueryRow(ctx, `SELECT `+sourceColumns+`
		FROM autoscan_sources WHERE id = $1`, id)
	s, err := scanSource(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Source{}, fmt.Errorf("%w: source %s", ErrNotFound, id)
		}
		return Source{}, fmt.Errorf("get autoscan source: %w", err)
	}
	return s, nil
}

// DeleteSource removes a source row by id. It lets an operator clear an orphaned
// source (one whose scan_source plugin was uninstalled/disabled). An unknown id
// maps to ErrNotFound.
func (r *Repository) DeleteSource(ctx context.Context, id string) error {
	if err := missingID("source", id); err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM autoscan_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete autoscan source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: source %s", ErrNotFound, id)
	}
	return nil
}

// MarkerAdvance is one poll's request to store its next marker, together with
// the rows the poll read. The marker is only valid for that state: a different
// starting marker, connection binding, source_config, or connection upstream
// means an admin reset the marker while the poll ran.
type MarkerAdvance struct {
	// Source is the source row the poll used. Its ID, Marker, ConnectionID and
	// SourceConfig are the snapshot the write is compared against.
	Source Source
	// Connection is the connection row the poll resolved. It is required when
	// Source.ConnectionID is set and must be that connection.
	Connection *Connection
	NextMarker string
}

// AdvanceMarker stores the opaque next marker for a source, stamps last_run_at,
// and clears any prior error. Called once a poll window's work is consumed —
// after a successful enqueue, or when the window's paths all resolved outside
// Silo's libraries and were advanced past.
//
// The write is a compare-and-swap against adv's snapshot. UpdateSource and
// UpdateConnection clear the marker when the upstream changes; a poll that
// was already running would otherwise write the old upstream's marker over
// that reset. When the snapshot no longer matches, nothing is written and
// AdvanceMarker returns false: the next poll starts from the reset marker.
// A skipped write also leaves last_run_at and last_error as they were, so the
// next poll cycle polls the source against its new upstream without waiting
// for its interval. The connection row is read FOR SHARE first, so a
// concurrent connection update either commits before the check or waits for
// this write and then clears it, matching UpdateConnection's lock order.
func (r *Repository) AdvanceMarker(ctx context.Context, adv MarkerAdvance) (bool, error) {
	src := adv.Source
	if src.ConnectionID != nil && (adv.Connection == nil || adv.Connection.ID != *src.ConnectionID) {
		return false, fmt.Errorf("advance autoscan marker: source %s: the poll's connection row is missing or does not match its binding", src.ID)
	}
	sourceConfig, err := sourceConfigSnapshot(src.SourceConfig)
	if err != nil {
		return false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin autoscan marker advance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if src.ConnectionID != nil {
		var current connectionUpstream
		err := tx.QueryRow(ctx, `
			SELECT base_url, request_integration_id
			FROM autoscan_connections
			WHERE id = $1
			FOR SHARE`, adv.Connection.ID).Scan(&current.baseURL, &current.integrationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read autoscan connection for marker advance: %w", err)
		}
		if current.differsFrom(*adv.Connection) {
			return false, nil
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE autoscan_sources
		SET marker = $2, last_run_at = now(), last_error = NULL, updated_at = now()
		WHERE id = $1
		  AND COALESCE(marker, '') = $3
		  AND connection_id IS NOT DISTINCT FROM $4::uuid
		  AND source_config = $5::jsonb`,
		src.ID, nullable(adv.NextMarker), derefString(src.Marker), connectionIDArg(src.ConnectionID), sourceConfig)
	if err != nil {
		return false, fmt.Errorf("advance autoscan marker: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM autoscan_sources WHERE id = $1)`, src.ID).Scan(&exists); err != nil {
			return false, fmt.Errorf("check autoscan source: %w", err)
		}
		if !exists {
			return false, fmt.Errorf("%w: source %s", ErrNotFound, src.ID)
		}
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit autoscan marker advance: %w", err)
	}
	return true, nil
}

// sourceConfigSnapshot encodes a source_config map exactly as it was read, for
// comparison with the stored jsonb. Unlike marshalSourceConfig it does not
// normalize, so a stored value that predates normalization still matches the
// map decoded from it and the poll can keep advancing.
func sourceConfigSnapshot(config map[string]string) ([]byte, error) {
	if config == nil {
		config = map[string]string{}
	}
	b, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode autoscan source_config snapshot: %w", err)
	}
	return b, nil
}

// maxLastErrorLen bounds the stored last_error (in bytes) so a pathological
// provider error can't bloat the row.
const maxLastErrorLen = 2048

// truncateUTF8 caps s to at most maxBytes bytes without splitting a multi-byte
// UTF-8 rune: if the byte cut lands mid-rune it backs off to the last valid
// rune boundary, so the stored value is always valid UTF-8.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// RecordError records a poll failure for a source: it stamps last_run_at and
// stores the (length-bounded) error message without advancing the marker.
func (r *Repository) RecordError(ctx context.Context, sourceID, msg string) error {
	msg = truncateUTF8(msg, maxLastErrorLen)
	tag, err := r.pool.Exec(ctx, `
		UPDATE autoscan_sources
		SET last_error = $2, last_run_at = now(), updated_at = now()
		WHERE id = $1`, sourceID, msg)
	if err != nil {
		return fmt.Errorf("record autoscan error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: source %s", ErrNotFound, sourceID)
	}
	return nil
}

const eventColumns = `id, source_id, plugin_id, capability_id, started_at, completed_at,
	duration_ms, status, delivery_mode, provider_event_type, changes_returned, changes_resolved,
	targets_claimed, scans_created, scans_reused, scans_suppressed, error_message, marker_before, marker_after,
	change_log, change_log_truncated`

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var e Event
	var status string
	var changeLog []byte
	if err := row.Scan(
		&e.ID,
		&e.SourceID,
		&e.PluginID,
		&e.CapabilityID,
		&e.StartedAt,
		&e.CompletedAt,
		&e.DurationMS,
		&status,
		&e.DeliveryMode,
		&e.ProviderEventType,
		&e.ChangesReturned,
		&e.ChangesResolved,
		&e.TargetsClaimed,
		&e.ScansCreated,
		&e.ScansReused,
		&e.ScansSuppressed,
		&e.ErrorMessage,
		&e.MarkerBefore,
		&e.MarkerAfter,
		&changeLog,
		&e.ChangesTruncated,
	); err != nil {
		return Event{}, err
	}
	e.Status = EventStatus(status)
	e.Changes = decodeChangeLog(changeLog)
	return e, nil
}

// decodeChangeLog reads a stored change log. The log is diagnostic, so an
// unreadable value degrades to an empty log rather than failing the listing.
func decodeChangeLog(raw []byte) []ChangeRecord {
	records := []ChangeRecord{}
	if len(raw) == 0 {
		return records
	}
	if err := json.Unmarshal(raw, &records); err != nil || records == nil {
		return []ChangeRecord{}
	}
	return records
}

func encodeChangeLog(records []ChangeRecord) ([]byte, error) {
	if records == nil {
		records = []ChangeRecord{}
	}
	return json.Marshal(records)
}

// scanRunStatusCompleted mirrors scanqueue.StatusCompleted for scan_runs rows
// this package reads directly.
const scanRunStatusCompleted = "completed"

// decodeScanResult reads a completed run's result_payload. Running runs carry
// progress in the same column, so only completed runs report a result.
func decodeScanResult(status string, raw []byte) *ScanResult {
	if status != scanRunStatusCompleted {
		return nil
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil
	}
	var result ScanResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	return &result
}

const insertEventSQL = `
	INSERT INTO autoscan_events (
		source_id, plugin_id, capability_id, started_at, completed_at,
		duration_ms, status, delivery_mode, provider_event_type, error_message, marker_before
	)
	VALUES ($1, $2, $3, $4, $4, 0, $5, $6, $7, $8, $9)
	RETURNING id`

func (r *Repository) CreateEvent(ctx context.Context, in EventCreate) (int64, error) {
	started := in.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	sourceID := strings.TrimSpace(in.SourceID)
	insertArgs := func(sourceID any) []any {
		return []any{
			sourceID,
			in.PluginID,
			in.CapabilityID,
			started,
			string(EventStatusRunning),
			deliveryModeArg(in.DeliveryMode),
			in.ProviderEventType,
			"",
			nullable(in.MarkerBefore),
		}
	}
	// Sourceless events (ad hoc triggers) and events that opt out of the
	// running-event exclusion (webhook deliveries — no marker window to
	// re-read, so they must never be dropped) insert without the advisory
	// lock + running check.
	if sourceID == "" || in.SkipRunningCheck {
		var sourceArg any
		if sourceID != "" {
			sourceArg = sourceID
		}
		row := r.pool.QueryRow(ctx, insertEventSQL, insertArgs(sourceArg)...)
		var id int64
		if err := row.Scan(&id); err != nil {
			return 0, fmt.Errorf("create autoscan event: %w", err)
		}
		return id, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin autoscan event: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(900173, hashtext($1))`, sourceID); err != nil {
		return 0, fmt.Errorf("lock autoscan event: %w", err)
	}

	var runningID int64
	err = tx.QueryRow(ctx, `
		SELECT id
		FROM autoscan_events
		WHERE source_id = $1
		  AND status = $2
		ORDER BY started_at ASC, id ASC
		LIMIT 1`,
		sourceID,
		string(EventStatusRunning),
	).Scan(&runningID)
	if err == nil {
		return 0, fmt.Errorf("%w: source %s event %d", ErrPollAlreadyRunning, sourceID, runningID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("check running autoscan event: %w", err)
	}

	row := tx.QueryRow(ctx, insertEventSQL, insertArgs(sourceID)...)
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, fmt.Errorf("create autoscan event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit autoscan event: %w", err)
	}
	return id, nil
}

func (r *Repository) FinishEvent(ctx context.Context, in EventFinish) error {
	if in.ID == 0 {
		return nil
	}
	completed := in.CompletedAt
	if completed.IsZero() {
		completed = time.Now()
	}
	msg := truncateUTF8(in.ErrorMessage, maxLastErrorLen)
	changeLog, err := encodeChangeLog(in.Changes)
	if err != nil {
		return fmt.Errorf("encode autoscan change log: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE autoscan_events
		SET completed_at = $2,
			duration_ms = GREATEST(0, EXTRACT(EPOCH FROM ($2 - started_at)) * 1000)::bigint,
			status = $3,
			changes_returned = $4,
			changes_resolved = $5,
			targets_claimed = $6,
			scans_created = $7,
			scans_reused = $8,
			scans_suppressed = $9,
			error_message = $10,
			marker_after = $11,
			change_log = $12::jsonb,
			change_log_truncated = $13
		WHERE id = $1`,
		in.ID,
		completed,
		string(in.Status),
		in.ChangesReturned,
		in.ChangesResolved,
		in.TargetsClaimed,
		in.ScansCreated,
		in.ScansReused,
		in.ScansSuppressed,
		msg,
		nullable(in.MarkerAfter),
		string(changeLog),
		in.ChangesTruncated,
	)
	if err != nil {
		return fmt.Errorf("finish autoscan event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: autoscan event %d", ErrNotFound, in.ID)
	}
	return nil
}

func (r *Repository) MarkInterruptedEvents(ctx context.Context) error {
	msg := "poll started but did not finish"
	_, err := r.pool.Exec(ctx, `
		UPDATE autoscan_events
		SET completed_at = now(),
			duration_ms = GREATEST(0, EXTRACT(EPOCH FROM (now() - started_at)) * 1000)::bigint,
			status = $1,
			error_message = $2
		WHERE status = $3`,
		string(EventStatusError),
		msg,
		string(EventStatusRunning),
	)
	if err != nil {
		return fmt.Errorf("mark interrupted autoscan events: %w", err)
	}
	return nil
}

// clampAutoscanLimit bounds a requested page size to a sane window: the
// default page when unset, capped so a single query can never fan out.
func clampAutoscanLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

// eventFilterClauses builds the shared WHERE clauses (and positional args) for
// autoscan event queries, so listing and counting filter identically and the
// search SQL lives in exactly one place.
func eventFilterClauses(filter EventListFilter) ([]string, []any) {
	clauses := []string{"true"}
	args := []any{}
	if strings.TrimSpace(filter.SourceID) != "" {
		args = append(args, strings.TrimSpace(filter.SourceID))
		clauses = append(clauses, fmt.Sprintf("source_id = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, string(filter.Status))
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	}
	if search := strings.ToLower(strings.TrimSpace(filter.Search)); search != "" {
		args = append(args, "%"+search+"%")
		param := fmt.Sprintf("$%d", len(args))
		clauses = append(clauses, `(
			lower(capability_id) LIKE `+param+`
			OR lower(status) LIKE `+param+`
			OR lower(error_message) LIKE `+param+`
			OR lower(COALESCE(source_id::text, '')) LIKE `+param+`
			OR EXISTS (
				SELECT 1
				FROM jsonb_array_elements(change_log) AS cl(change)
				WHERE lower(COALESCE(cl.change->>'source_path', '')) LIKE `+param+`
				   OR lower(COALESCE(cl.change->>'rewritten_path', '')) LIKE `+param+`
			)
			OR EXISTS (
				SELECT 1
				FROM scan_runs sr
				WHERE sr.autoscan_event_id = autoscan_events.id
				  AND (
					lower(sr.id) LIKE `+param+`
					OR lower(sr.mode) LIKE `+param+`
					OR lower(sr.path) LIKE `+param+`
					OR lower(sr.status) LIKE `+param+`
					OR lower(COALESCE(sr.error_message, '')) LIKE `+param+`
				  )
			)
		)`)
	}
	return clauses, args
}

// scanFilterClauses builds the shared WHERE clauses for autoscan scan queries.
// The clauses reference the `sr` (scan_runs) and `e` (autoscan_events) aliases,
// so callers must select FROM scan_runs sr LEFT JOIN autoscan_events e.
func scanFilterClauses(filter ScanListFilter) ([]string, []any) {
	clauses := []string{"sr.trigger = 'autoscan'"}
	args := []any{}
	if strings.TrimSpace(filter.Status) != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		clauses = append(clauses, fmt.Sprintf("sr.status = $%d", len(args)))
	}
	if search := strings.ToLower(strings.TrimSpace(filter.Search)); search != "" {
		args = append(args, "%"+search+"%")
		param := fmt.Sprintf("$%d", len(args))
		clauses = append(clauses, `(
			lower(sr.id) LIKE `+param+`
			OR lower(sr.mode) LIKE `+param+`
			OR lower(sr.path) LIKE `+param+`
			OR lower(sr.status) LIKE `+param+`
			OR lower(COALESCE(sr.error_message, '')) LIKE `+param+`
			OR lower(COALESCE(e.capability_id, '')) LIKE `+param+`
			OR lower(COALESCE(e.status, '')) LIKE `+param+`
			OR lower(COALESCE(e.source_id::text, '')) LIKE `+param+`
		)`)
	}
	return clauses, args
}

// CountEvents returns the total number of autoscan events matching filter,
// ignoring limit/offset. It powers the "of N" total in paginated views.
func (r *Repository) CountEvents(ctx context.Context, filter EventListFilter) (int, error) {
	clauses, args := eventFilterClauses(filter)
	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM autoscan_events
		WHERE `+strings.Join(clauses, " AND "),
		args...,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("count autoscan events: %w", err)
	}
	return total, nil
}

// CountAutoscanScans returns the total number of autoscan scan runs matching
// filter, ignoring limit/offset. It powers the "of N" total in paginated views.
func (r *Repository) CountAutoscanScans(ctx context.Context, filter ScanListFilter) (int, error) {
	clauses, args := scanFilterClauses(filter)
	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM scan_runs sr
		LEFT JOIN autoscan_events e ON e.id = sr.autoscan_event_id
		WHERE `+strings.Join(clauses, " AND "),
		args...,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("count autoscan scans: %w", err)
	}
	return total, nil
}

func (r *Repository) ListEvents(ctx context.Context, filter EventListFilter) ([]EventWithRuns, error) {
	limit := clampAutoscanLimit(filter.Limit)
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	clauses, args := eventFilterClauses(filter)
	args = append(args, limit)
	limitParam := len(args)
	args = append(args, offset)
	offsetParam := len(args)

	rows, err := r.pool.Query(ctx, `
		SELECT `+eventColumns+`
		FROM autoscan_events
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY completed_at DESC, id DESC
		LIMIT $`+fmt.Sprint(limitParam)+` OFFSET $`+fmt.Sprint(offsetParam),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list autoscan events: %w", err)
	}
	defer rows.Close()

	events := make([]EventWithRuns, 0)
	ids := make([]int64, 0)
	indexByID := map[int64]int{}
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		indexByID[event.ID] = len(events)
		ids = append(ids, event.ID)
		events = append(events, EventWithRuns{Event: event, Runs: []ScanRunSummary{}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return events, nil
	}

	runRows, err := r.pool.Query(ctx, `
		SELECT autoscan_event_id, id, media_folder_id, mode, path, trigger, status,
			COALESCE(error_message, ''), requested_at, started_at, completed_at, result_payload
		FROM scan_runs
		WHERE autoscan_event_id = ANY($1)
		ORDER BY requested_at ASC`,
		ids,
	)
	if err != nil {
		return nil, fmt.Errorf("list autoscan event scan runs: %w", err)
	}
	defer runRows.Close()
	for runRows.Next() {
		var eventID int64
		var run ScanRunSummary
		var resultPayload []byte
		if err := runRows.Scan(
			&eventID,
			&run.ID,
			&run.MediaFolderID,
			&run.Mode,
			&run.Path,
			&run.Trigger,
			&run.Status,
			&run.ErrorMessage,
			&run.RequestedAt,
			&run.StartedAt,
			&run.CompletedAt,
			&resultPayload,
		); err != nil {
			return nil, err
		}
		run.Result = decodeScanResult(run.Status, resultPayload)
		if idx, ok := indexByID[eventID]; ok {
			events[idx].Runs = append(events[idx].Runs, run)
		}
	}
	return events, runRows.Err()
}

func (r *Repository) ListRunningEvents(ctx context.Context) ([]Event, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+eventColumns+`
		FROM autoscan_events
		WHERE status = $1
		ORDER BY started_at ASC, id ASC`,
		string(EventStatusRunning),
	)
	if err != nil {
		return nil, fmt.Errorf("list running autoscan events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0)
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *Repository) ListAutoscanScans(ctx context.Context, filter ScanListFilter) ([]ScanWithEvent, error) {
	limit := clampAutoscanLimit(filter.Limit)
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	clauses, args := scanFilterClauses(filter)
	args = append(args, limit)
	limitParam := len(args)
	args = append(args, offset)
	offsetParam := len(args)

	rows, err := r.pool.Query(ctx, `
		SELECT
			sr.id,
			sr.media_folder_id,
			sr.mode,
			sr.path,
			sr.trigger,
			sr.status,
			COALESCE(sr.error_message, ''),
				sr.requested_at,
				sr.started_at,
				sr.completed_at,
				sr.autoscan_event_id,
				e.source_id,
				COALESCE(e.plugin_id, ''),
				COALESCE(e.capability_id, ''),
				COALESCE(e.status, ''),
				e.completed_at,
				sr.result_payload
		FROM scan_runs sr
		LEFT JOIN autoscan_events e ON e.id = sr.autoscan_event_id
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY COALESCE(sr.completed_at, sr.started_at, sr.requested_at) DESC, sr.id DESC
		LIMIT $`+fmt.Sprint(limitParam)+` OFFSET $`+fmt.Sprint(offsetParam),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list autoscan scans: %w", err)
	}
	defer rows.Close()

	scans := make([]ScanWithEvent, 0)
	for rows.Next() {
		var scan ScanWithEvent
		var eventStatus string
		var resultPayload []byte
		if err := rows.Scan(
			&scan.ID,
			&scan.MediaFolderID,
			&scan.Mode,
			&scan.Path,
			&scan.Trigger,
			&scan.Status,
			&scan.ErrorMessage,
			&scan.RequestedAt,
			&scan.StartedAt,
			&scan.CompletedAt,
			&scan.AutoscanEventID,
			&scan.SourceID,
			&scan.PluginID,
			&scan.CapabilityID,
			&eventStatus,
			&scan.EventCompletedAt,
			&resultPayload,
		); err != nil {
			return nil, err
		}
		scan.EventStatus = EventStatus(eventStatus)
		scan.Result = decodeScanResult(scan.Status, resultPayload)
		scans = append(scans, scan)
	}
	return scans, rows.Err()
}

func (r *Repository) GetQueueSummary(ctx context.Context) (QueueSummary, error) {
	var summary QueueSummary
	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = ANY($1))::int,
			COUNT(*) FILTER (WHERE status = $2)::int,
			COUNT(*) FILTER (WHERE status = $3)::int
		FROM scan_runs
		WHERE trigger = $4`,
		[]string{"accepted", "running"},
		"accepted",
		"running",
		"autoscan",
	).Scan(&summary.Active, &summary.Accepted, &summary.Running)
	if err != nil {
		return QueueSummary{}, fmt.Errorf("get autoscan queue summary: %w", err)
	}
	return summary, nil
}

func (r *Repository) LatestEventAt(ctx context.Context) (*time.Time, error) {
	var latest *time.Time
	err := r.pool.QueryRow(ctx, `SELECT max(completed_at) FROM autoscan_events WHERE status <> $1`, string(EventStatusRunning)).Scan(&latest)
	if err != nil {
		return nil, fmt.Errorf("get latest autoscan event time: %w", err)
	}
	return latest, nil
}

// missingID maps a caller-supplied row id that cannot address a row to
// ErrNotFound without running a query. Every autoscan id column
// (autoscan_sources.id, autoscan_connections.id,
// autoscan_webhook_endpoints.source_id) is a `uuid`, so a malformed id can only
// ever miss; handing it to Postgres raises SQLSTATE 22P02 and surfaces to the
// client as a 500 instead of a 404. kind names the row for the error message,
// e.g. "source" or "connection".
func missingID(kind, id string) error {
	// Validate the exact value the query will bind; a padded UUID is still
	// rejected by the uuid column, so trimming here would recreate the 500.
	if uuid.Validate(id) == nil {
		return nil
	}
	return fmt.Errorf("%w: %s %s", ErrNotFound, kind, id)
}

// missingConnectionID maps a bound connection id that cannot address a row to
// ErrNotFound, matching how the FK violation (23503) for a non-existent
// connection is reported. A nil or blank id means "unbound" and is allowed.
func missingConnectionID(connectionID *string) error {
	if connectionID == nil || strings.TrimSpace(*connectionID) == "" {
		return nil
	}
	return missingID("connection", *connectionID)
}
