package audiobooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

const (
	absJWTSecretKey          = "audiobooks.abs.jwt_secret"
	absDefaultAccessTTL      = 24 * time.Hour
	absDefaultRefreshTTL     = 30 * 24 * time.Hour
)

// ABSConfigProvider implements abs.ConfigProvider using silo's server_settings
// table. The ABS JWT secret is generated once on first read and persisted for
// the lifetime of the deployment.
type ABSConfigProvider struct {
	Settings *catalog.ServerSettingsRepo

	// secretCache holds the generated secret so we only hit the DB once per
	// process lifetime. Protected by mu.
	mu          sync.Mutex
	secretCache []byte
}

var _ abs.ConfigProvider = (*ABSConfigProvider)(nil)

// JWTSecret returns the HMAC-SHA256 signing key for ABS JWTs. On the very
// first call it generates a random 32-byte key, persists it in server_settings
// under "audiobooks.abs.jwt_secret" (as hex), and caches it for the process
// lifetime. Subsequent calls return the cached value without touching the DB.
func (c *ABSConfigProvider) JWTSecret(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.secretCache) > 0 {
		return c.secretCache, nil
	}

	existing, err := c.Settings.Get(ctx, absJWTSecretKey)
	if err != nil {
		return nil, fmt.Errorf("abs_config: read jwt secret: %w", err)
	}
	if existing != "" {
		decoded, err := hex.DecodeString(existing)
		if err != nil {
			return nil, fmt.Errorf("abs_config: decode jwt secret: %w", err)
		}
		c.secretCache = decoded
		return c.secretCache, nil
	}

	// Generate a new secret and persist it.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("abs_config: generate jwt secret: %w", err)
	}
	if err := c.Settings.Set(ctx, absJWTSecretKey, hex.EncodeToString(secret)); err != nil {
		// Non-fatal: use the in-memory key for this boot. Next start may
		// differ, invalidating existing tokens — acceptable at early deploy.
		_ = err
	}
	c.secretCache = secret
	return c.secretCache, nil
}

// AccessTTL returns the default access token lifetime.
// Returns 0 to signal "use built-in default" when no override is set.
func (c *ABSConfigProvider) AccessTTL(_ context.Context) (time.Duration, error) {
	return absDefaultAccessTTL, nil
}

// RefreshTTL returns the default refresh token lifetime.
func (c *ABSConfigProvider) RefreshTTL(_ context.Context) (time.Duration, error) {
	return absDefaultRefreshTTL, nil
}

// StandaloneLoginEnabled reports whether body-creds login is permitted.
// We default to true; an operator can set "audiobooks.abs.login_disabled"
// to "true" in server_settings to gate it off.
func (c *ABSConfigProvider) StandaloneLoginEnabled(ctx context.Context) (bool, error) {
	disabled, err := c.Settings.Get(ctx, "audiobooks.abs.login_disabled")
	if err != nil {
		return true, nil // fail open
	}
	return disabled != "true", nil
}
