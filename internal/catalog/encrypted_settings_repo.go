package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// SettingsStore is the read/write surface over server_settings shared by the
// raw *ServerSettingsRepo and the *EncryptedSettingsRepo decorator. Consumers
// depend on this interface (rather than the concrete repo) so they
// transparently gain at-rest encryption for sensitive keys once the decorator
// is wired in.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	GetAll(ctx context.Context) (map[string]string, error)
}

// SensitiveSettingKeys is the single source of truth for which server_settings
// keys hold secrets. It drives BOTH at-rest encryption (the decorator encrypts
// these on write, decrypts them on read) AND admin-API redaction, so the two
// can never drift apart.
//
// It was audited from the config loader's real inputs
// (internal/config/db_loader.go), the admin redaction map, and the other known
// server_settings consumers — the Audiobookshelf-compat JWT secret
// (internal/audiobooks/config.go), the watch-sync OAuth client config
// (internal/watchsync/service.go), and the legacy single-instance arr keys —
// including the legacy aliases still read as fallbacks (s3.operational_*,
// recommendations.openai_api_key) and redis.sentinel_password, which the old
// redaction map omitted.
//
// Adding a key here is only safe when every reader of that key goes through a
// cipher-aware path (the decorator, or a repo that calls
// secret.Cipher.DecryptIfEncrypted with secret.SettingsAAD). A raw SQL reader
// would otherwise see ciphertext after the backfill.
var SensitiveSettingKeys = map[string]bool{
	// Core auth signing secret — no longer the encryption root (SECRET_KEY is),
	// and now itself encrypted at rest under SECRET_KEY.
	"auth.jwt_secret": true,
	// Audiobookshelf-compat HMAC signing key (generated + persisted as hex).
	"audiobooks.abs.jwt_secret": true,

	// S3 — public assets, private internal, user DB, plus the legacy
	// "operational" aliases db_loader still reads as fallbacks (L155-192;
	// migration 086 copied but did not delete them).
	"s3.public_access_key":        true,
	"s3.public_secret_key":        true,
	"s3.public_token_secret":      true,
	"s3.private_access_key":       true,
	"s3.private_secret_key":       true,
	"s3.user_db_access_key":       true,
	"s3.user_db_secret_key":       true,
	"s3.operational_access_key":   true,
	"s3.operational_secret_key":   true,
	"s3.operational_token_secret": true,

	// Redis — url may embed credentials (redis://:pass@host); sentinel_password
	// is read at db_loader L334 and was MISSING from the old redaction map.
	"redis.url":               true,
	"redis.sentinel_password": true,

	// Metadata / list-provider API keys.
	"tmdb.api_key":    true,
	"mdblist.api_key": true,
	"introdb.api_key": true,

	// Shared AI endpoint API keys (+ legacy subtitle_ai alias the loader still
	// falls back to; the legacy row is never renamed because ciphertext is
	// GCM-bound to its key).
	"ai.api_key":          true,
	"ai.asr_api_key":      true,
	"subtitle_ai.api_key": true,

	// Recommendations embedding auth (+ legacy openai alias at db_loader L409).
	"recommendations.embedding_auth_token": true,
	"recommendations.openai_api_key":       true,

	// Optional catalog search provider auth. Do not rename this key after
	// shipping; encrypted values are bound to the setting name.
	"catalog.search.meilisearch.api_key": true,

	// Watch-sync OAuth client credentials. The client_id entries are low-value
	// but kept as a harmless safe superset and to match existing redaction.
	"watchsync.trakt.client_id":     true,
	"watchsync.trakt.client_secret": true,
	"watchsync.simkl.client_id":     true,
	"watchsync.simkl.client_secret": true,

	// Legacy single-instance arr keys (superseded by per-integration rows, but
	// still referenced by older request_integrations rows until backfilled).
	"requests.radarr.api_key": true,
	"requests.sonarr.api_key": true,

	// Shared outbound email (internal/mail) SMTP credential.
	"email.smtp_password": true,

	// Discord notification integration. The client_id is public in Discord's
	// own UI, so only the secret and bot token are encrypted.
	"discord.client_secret": true,
	"discord.bot_token":     true,

	// Web Push VAPID keypair JSON (generated + persisted atomically as one
	// value by the notifications system; clients receive the public half via
	// the capability endpoint, never from the settings store).
	"notifications.web_push.vapid_keypair": true,

	// Silo push relay bearer credential for APNs/FCM delivery.
	"notifications.push_relay_api_key": true,
}

