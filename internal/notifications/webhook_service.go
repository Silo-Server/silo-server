package notifications

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/oklog/ulid/v2"
)

// Webhook service errors surfaced to the API layer.
var (
	ErrWebhookInvalid   = errors.New("invalid webhook")
	ErrWebhookNotFound  = errors.New("webhook not found")
	ErrWebhookLimit     = errors.New("webhook limit reached")
	ErrWebhooksDisabled = errors.New("webhooks are disabled by the administrator")
)

// WebhookService owns webhook CRUD, validation, and signing-secret handling.
// URLs and secrets are encrypted at rest, bound to the webhook row identity,
// and never returned after creation (the URL token IS the credential for
// Discord webhooks).
type WebhookService struct {
	repo     *WebhookRepository
	cipher   *secret.Cipher
	settings *Settings
	sender   *webhookSender
}

func newWebhookService(repo *WebhookRepository, cipher *secret.Cipher, settings *Settings, sender *webhookSender) *WebhookService {
	return &WebhookService{repo: repo, cipher: cipher, settings: settings, sender: sender}
}

// WebhookInput is the create/update request shape. Pointer fields are
// optional on update; Create requires Name and URL.
type WebhookInput struct {
	Name                   *string
	URL                    *string
	Type                   *string
	Enabled                *bool
	NotifyFavorites        *bool
	NotifyWatchlist        *bool
	NotifyContinueWatching *bool
	NotifyNextUp           *bool
	NotifyRequests         *bool
}

// validateChannelName applies the shared destination-name policy (matching
// the varchar(64) columns); invalid is the caller's sentinel to wrap.
func validateChannelName(name string, invalid error) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: name is required", invalid)
	}
	if len(trimmed) > 64 {
		return "", fmt.Errorf("%w: name must be 64 characters or fewer", invalid)
	}
	return trimmed, nil
}

// resolveWebhookType infers or validates a destination type against its URL.
// An empty explicit type auto-detects; an explicit type must match the
// destination, or the sender would apply the wrong payload/signing behavior
// from the first delivery. Shared by profile webhooks and server channels.
func resolveWebhookType(rawURL, explicitType string, invalid error) (string, error) {
	isDiscordURL := discordWebhookURL(rawURL)
	switch explicitType {
	case "":
		if isDiscordURL {
			return WebhookTypeDiscord, nil
		}
		return WebhookTypeGeneric, nil
	case WebhookTypeDiscord, WebhookTypeGeneric:
	default:
		return "", fmt.Errorf("%w: type must be discord or generic", invalid)
	}
	if explicitType == WebhookTypeDiscord && !isDiscordURL {
		return "", fmt.Errorf("%w: type discord requires a Discord webhook URL", invalid)
	}
	if explicitType == WebhookTypeGeneric && isDiscordURL {
		return "", fmt.Errorf("%w: Discord webhook URLs must use type discord", invalid)
	}
	return explicitType, nil
}

// validateReplacementURL checks a replacement URL stays compatible with the
// destination's fixed type, so existing receivers keep working.
func validateReplacementURL(hookType, rawURL string, invalid error) error {
	isDiscordURL := discordWebhookURL(rawURL)
	if hookType == WebhookTypeDiscord && !isDiscordURL {
		return fmt.Errorf("%w: a Discord webhook needs a Discord webhook URL", invalid)
	}
	if hookType == WebhookTypeGeneric && isDiscordURL {
		return fmt.Errorf("%w: Discord webhook URLs must use type discord", invalid)
	}
	return nil
}

func newSigningSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate signing secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// List returns the profile's webhooks (ciphertext fields are for internal
// use; the handler view must expose url_host only).
func (s *WebhookService) List(ctx context.Context, profileID string) ([]Webhook, error) {
	return s.repo.ListByProfile(ctx, profileID)
}

// Get returns one webhook scoped to the profile.
func (s *WebhookService) Get(ctx context.Context, profileID, id string) (*Webhook, error) {
	return s.repo.GetByID(ctx, profileID, id)
}

