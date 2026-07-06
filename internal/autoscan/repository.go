package autoscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	row := r.pool.QueryRow(ctx, `
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
	return out, nil
}

func (r *Repository) DeleteConnection(ctx context.Context, id string) error {
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

const sourceColumns = `id, plugin_id, capability_id, connection_id, enabled,
	poll_interval_seconds, path_rewrites, source_config, label, marker, last_run_at, last_error`

func scanSource(row interface{ Scan(...any) error }) (Source, error) {
	var s Source
	var pathRewrites []byte
	var sourceConfig []byte
	if err := row.Scan(&s.ID, &s.PluginID, &s.CapabilityID, &s.ConnectionID,
		&s.Enabled, &s.PollIntervalSeconds, &pathRewrites, &sourceConfig, &s.Label, &s.Marker, &s.LastRunAt, &s.LastError); err != nil {
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

// CreateSource inserts a new autoscan source row. One installed scan_source
// capability can back many sources (e.g. one Sonarr plugin install fronting 4
// arr servers, one source per connection), so this is a plain INSERT with a
// fresh uuid rather than an upsert. A non-existent connection trips the FK
// constraint and maps to ErrNotFound.
func (r *Repository) CreateSource(ctx context.Context, s Source) (Source, error) {
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
			plugin_id, capability_id, connection_id, enabled, poll_interval_seconds, path_rewrites, source_config, label
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+sourceColumns,
		s.PluginID, s.CapabilityID, connectionIDArg(s.ConnectionID), s.Enabled, s.PollIntervalSeconds, rewrites, sourceConfig, s.Label)
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
// (plugin_id, capability_id) and bookkeeping fields (marker/last_run_at/
// last_error) are left untouched. An unknown id maps to ErrNotFound; a
// non-existent connection trips the FK constraint and also maps to ErrNotFound.
func (r *Repository) UpdateSource(ctx context.Context, s Source) (Source, error) {
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
		    poll_interval_seconds = $4,
		    path_rewrites = $5,
		    source_config = $6,
		    label = $7,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+sourceColumns,
		s.ID, connectionIDArg(s.ConnectionID), s.Enabled, s.PollIntervalSeconds, rewrites, sourceConfig, s.Label)
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
	tag, err := r.pool.Exec(ctx, `DELETE FROM autoscan_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete autoscan source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: source %s", ErrNotFound, id)
	}
	return nil
}

// AdvanceMarker stores the opaque next marker for a source, stamps last_run_at,
// and clears any prior error. Called once a poll window's work is consumed —
// after a successful enqueue, or when the window's paths all resolved outside
// Silo's libraries and were advanced past.
func (r *Repository) AdvanceMarker(ctx context.Context, sourceID, marker string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE autoscan_sources
		SET marker = $2, last_run_at = now(), last_error = NULL, updated_at = now()
		WHERE id = $1`, sourceID, nullable(marker))
	if err != nil {
		return fmt.Errorf("advance autoscan marker: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: source %s", ErrNotFound, sourceID)
	}
	return nil
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
	duration_ms, status, changes_returned, changes_resolved, targets_claimed, scans_created,
	scans_reused, scans_suppressed, error_message, marker_before, marker_after`

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var e Event
	var status string
	if err := row.Scan(
		&e.ID,
		&e.SourceID,
		&e.PluginID,
		&e.CapabilityID,
		&e.StartedAt,
		&e.CompletedAt,
		&e.DurationMS,
		&status,
		&e.ChangesReturned,
		&e.ChangesResolved,
		&e.TargetsClaimed,
		&e.ScansCreated,
		&e.ScansReused,
		&e.ScansSuppressed,
		&e.ErrorMessage,
		&e.MarkerBefore,
		&e.MarkerAfter,
	); err != nil {
		return Event{}, err
	}
	e.Status = EventStatus(status)
	return e, nil
}

func (r *Repository) CreateEvent(ctx context.Context, in EventCreate) (int64, error) {
	started := in.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	sourceID := strings.TrimSpace(in.SourceID)
	if sourceID == "" {
		row := r.pool.QueryRow(ctx, `
			INSERT INTO autoscan_events (
				source_id, plugin_id, capability_id, started_at, completed_at,
				duration_ms, status, error_message, marker_before
			)
		VALUES ($1, $2, $3, $4, $4, 0, $5, $6, $7)
		RETURNING id`,
			nil,
			in.PluginID,
			in.CapabilityID,
			started,
			string(EventStatusRunning),
			"",
			nullable(in.MarkerBefore),
		)
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

	row := tx.QueryRow(ctx, `
		INSERT INTO autoscan_events (
			source_id, plugin_id, capability_id, started_at, completed_at,
			duration_ms, status, error_message, marker_before
		)
		VALUES ($1, $2, $3, $4, $4, 0, $5, $6, $7)
		RETURNING id`,
		sourceID,
		in.PluginID,
		in.CapabilityID,
		started,
		string(EventStatusRunning),
		"",
		nullable(in.MarkerBefore),
	)
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
			marker_after = $11
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
			COALESCE(error_message, ''), requested_at, started_at, completed_at
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
		); err != nil {
			return nil, err
		}
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
				e.completed_at
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
		); err != nil {
			return nil, err
		}
		scan.EventStatus = EventStatus(eventStatus)
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