// EncryptedSettingsRepo decorates a raw settings store, transparently
// encrypting the SensitiveSettingKeys on write and decrypting them on read.
// config.LoadFromDB and every other consumer keep seeing plaintext while the
// values rest as ciphertext in the database, GCM-bound to their key.
type EncryptedSettingsRepo struct {
	// inner is the RAW store (no encryption). It must never be another
	// EncryptedSettingsRepo, or writes would double-encrypt.
	inner  SettingsStore
	cipher *secret.Cipher
}

// NewEncryptedSettingsRepo wraps a raw settings store with the cipher. Pass the
// concrete *ServerSettingsRepo (or an in-memory fake in tests) as inner.
func NewEncryptedSettingsRepo(inner SettingsStore, cipher *secret.Cipher) *EncryptedSettingsRepo {
	return &EncryptedSettingsRepo{inner: inner, cipher: cipher}
}

var _ SettingsStore = (*EncryptedSettingsRepo)(nil)

// Set encrypts a sensitive, non-empty value (AAD-bound to its key) before
// delegating to the raw store; non-sensitive keys and empty values delegate
// unchanged. Encrypting an empty value is a no-op, so an absent secret stays an
// empty column rather than an envelope.
func (r *EncryptedSettingsRepo) Set(ctx context.Context, key, value string) error {
	if SensitiveSettingKeys[key] && value != "" {
		ct, err := r.cipher.Encrypt(value, secret.SettingsAAD(key))
		if err != nil {
			return fmt.Errorf("encrypt setting %q: %w", key, err)
		}
		value = ct
	}
	return r.inner.Set(ctx, key, value)
}

// settingsConditionalWriter is the optional conditional-write capability of a
// raw settings store (satisfied by *ServerSettingsRepo).
type settingsConditionalWriter interface {
	SetIfAbsent(ctx context.Context, key, value string) (bool, error)
}

type settingsBatchWriter interface {
	SetMany(ctx context.Context, values map[string]string) error
}

type settingsAtomicUpdater interface {
	UpdateAtomic(
		ctx context.Context,
		update func(current map[string]string) (map[string]string, error),
	) error
}

// SetMany encrypts every sensitive member before delegating one atomic batch
// to the raw settings repository.
func (r *EncryptedSettingsRepo) SetMany(ctx context.Context, values map[string]string) error {
	inner, ok := r.inner.(settingsBatchWriter)
	if !ok {
		return fmt.Errorf("settings store does not support atomic batch writes")
	}
	encrypted := make(map[string]string, len(values))
	for key, value := range values {
		if SensitiveSettingKeys[key] && value != "" {
			ct, err := r.cipher.Encrypt(value, secret.SettingsAAD(key))
			if err != nil {
				return fmt.Errorf("encrypt setting %q: %w", key, err)
			}
			value = ct
		}
		encrypted[key] = value
	}
	return inner.SetMany(ctx, encrypted)
}