// Create validates and persists a new webhook. For generic webhooks the
// returned signingSecret is shown exactly once.
func (s *WebhookService) Create(ctx context.Context, userID int, profileID string, input WebhookInput) (*Webhook, string, error) {
	// Webhooks are opt-in: creation is blocked until an admin enables the
	// channel. Existing webhooks stay manageable (list/update/delete) so a
	// later disable never strands rows users can no longer remove.
	if !s.settings.WebhooksEnabled(ctx) {
		return nil, "", ErrWebhooksDisabled
	}
	if input.Name == nil || input.URL == nil {
		return nil, "", fmt.Errorf("%w: name and url are required", ErrWebhookInvalid)
	}
	name, err := validateChannelName(*input.Name, ErrWebhookInvalid)
	if err != nil {
		return nil, "", err
	}

	rawURL := strings.TrimSpace(*input.URL)
	host, err := ValidateWebhookURL(rawURL, s.settings.WebhooksAllowPrivateDestinations(ctx))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrWebhookInvalid, err.Error())
	}

	hookType := ""
	if input.Type != nil {
		hookType = strings.TrimSpace(*input.Type)
	}
	hookType, err = resolveWebhookType(rawURL, hookType, ErrWebhookInvalid)
	if err != nil {
		return nil, "", err
	}

	hook := Webhook{
		ID:                     ulid.Make().String(),
		UserID:                 userID,
		ProfileID:              profileID,
		Name:                   name,
		Type:                   hookType,
		URLHost:                host,
		Enabled:                true,
		NotifyFavorites:        boolOrDefault(input.NotifyFavorites, true),
		NotifyWatchlist:        boolOrDefault(input.NotifyWatchlist, true),
		NotifyContinueWatching: boolOrDefault(input.NotifyContinueWatching, true),
		NotifyNextUp:           boolOrDefault(input.NotifyNextUp, true),
		NotifyRequests:         boolOrDefault(input.NotifyRequests, true),
	}
	hook.URLCiphertext, err = s.cipher.Encrypt(rawURL, webhookURLAAD(hook.ID))
	if err != nil {
		return nil, "", fmt.Errorf("encrypt webhook url: %w", err)
	}

	signingSecret := ""
	if hookType == WebhookTypeGeneric {
		signingSecret, err = newSigningSecret()
		if err != nil {
			return nil, "", err
		}
		ciphertext, err := s.cipher.Encrypt(signingSecret, webhookSecretAAD(hook.ID))
		if err != nil {
			return nil, "", fmt.Errorf("encrypt signing secret: %w", err)
		}
		hook.SigningSecretCiphertext = &ciphertext
	}

	// The per-profile cap is enforced inside the insert (advisory-locked
	// count + insert), so concurrent creates cannot both slip past it.
	if err := s.repo.InsertWithLimit(ctx, hook, s.settings.WebhooksMaxPerProfile(ctx)); err != nil {
		if errors.Is(err, ErrWebhookNameTaken) {
			return nil, "", fmt.Errorf("%w: %s", ErrWebhookInvalid, err.Error())
		}
		return nil, "", err
	}
	return &hook, signingSecret, nil
}

// Update applies the provided fields. A URL change re-validates the
// destination and resets the failure streak; re-enabling clears the
// auto-disable reason.
func (s *WebhookService) Update(ctx context.Context, profileID, id string, input WebhookInput) (*Webhook, error) {
	hook, err := s.repo.GetByID(ctx, profileID, id)
	if err != nil {
		return nil, err
	}
	if hook == nil {
		return nil, ErrWebhookNotFound
	}

	if input.Name != nil {
		name, err := validateChannelName(*input.Name, ErrWebhookInvalid)
		if err != nil {
			return nil, err
		}
		hook.Name = name
	}
	if input.URL != nil {
		rawURL := strings.TrimSpace(*input.URL)
		host, err := ValidateWebhookURL(rawURL, s.settings.WebhooksAllowPrivateDestinations(ctx))
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrWebhookInvalid, err.Error())
		}
		if err := validateReplacementURL(hook.Type, rawURL, ErrWebhookInvalid); err != nil {
			return nil, err
		}
		hook.URLCiphertext, err = s.cipher.Encrypt(rawURL, webhookURLAAD(hook.ID))
		if err != nil {
			return nil, fmt.Errorf("encrypt webhook url: %w", err)
		}
		hook.URLHost = host
		hook.ConsecutiveFailures = 0
		hook.DisabledReason = nil
	}
	if input.Enabled != nil {
		hook.Enabled = *input.Enabled
		if hook.Enabled {
			hook.DisabledReason = nil
			hook.ConsecutiveFailures = 0
		}
	}
	if input.NotifyFavorites != nil {
		hook.NotifyFavorites = *input.NotifyFavorites
	}
	if input.NotifyWatchlist != nil {
		hook.NotifyWatchlist = *input.NotifyWatchlist
	}
	if input.NotifyContinueWatching != nil {
		hook.NotifyContinueWatching = *input.NotifyContinueWatching
	}
	if input.NotifyNextUp != nil {
		hook.NotifyNextUp = *input.NotifyNextUp
	}
	if input.NotifyRequests != nil {
		hook.NotifyRequests = *input.NotifyRequests
	}

	if err := s.repo.Update(ctx, *hook); err != nil {
		if errors.Is(err, ErrWebhookNameTaken) {
			return nil, fmt.Errorf("%w: %s", ErrWebhookInvalid, err.Error())
		}
		return nil, err
	}
	return hook, nil
}

