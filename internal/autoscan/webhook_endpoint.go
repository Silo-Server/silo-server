package autoscan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// webhookSecretAAD binds an autoscan_webhook_endpoints secret_ref ciphertext to
// its owning source id.
func webhookSecretAAD(sourceID string) string {
	return secret.RowAAD("autoscan_webhook_endpoints", "secret_ref", sourceID)
}

// webhookSecretSuffixLen is how many trailing token characters are stored for
// UI/debug display.
const webhookSecretSuffixLen = 6

// newWebhookToken generates a URL-safe bearer token plus its SHA-256 lookup
// hash and display suffix. The token is 32 random bytes; plain SHA-256 is
// sufficient for lookup because the preimage is high-entropy random data.
func newWebhookToken() (token, hash, suffix string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("generate webhook token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	hash = hashWebhookToken(token)
	return token, hash, token[len(token)-webhookSecretSuffixLen:], nil
}

// hashWebhookToken maps a raw bearer token to its secret_hash lookup value.
func hashWebhookToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const webhookEndpointColumns = `source_id, secret_suffix, created_at, rotated_at,
	last_received_at, last_error_at, last_error_message`

func scanWebhookEndpoint(row interface{ Scan(...any) error }) (WebhookEndpoint, error) {
	var e WebhookEndpoint
	if err := row.Scan(&e.SourceID, &e.SecretSuffix, &e.CreatedAt, &e.RotatedAt,
		&e.LastReceivedAt, &e.LastErrorAt, &e.LastErrorMessage); err != nil {
		return WebhookEndpoint{}, err
	}
	return e, nil
}

// CreateWebhookEndpoint creates a source's webhook endpoint and returns it with
// the plaintext bearer token. When the endpoint already exists it is returned
// unchanged with an empty token (the token is only handed out on create/rotate;
// redisplay goes through RevealWebhookToken). An unknown source maps to
// ErrNotFound.
func (r *Repository) CreateWebhookEndpoint(ctx context.Context, sourceID string) (WebhookEndpoint, string, error) {
	token, hash, suffix, err := newWebhookToken()
	if err != nil {
		return WebhookEndpoint{}, "", err
	}
	ref, err := r.cipher.Encrypt(token, webhookSecretAAD(sourceID))
	if err != nil {
		return WebhookEndpoint{}, "", fmt.Errorf("encrypt webhook token: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_webhook_endpoints (source_id, secret_hash, secret_ref, secret_suffix)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (source_id) DO NOTHING
		RETURNING `+webhookEndpointColumns,
		sourceID, hash, ref, suffix)
	endpoint, err := scanWebhookEndpoint(row)
	if err == nil {
		return endpoint, token, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Conflict: the endpoint already exists — return it, discard the
		// unused freshly generated token.
		existing, gerr := r.GetWebhookEndpoint(ctx, sourceID)
		if gerr != nil {
			return WebhookEndpoint{}, "", gerr
		}
		return existing, "", nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return WebhookEndpoint{}, "", fmt.Errorf("%w: source %s", ErrNotFound, sourceID)
	}
	return WebhookEndpoint{}, "", fmt.Errorf("create autoscan webhook endpoint: %w", err)
}

// RotateWebhookEndpoint replaces the endpoint's bearer token, invalidating the
// old URL immediately, and returns the new plaintext token. An unknown
// source/endpoint maps to ErrNotFound.
func (r *Repository) RotateWebhookEndpoint(ctx context.Context, sourceID string) (WebhookEndpoint, string, error) {
	token, hash, suffix, err := newWebhookToken()
	if err != nil {
		return WebhookEndpoint{}, "", err
	}
	ref, err := r.cipher.Encrypt(token, webhookSecretAAD(sourceID))
	if err != nil {
		return WebhookEndpoint{}, "", fmt.Errorf("encrypt webhook token: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE autoscan_webhook_endpoints
		SET secret_hash = $2, secret_ref = $3, secret_suffix = $4, rotated_at = now()
		WHERE source_id = $1
		RETURNING `+webhookEndpointColumns,
		sourceID, hash, ref, suffix)
	endpoint, err := scanWebhookEndpoint(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WebhookEndpoint{}, "", fmt.Errorf("%w: webhook endpoint for source %s", ErrNotFound, sourceID)
		}
		return WebhookEndpoint{}, "", fmt.Errorf("rotate autoscan webhook endpoint: %w", err)
	}
	return endpoint, token, nil
}

// DeleteWebhookEndpoint removes a source's webhook endpoint; future deliveries
// to the old URL return not found. An unknown endpoint maps to ErrNotFound.
func (r *Repository) DeleteWebhookEndpoint(ctx context.Context, sourceID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM autoscan_webhook_endpoints WHERE source_id = $1`, sourceID)
	if err != nil {
		return fmt.Errorf("delete autoscan webhook endpoint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: webhook endpoint for source %s", ErrNotFound, sourceID)
	}
	return nil
}

// GetWebhookEndpoint loads a source's webhook endpoint state. An unknown
// endpoint maps to ErrNotFound.
func (r *Repository) GetWebhookEndpoint(ctx context.Context, sourceID string) (WebhookEndpoint, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+webhookEndpointColumns+`
		FROM autoscan_webhook_endpoints WHERE source_id = $1`, sourceID)
	endpoint, err := scanWebhookEndpoint(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WebhookEndpoint{}, fmt.Errorf("%w: webhook endpoint for source %s", ErrNotFound, sourceID)
		}
		return WebhookEndpoint{}, fmt.Errorf("get autoscan webhook endpoint: %w", err)
	}
	return endpoint, nil
}

// ListWebhookEndpoints returns every webhook endpoint (one batched query for
// admin source listings).
func (r *Repository) ListWebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+webhookEndpointColumns+`
		FROM autoscan_webhook_endpoints`)
	if err != nil {
		return nil, fmt.Errorf("list autoscan webhook endpoints: %w", err)
	}
	defer rows.Close()
	var out []WebhookEndpoint
	for rows.Next() {
		endpoint, err := scanWebhookEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, endpoint)
	}
	return out, rows.Err()
}

// RevealWebhookToken decrypts the stored bearer token for admin redisplay.
func (r *Repository) RevealWebhookToken(ctx context.Context, sourceID string) (string, error) {
	var ref string
	err := r.pool.QueryRow(ctx, `SELECT secret_ref
		FROM autoscan_webhook_endpoints WHERE source_id = $1`, sourceID).Scan(&ref)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("%w: webhook endpoint for source %s", ErrNotFound, sourceID)
		}
		return "", fmt.Errorf("reveal autoscan webhook token: %w", err)
	}
	token, err := r.cipher.Decrypt(ref, webhookSecretAAD(sourceID))
	if err != nil {
		return "", fmt.Errorf("decrypt webhook token: %w", err)
	}
	return token, nil
}

// ResolveWebhookToken maps a raw delivery token to its owning source and
// endpoint. Lookup is constant-shape: the token is hashed and matched against
// secret_hash; unknown, deleted, and never-existed tokens are indistinguishable
// and all map to ErrNotFound.
func (r *Repository) ResolveWebhookToken(ctx context.Context, token string) (Source, WebhookEndpoint, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Source{}, WebhookEndpoint{}, fmt.Errorf("%w: webhook token", ErrNotFound)
	}
	row := r.pool.QueryRow(ctx, `
		SELECT s.id, s.plugin_id, s.capability_id, s.connection_id, s.enabled, s.delivery_mode,
			s.poll_interval_seconds, s.path_rewrites, s.source_config, s.label, s.marker, s.last_run_at, s.last_error,
			e.source_id, e.secret_suffix, e.created_at, e.rotated_at,
			e.last_received_at, e.last_error_at, e.last_error_message
		FROM autoscan_webhook_endpoints e
		JOIN autoscan_sources s ON s.id = e.source_id
		WHERE e.secret_hash = $1`,
		hashWebhookToken(token))
	var (
		src          Source
		endpoint     WebhookEndpoint
		pathRewrites []byte
		sourceConfig []byte
	)
	if err := row.Scan(&src.ID, &src.PluginID, &src.CapabilityID, &src.ConnectionID,
		&src.Enabled, &src.DeliveryMode, &src.PollIntervalSeconds, &pathRewrites, &sourceConfig,
		&src.Label, &src.Marker, &src.LastRunAt, &src.LastError,
		&endpoint.SourceID, &endpoint.SecretSuffix, &endpoint.CreatedAt, &endpoint.RotatedAt,
		&endpoint.LastReceivedAt, &endpoint.LastErrorAt, &endpoint.LastErrorMessage); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Source{}, WebhookEndpoint{}, fmt.Errorf("%w: webhook token", ErrNotFound)
		}
		return Source{}, WebhookEndpoint{}, fmt.Errorf("resolve autoscan webhook token: %w", err)
	}
	rewrites, err := unmarshalPathRewrites(pathRewrites)
	if err != nil {
		return Source{}, WebhookEndpoint{}, err
	}
	src.PathRewrites = rewrites
	config, err := unmarshalSourceConfig(sourceConfig)
	if err != nil {
		return Source{}, WebhookEndpoint{}, err
	}
	src.SourceConfig = config
	return src, endpoint, nil
}

// TouchWebhookReceived stamps the endpoint's last valid delivery time.
func (r *Repository) TouchWebhookReceived(ctx context.Context, sourceID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE autoscan_webhook_endpoints
		SET last_received_at = now()
		WHERE source_id = $1`, sourceID)
	if err != nil {
		return fmt.Errorf("touch autoscan webhook received: %w", err)
	}
	return nil
}

// RecordWebhookError stores the endpoint's most recent parse/ingest failure
// (length-bounded; the caller sanitizes payload-derived content).
func (r *Repository) RecordWebhookError(ctx context.Context, sourceID, msg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE autoscan_webhook_endpoints
		SET last_error_at = now(), last_error_message = $2
		WHERE source_id = $1`, sourceID, truncateUTF8(msg, maxLastErrorLen))
	if err != nil {
		return fmt.Errorf("record autoscan webhook error: %w", err)
	}
	return nil
}

// ClearWebhookError removes stale delivery failure state after a queued retry
// succeeds. last_received_at remains the time the provider actually delivered
// the webhook rather than being rewritten as a processing timestamp.
func (r *Repository) ClearWebhookError(ctx context.Context, sourceID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE autoscan_webhook_endpoints
		SET last_error_at = NULL, last_error_message = ''
		WHERE source_id = $1`, sourceID)
	if err != nil {
		return fmt.Errorf("clear autoscan webhook error: %w", err)
	}
	return nil
}
