package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/oklog/ulid/v2"
)

// VAPID key settings. The keypair is stored as a single JSON value (encrypted
// at rest via SensitiveSettingKeys) so both halves persist atomically: two
// concurrent provisioners can each lose the race wholesale but can never
// strand a mismatched public/private pair. Clients receive the public half
// from the capability endpoint, never from the settings store directly.
const (
	SettingWebPushEnabled      = "notifications.web_push_enabled"
	SettingWebPushVAPIDKeypair = "notifications.web_push.vapid_keypair" //nolint:gosec // setting key name, not a credential
)

// vapidKeypair is the JSON shape persisted under SettingWebPushVAPIDKeypair.
type vapidKeypair struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

// WebPushEnabled gates the web push channel (kill switch).
func (s *Settings) WebPushEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingWebPushEnabled, true)
}

// SettingWriter persists server settings. Satisfied by
// catalog.EncryptedSettingsRepo, which encrypts sensitive keys on write.
type SettingWriter interface {
	Set(ctx context.Context, key, value string) error
}

// ErrWebPushInvalid marks rejected subscription input.
var ErrWebPushInvalid = errors.New("invalid web push subscription")

// WebPushService owns browser push subscriptions and the server's VAPID
// identity. VAPID keys are self-provisioned on first use — Web Push needs no
// third-party accounts, and payloads are end-to-end encrypted to the browser
// so the vendor push service never sees notification content.
type WebPushService struct {
	repo     *WebPushRepository
	settings *Settings
	writer   SettingWriter

	mu         sync.Mutex
	publicKey  string
	privateKey string
}

func newWebPushService(repo *WebPushRepository, settings *Settings, writer SettingWriter) *WebPushService {
	return &WebPushService{repo: repo, settings: settings, writer: writer}
}

// vapidKeys returns the server's VAPID keypair, generating and persisting one
// on first call. The keypair must stay stable for the server's lifetime:
// browsers bind subscriptions to the public key.
func (s *WebPushService) vapidKeys(ctx context.Context) (publicKey, privateKey string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publicKey != "" && s.privateKey != "" {
		return s.publicKey, s.privateKey, nil
	}

	stored := s.loadKeypair(ctx)
	if stored.Public == "" || stored.Private == "" {
		if s.writer == nil {
			return "", "", errors.New("web push requires a writable settings store")
		}
		private, public, genErr := webpush.GenerateVAPIDKeys()
		if genErr != nil {
			return "", "", fmt.Errorf("generate VAPID keys: %w", genErr)
		}
		data, marshalErr := json.Marshal(vapidKeypair{Public: public, Private: private})
		if marshalErr != nil {
			return "", "", fmt.Errorf("encode VAPID keypair: %w", marshalErr)
		}
		if err := s.writer.Set(ctx, SettingWebPushVAPIDKeypair, string(data)); err != nil {
			return "", "", fmt.Errorf("persist VAPID keypair: %w", err)
		}
		// Re-read and adopt whatever the store now holds: if another process
		// provisioned concurrently, the last write won and every node must
		// converge on that pair rather than caching its own loser.
		stored = s.loadKeypair(ctx)
		if stored.Public == "" || stored.Private == "" {
			stored = vapidKeypair{Public: public, Private: private}
		}
	}
	s.publicKey = stored.Public
	s.privateKey = stored.Private
	return s.publicKey, s.privateKey, nil
}

// loadKeypair reads the persisted keypair directly from the settings reader,
// bypassing the Settings facade cache: provisioning must observe the latest
// stored value, not a seconds-old cached miss. A corrupt value reads as
// unprovisioned and is overwritten with a fresh pair.
func (s *WebPushService) loadKeypair(ctx context.Context) vapidKeypair {
	var keys vapidKeypair
	if s.settings == nil || s.settings.reader == nil {
		return keys
	}
	raw, err := s.settings.reader.Get(ctx, SettingWebPushVAPIDKeypair)
	if err != nil || strings.TrimSpace(raw) == "" {
		return vapidKeypair{}
	}
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return vapidKeypair{}
	}
	return keys
}

// PublicKey returns the VAPID application server key clients subscribe with.
func (s *WebPushService) PublicKey(ctx context.Context) (string, error) {
	publicKey, _, err := s.vapidKeys(ctx)
	return publicKey, err
}

// Subscribe registers (or reassigns) a browser PushSubscription for the
// profile. The endpoint must be an HTTPS URL on a public host — it is
// attacker-controllable input that the server will POST to.
func (s *WebPushService) Subscribe(ctx context.Context, userID int, profileID, endpoint, p256dh, auth, deviceName string) (*WebPushSubscription, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || p256dh == "" || auth == "" {
		return nil, fmt.Errorf("%w: endpoint and keys are required", ErrWebPushInvalid)
	}
	if len(endpoint) > 2048 {
		return nil, fmt.Errorf("%w: endpoint is too long", ErrWebPushInvalid)
	}
	if _, err := ValidateWebhookURL(endpoint, false); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWebPushInvalid, err.Error())
	}
	if len(deviceName) > 128 {
		deviceName = deviceName[:128]
	}
	return s.repo.Upsert(ctx, WebPushSubscription{
		ID:         ulid.Make().String(),
		UserID:     userID,
		ProfileID:  profileID,
		Endpoint:   endpoint,
		P256dh:     p256dh,
		Auth:       auth,
		DeviceName: deviceName,
	})
}

// List returns the profile's subscriptions.
func (s *WebPushService) List(ctx context.Context, profileID string) ([]WebPushSubscription, error) {
	return s.repo.ListByProfile(ctx, profileID)
}

// Unsubscribe removes a subscription by id (profile-scoped, settings UI) or
// by endpoint (user-scoped: the browser owns the endpoint and Subscribe may
// have reassigned it to a sibling profile). Idempotent.
func (s *WebPushService) Unsubscribe(ctx context.Context, userID int, profileID, id, endpoint string) error {
	if id != "" {
		return s.repo.Delete(ctx, profileID, id)
	}
	if endpoint != "" {
		return s.repo.DeleteByEndpoint(ctx, userID, endpoint)
	}
	return fmt.Errorf("%w: an id or endpoint is required", ErrWebPushInvalid)
}