// Delete removes a webhook. Idempotent.
func (s *WebhookService) Delete(ctx context.Context, profileID, id string) error {
	return s.repo.Delete(ctx, profileID, id)
}

// RotateSecret generates and stores a new signing secret for a generic
// webhook, returning it exactly once. The previous secret is gone
// immediately; there is no dual-acceptance window.
func (s *WebhookService) RotateSecret(ctx context.Context, profileID, id string) (string, error) {
	hook, err := s.repo.GetByID(ctx, profileID, id)
	if err != nil {
		return "", err
	}
	if hook == nil {
		return "", ErrWebhookNotFound
	}
	if hook.Type != WebhookTypeGeneric {
		return "", fmt.Errorf("%w: only generic webhooks have signing secrets", ErrWebhookInvalid)
	}
	signingSecret, err := newSigningSecret()
	if err != nil {
		return "", err
	}
	ciphertext, err := s.cipher.Encrypt(signingSecret, webhookSecretAAD(hook.ID))
	if err != nil {
		return "", fmt.Errorf("encrypt signing secret: %w", err)
	}
	hook.SigningSecretCiphertext = &ciphertext
	if err := s.repo.Update(ctx, *hook); err != nil {
		return "", err
	}
	return signingSecret, nil
}

// WebhookTestResult is the synchronous outcome of a test send.
type WebhookTestResult struct {
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
}

// testResult converts a send outcome to the API test-result shape.
func (r webhookSendResult) testResult() *WebhookTestResult {
	return &WebhookTestResult{
		OK:         r.OK,
		HTTPStatus: r.HTTPStatus,
		DurationMS: r.Duration.Milliseconds(),
		Message:    r.Message,
	}
}

// Test synchronously POSTs a clearly marked sample payload. Test sends never
// touch webhook_delivery_attempts or the failure counters.
func (s *WebhookService) Test(ctx context.Context, profileID, id string) (*WebhookTestResult, error) {
	// Test sends are outbound traffic; the channel gate covers them too
	// (regular delivery is already gated at enqueue and dispatch).
	if !s.settings.WebhooksEnabled(ctx) {
		return nil, ErrWebhooksDisabled
	}
	hook, err := s.repo.GetByID(ctx, profileID, id)
	if err != nil {
		return nil, err
	}
	if hook == nil {
		return nil, ErrWebhookNotFound
	}
	return s.sender.send(ctx, hook, sampleDeliveryRow(profileID), true).testResult(), nil
}

// sampleDeliveryRow is the fixture used for test sends.
func sampleDeliveryRow(profileID string) DeliveryRow {
	libraryID := 1
	seriesID := "test-series"
	episodeID := "test-episode"
	seasonNumber := 1
	episodeNumber := 1
	return DeliveryRow{
		Delivery: Delivery{
			ID:          ulid.Make().String(),
			ProfileID:   profileID,
			LibraryID:   &libraryID,
			SeriesID:    &seriesID,
			EpisodeID:   &episodeID,
			Type:        DeliveryTypeEpisodeAvailable,
			ReasonFlags: []byte(`{"favorite":true,"watchlist":false,"continue_watching":false,"next_up":false}`),
			CreatedAt:   time.Now(),
		},
		SeriesTitle:   "Silo Test Series",
		EpisodeTitle:  "This is a test notification",
		SeasonNumber:  &seasonNumber,
		EpisodeNumber: &episodeNumber,
	}
}

func boolOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