// UpdateAtomic preserves the raw repository's cross-process serialization
// while presenting plaintext to the validator and encrypting only the returned
// writes before they reach server_settings.
func (r *EncryptedSettingsRepo) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	inner, ok := r.inner.(settingsAtomicUpdater)
	if !ok {
		return fmt.Errorf("settings store does not support atomic updates")
	}
	return inner.UpdateAtomic(ctx, func(rawCurrent map[string]string) (map[string]string, error) {
		current := make(map[string]string, len(rawCurrent))
		for key, value := range rawCurrent {
			plain, err := r.cipher.DecryptIfEncrypted(value, secret.SettingsAAD(key))
			if err != nil {
				return nil, fmt.Errorf("decrypt setting %q: %w", key, err)
			}
			current[key] = plain
		}
		writes, err := update(current)
		if err != nil {
			return nil, err
		}
		encrypted := make(map[string]string, len(writes))
		for key, value := range writes {
			if SensitiveSettingKeys[key] && value != "" {
				ciphertext, err := r.cipher.Encrypt(value, secret.SettingsAAD(key))
				if err != nil {
					return nil, fmt.Errorf("encrypt setting %q: %w", key, err)
				}
				value = ciphertext
			}
			encrypted[key] = value
		}
		return encrypted, nil
	})
}

// SetIfAbsent applies Set's encryption contract to a conditional write: the
// value lands only when the key currently has no value, so concurrent
// provisioners of generated secrets cannot overwrite each other.
func (r *EncryptedSettingsRepo) SetIfAbsent(ctx context.Context, key, value string) (bool, error) {
	inner, ok := r.inner.(settingsConditionalWriter)
	if !ok {
		return false, fmt.Errorf("settings store does not support conditional writes")
	}
	if SensitiveSettingKeys[key] && value != "" {
		ct, err := r.cipher.Encrypt(value, secret.SettingsAAD(key))
		if err != nil {
			return false, fmt.Errorf("encrypt setting %q: %w", key, err)
		}
		value = ct
	}
	return inner.SetIfAbsent(ctx, key, value)
}

// Get reads a value and applies the read-path contract: legacy plaintext passes
// through, an enc:v1: value is decrypted, and a corrupt ciphertext errors.
func (r *EncryptedSettingsRepo) Get(ctx context.Context, key string) (string, error) {
	value, err := r.inner.Get(ctx, key)
	if err != nil {
		return "", err
	}
	out, err := r.cipher.DecryptIfEncrypted(value, secret.SettingsAAD(key))
	if err != nil {
		return "", fmt.Errorf("decrypt setting %q: %w", key, err)
	}
	return out, nil
}

// GetAll reads every setting and decrypts any enc:v1: value in place, so
// callers (notably config.LoadFromDB) receive a fully plaintext map.
func (r *EncryptedSettingsRepo) GetAll(ctx context.Context) (map[string]string, error) {
	all, err := r.inner.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	for key, value := range all {
		out, derr := r.cipher.DecryptIfEncrypted(value, secret.SettingsAAD(key))
		if derr != nil {
			return nil, fmt.Errorf("decrypt setting %q: %w", key, derr)
		}
		all[key] = out
	}
	return all, nil
}

// BackfillSensitiveSettings encrypts any plaintext value currently stored under
// a sensitive key. It is idempotent (already-encrypted and empty values are
// skipped) and best-effort: per-key failures are collected and returned but do
// not abort the sweep, so one unreadable row cannot block startup. Returns the
// number of keys newly encrypted.
func (r *EncryptedSettingsRepo) BackfillSensitiveSettings(ctx context.Context) (int, error) {
	keys := make([]string, 0, len(SensitiveSettingKeys))
	for key := range SensitiveSettingKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys) // deterministic order for logs and tests

	encrypted := 0
	var errs []error
	for _, key := range keys {
		// Read the RAW inner value (not through this decorator) so we can tell
		// plaintext from an already-encrypted envelope.
		raw, err := r.inner.Get(ctx, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %q: %w", key, err))
			continue
		}
		if raw == "" || secret.IsEncrypted(raw) {
			continue
		}
		ct, err := r.cipher.Encrypt(raw, secret.SettingsAAD(key))
		if err != nil {
			errs = append(errs, fmt.Errorf("encrypt %q: %w", key, err))
			continue
		}
		if err := r.inner.Set(ctx, key, ct); err != nil {
			errs = append(errs, fmt.Errorf("write %q: %w", key, err))
			continue
		}
		encrypted++
	}
	return encrypted, errors.Join(errs...)
}
