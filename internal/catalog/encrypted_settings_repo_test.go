package catalog

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// memSettings is an in-memory raw SettingsStore for DB-free decorator tests.
type memSettings struct{ m map[string]string }

func newMemSettings() *memSettings { return &memSettings{m: map[string]string{}} }

func (s *memSettings) Get(_ context.Context, key string) (string, error) { return s.m[key], nil }
func (s *memSettings) Set(_ context.Context, key, value string) error {
	s.m[key] = value
	return nil
}
func (s *memSettings) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out, nil
}
func (s *memSettings) UpdateAtomic(
	_ context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	current, _ := s.GetAll(context.Background())
	writes, err := update(current)
	if err != nil {
		return err
	}
	for key, value := range writes {
		s.m[key] = value
	}
	return nil
}

func newCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	key := make([]byte, 48)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	c, err := secret.New(key)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	return c
}

func pickSensitiveKey() string {
	for k := range SensitiveSettingKeys {
		return k
	}
	return "auth.jwt_secret"
}

func TestEncryptedSettings_SensitiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	dec := NewEncryptedSettingsRepo(raw, newCipher(t))

	const key = "tmdb.api_key"
	const plain = "tmdb-secret-key-value"
	if err := dec.Set(ctx, key, plain); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Raw store must hold ciphertext.
	if got := raw.m[key]; !secret.IsEncrypted(got) {
		t.Fatalf("raw store value = %q, want enc:v1: ciphertext", got)
	}
	// Decorator Get must return plaintext.
	got, err := dec.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != plain {
		t.Fatalf("Get = %q, want %q", got, plain)
	}
}

func TestEncryptedSettings_NonSensitivePassthrough(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	dec := NewEncryptedSettingsRepo(raw, newCipher(t))

	const key = "server.log_level"
	if SensitiveSettingKeys[key] {
		t.Fatalf("precondition: %q must be non-sensitive", key)
	}
	if err := dec.Set(ctx, key, "debug"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := raw.m[key]; got != "debug" {
		t.Fatalf("non-sensitive raw value = %q, want plaintext 'debug'", got)
	}
}

func TestEncryptedSettings_LegacyPlaintextPassthrough(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	dec := NewEncryptedSettingsRepo(raw, newCipher(t))

	// Simulate a pre-encryption row: a sensitive key holding raw plaintext.
	raw.m["tmdb.api_key"] = "legacy-plaintext"
	got, err := dec.Get(ctx, "tmdb.api_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "legacy-plaintext" {
		t.Fatalf("legacy plaintext Get = %q, want pass-through", got)
	}
}

func TestEncryptedSettings_EmptySensitiveStaysEmpty(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	dec := NewEncryptedSettingsRepo(raw, newCipher(t))
	if err := dec.Set(ctx, "tmdb.api_key", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := raw.m["tmdb.api_key"]; got != "" {
		t.Fatalf("empty sensitive value stored as %q, want empty (no envelope)", got)
	}
}

func TestEncryptedSettings_GetAllDecrypts(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	dec := NewEncryptedSettingsRepo(raw, newCipher(t))

	if err := dec.Set(ctx, "tmdb.api_key", "k1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := dec.Set(ctx, "server.mode", "integrated"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	all, err := dec.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if all["tmdb.api_key"] != "k1" {
		t.Fatalf("GetAll sensitive = %q, want decrypted 'k1'", all["tmdb.api_key"])
	}
	if all["server.mode"] != "integrated" {
		t.Fatalf("GetAll non-sensitive = %q", all["server.mode"])
	}
}

func TestEncryptedSettings_UpdateAtomicUsesPlaintextContract(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	dec := NewEncryptedSettingsRepo(raw, newCipher(t))
	if err := dec.Set(ctx, "tmdb.api_key", "old-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	err := dec.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		if current["tmdb.api_key"] != "old-secret" {
			t.Fatalf("callback value = %q, want plaintext", current["tmdb.api_key"])
		}
		return map[string]string{
			"tmdb.api_key":     "new-secret",
			"server.log_level": "debug",
		}, nil
	})
	if err != nil {
		t.Fatalf("UpdateAtomic: %v", err)
	}
	if !secret.IsEncrypted(raw.m["tmdb.api_key"]) {
		t.Fatalf("raw sensitive value = %q, want ciphertext", raw.m["tmdb.api_key"])
	}
	if raw.m["server.log_level"] != "debug" {
		t.Fatalf("raw non-sensitive value = %q, want debug", raw.m["server.log_level"])
	}
	got, err := dec.Get(ctx, "tmdb.api_key")
	if err != nil || got != "new-secret" {
		t.Fatalf("Get = %q, %v; want new-secret", got, err)
	}
}

// TestSensitiveSettingKeys_Audited locks the audited allowlist: a dropped key is
// a plaintext leak (and breaks redaction), so the critical secrets must stay
// present, while values that are NOT secrets must stay absent (encrypting them
// would corrupt config reads).
func TestSensitiveSettingKeys_Audited(t *testing.T) {
	mustHave := []string{
		"auth.jwt_secret",
		"audiobooks.abs.jwt_secret",
		"s3.public_secret_key",
		"s3.operational_secret_key", // legacy alias still read as a fallback
		"redis.sentinel_password",   // was missing from the old redaction map
		"recommendations.openai_api_key",
		"tmdb.api_key",
		"requests.radarr.api_key",
		"requests.sonarr.api_key",
		"watchsync.trakt.client_secret",
		"email.smtp_password",
		"discord.client_secret",
		"discord.bot_token",
		"notifications.web_push.vapid_keypair",
		"notifications.push_relay_api_key",
	}
	for _, k := range mustHave {
		if !SensitiveSettingKeys[k] {
			t.Errorf("SensitiveSettingKeys missing critical secret key %q", k)
		}
	}
	mustNotHave := []string{
		"server.mode",
		"server.log_level",
		"database.max_connections",
		"jellyfin_compat.server_id",
	}
	for _, k := range mustNotHave {
		if SensitiveSettingKeys[k] {
			t.Errorf("SensitiveSettingKeys must not contain non-secret key %q", k)
		}
	}
}

func TestEncryptedSettings_BackfillIdempotent(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	dec := NewEncryptedSettingsRepo(raw, newCipher(t))

	key := pickSensitiveKey()
	raw.m[key] = "plaintext-secret"     // legacy row
	raw.m["server.mode"] = "integrated" // non-sensitive, must be left alone

	n, err := dec.BackfillSensitiveSettings(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 1 {
		t.Fatalf("backfill encrypted %d, want 1", n)
	}
	if !secret.IsEncrypted(raw.m[key]) {
		t.Fatalf("after backfill raw[%q] = %q, want ciphertext", key, raw.m[key])
	}
	if raw.m["server.mode"] != "integrated" {
		t.Fatalf("backfill mutated non-sensitive key: %q", raw.m["server.mode"])
	}
	// Decryptable back to the original.
	if got, _ := dec.Get(ctx, key); got != "plaintext-secret" {
		t.Fatalf("post-backfill Get = %q, want original", got)
	}
	// Second run is a no-op.
	n2, err := dec.BackfillSensitiveSettings(ctx)
	if err != nil {
		t.Fatalf("backfill 2: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second backfill encrypted %d, want 0 (no double-encrypt)", n2)
	}
}
