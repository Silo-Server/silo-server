package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// JWTSecretSettingKey is the server_settings row that holds the signing secret
// every process of a deployment shares: login tokens, profile tokens, stream
// tokens and signed artwork URLs.
const JWTSecretSettingKey = "auth.jwt_secret" //nolint:gosec // G101: a setting name, not a credential

// JWTSecretStore is the server_settings surface EnsureJWTSecret needs.
// catalog.EncryptedSettingsRepo satisfies it.
type JWTSecretStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	SetIfAbsent(ctx context.Context, key, value string) (bool, error)
}

// EnsureJWTSecret returns the stored signing secret, generating and storing
// one when none is stored. Processes that start at once on a new database
// each generate a secret, but only the first write lands, and every process
// returns the stored value, so all of them sign with the same secret.
//
// A stored value of only whitespace counts as no secret. It is encrypted at
// rest, so the conditional write cannot see it as blank; it is replaced with
// a plain write instead, which keeps the last writer's secret if processes
// race on such a row.
func EnsureJWTSecret(ctx context.Context, store JWTSecretStore) (string, error) {
	stored, err := store.Get(ctx, JWTSecretSettingKey)
	if err != nil {
		return "", fmt.Errorf("reading JWT secret: %w", err)
	}
	if strings.TrimSpace(stored) != "" {
		return stored, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating JWT secret: %w", err)
	}
	generated := base64.StdEncoding.EncodeToString(raw)
	if stored == "" {
		_, err = store.SetIfAbsent(ctx, JWTSecretSettingKey, generated)
	} else {
		err = store.Set(ctx, JWTSecretSettingKey, generated)
	}
	if err != nil {
		return "", fmt.Errorf("storing generated JWT secret: %w", err)
	}
	winner, err := store.Get(ctx, JWTSecretSettingKey)
	if err != nil {
		return "", fmt.Errorf("reading JWT secret back: %w", err)
	}
	if strings.TrimSpace(winner) == "" {
		return "", errors.New("JWT secret is still empty after storing one")
	}
	return winner, nil
}
