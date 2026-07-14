package notifications

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SettingReader reads live server settings. Satisfied by
// catalog.ServerSettingsRepo and catalog.EncryptedSettingsRepo; declared
// locally to avoid a catalog dependency.
type SettingReader interface {
	Get(ctx context.Context, key string) (string, error)
}

type settingBatchWriter interface {
	SetMany(ctx context.Context, values map[string]string) error
}

// Server-setting keys for the notification system. All keys are live (no
// restart required): consumers read them through Settings, which caches reads
// briefly. The enabled flags default to on and act as kill switches — except
// webhooks and Discord, which are opt-in and stay off until an admin enables
// them. Flood safety comes from per-library seed markers, not from staged
// flag flips.
const (
	SettingReleaseEventsEnabled = "notifications.release_events_enabled"
	SettingFanoutEnabled        = "notifications.fanout_enabled"
	SettingUIEnabled            = "notifications.ui_enabled"
	SettingFanoutSettleSeconds  = "notifications.fanout.settle_seconds"
	SettingFanoutMaxSeriesBurst = "notifications.fanout.max_series_burst"
	SettingFanoutMaxEventAge    = "notifications.fanout.max_event_age_hours"
	SettingRetentionReadDays    = "notifications.retention.read_days"
	SettingRetentionUnreadDays  = "notifications.retention.unread_days"
	SettingRetentionEventDays   = "notifications.retention.event_days"

	SettingWebhooksEnabled       = "notifications.webhooks_enabled"
	SettingWebhooksMaxPerProfile = "notifications.webhooks.max_per_profile"
	SettingWebhooksAllowPrivate  = "notifications.webhooks.allow_private_destinations"
	SettingWebhooksRatePerMinute = "notifications.webhooks.deliveries_per_minute_per_profile"

	SettingEmailEnabled         = "notifications.email_enabled"
	SettingEmailAllowPerEpisode = "notifications.email.allow_per_episode"
	SettingEmailDigestHour      = "notifications.email.digest_hour"
	SettingEmailExternalURL     = "notifications.email.external_url"

	SettingDiscordEnabled         = "notifications.discord_enabled"
	SettingDiscordAllowPerEpisode = "notifications.discord.allow_per_episode"
	SettingDiscordDigestHour      = "notifications.discord.digest_hour"
	SettingDiscordPosterMode      = "notifications.discord.poster_mode"

	SettingServerChannelsEnabled          = "notifications.server_channels_enabled"
	SettingServerChannelsBatchSeconds     = "notifications.server_channels.batch_seconds"
	SettingServerChannelMentionRequesters = "notifications.server_channels.mention_requesters"

	SettingApplePushDeliveryEnabled   = "notifications.apple_push_delivery_enabled"
	SettingAndroidPushDeliveryEnabled = "notifications.android_push_delivery_enabled"
	SettingPushRelayURL               = "notifications.push_relay_url"
	SettingPushRelayDeploymentID      = "notifications.push_relay_deployment_id"
	SettingPushRelayAPIKey            = "notifications.push_relay_api_key"
	SettingPushRelayExpiresAt         = "notifications.push_relay_expires_at"
	SettingPushRelayKeyPrefix         = "notifications.push_relay_key_prefix"
	SettingPushRelayReregister        = "notifications.push_relay_reregistration_required"

	// Discord application credentials live under the discord.* namespace
	// (admin-configured, alongside email.smtp_*). The secret and bot token
	// are registered in catalog.SensitiveSettingKeys and encrypted at rest.
	SettingDiscordClientID     = "discord.client_id"
	SettingDiscordClientSecret = "discord.client_secret"
	SettingDiscordBotToken     = "discord.bot_token"
)

const (
	defaultSettleSeconds      = 30
	defaultMaxSeriesBurst     = 3
	defaultMaxEventAgeHours   = 72
	defaultRetentionReadDays  = 90
	defaultRetentionUnread    = 180
	defaultRetentionEventDays = 30
	// defaultDigestHour applies to every account-level digest channel
	// (email, Discord).
	defaultDigestHour = 8

	// defaultServerChannelsBatchSeconds is the server-channel content batch
	// window: how old a release event must be before the sweep reads it, so a
	// season pack lands in one grouped post. The minimum must stay >= the
	// availability detector's timeout (120s): an in-flight availability
	// transaction can commit events whose created_at predates rows the sweep
	// already passed, and the window is what keeps those visible.
	defaultServerChannelsBatchSeconds = 300
	minServerChannelsBatchSeconds     = 120
	// DefaultPushRelayURL is the public Silo relay origin used when no
	// notifications.push_relay_url override is stored.
	DefaultPushRelayURL = "https://push.siloserver.org"

	settingsCacheTTL = 15 * time.Second
)

// Settings exposes typed accessors over live server settings with a short
// read-through cache so hot paths (ingest, fanout loop) do not hit the
// settings table on every call.
type Settings struct {
	reader SettingReader
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]settingsCacheEntry
}

type settingsCacheEntry struct {
	value     string
	fetchedAt time.Time
}

// NewSettings creates a Settings facade. reader may be nil, in which case all
// accessors return their defaults.
func NewSettings(reader SettingReader) *Settings {
	return &Settings{
		reader: reader,
		now:    time.Now,
		cache:  make(map[string]settingsCacheEntry),
	}
}

func (s *Settings) raw(ctx context.Context, key string) string {
	if s == nil || s.reader == nil {
		return ""
	}
	s.mu.Lock()
	entry, ok := s.cache[key]
	if ok && s.now().Sub(entry.fetchedAt) < settingsCacheTTL {
		s.mu.Unlock()
		return entry.value
	}
	s.mu.Unlock()

	value, err := s.reader.Get(ctx, key)
	if err != nil {
		// Fall back to the stale cached value (if any) rather than flapping
		// to defaults on transient DB errors.
		if ok {
			return entry.value
		}
		return ""
	}

	s.mu.Lock()
	s.cache[key] = settingsCacheEntry{value: value, fetchedAt: s.now()}
	s.mu.Unlock()
	return value
}

// Invalidate drops cached values so the next read hits the store. Admin test
// paths use it: a test typically runs seconds after a settings save, inside
// the read-cache TTL, and must see the just-saved value.
func (s *Settings) Invalidate(keys ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, key := range keys {
		delete(s.cache, key)
	}
	s.mu.Unlock()
}

// PushRelayCredential is the complete persisted identity of one relay
// capability generation. It must always be replaced as one atomic unit.
type PushRelayCredential struct {
	RelayURL               string
	DeploymentID           string
	APIKey                 string
	ExpiresAt              time.Time
	KeyPrefix              string
	ReregistrationRequired bool
}

// UpdatePushRelayCredential atomically persists all fields that identify a
// relay credential. Production requires a batch-capable settings repository;
// refusing a sequential fallback is what preserves the invariant.
func (s *Settings) UpdatePushRelayCredential(ctx context.Context, credential PushRelayCredential) error {
	if s == nil || s.reader == nil {
		return errors.New("push relay settings are unavailable")
	}
	writer, ok := s.reader.(settingBatchWriter)
	if !ok {
		return errors.New("push relay settings do not support atomic writes")
	}
	expiresAt := ""
	if !credential.ExpiresAt.IsZero() {
		expiresAt = credential.ExpiresAt.UTC().Format(time.RFC3339)
	}
	values := map[string]string{
		SettingPushRelayURL:          strings.TrimRight(strings.TrimSpace(credential.RelayURL), "/"),
		SettingPushRelayDeploymentID: strings.TrimSpace(credential.DeploymentID),
		SettingPushRelayAPIKey:       strings.TrimSpace(credential.APIKey),
		SettingPushRelayExpiresAt:    expiresAt,
		SettingPushRelayKeyPrefix:    strings.TrimSpace(credential.KeyPrefix),
		SettingPushRelayReregister:   strconv.FormatBool(credential.ReregistrationRequired),
	}
	if err := writer.SetMany(ctx, values); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	s.Invalidate(keys...)
	return nil
}

func (s *Settings) boolSetting(ctx context.Context, key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(s.raw(ctx, key)))
	switch raw {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}

func (s *Settings) intSetting(ctx context.Context, key string, fallback, min, max int) int {
	raw := strings.TrimSpace(s.raw(ctx, key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}

// ReleaseEventsEnabled gates release-event creation during ingest.
func (s *Settings) ReleaseEventsEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingReleaseEventsEnabled, true)
}

// FanoutEnabled gates the fanout worker.
func (s *Settings) FanoutEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingFanoutEnabled, true)
}

// UIEnabled gates the inbox/preferences API surface advertised to clients.
func (s *Settings) UIEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingUIEnabled, true)
}

// SettleDelay is how old a release event must be before the fanout worker
// claims it, so one scan's burst for a series lands in the same claim batch.
func (s *Settings) SettleDelay(ctx context.Context) time.Duration {
	return time.Duration(s.intSetting(ctx, SettingFanoutSettleSeconds, defaultSettleSeconds, 0, 3600)) * time.Second
}

// MaxSeriesBurst is the per-(library, series) cap on fanned-out events per
// claim batch; the remainder is suppressed with suppressed_reason.
func (s *Settings) MaxSeriesBurst(ctx context.Context) int {
	return s.intSetting(ctx, SettingFanoutMaxSeriesBurst, defaultMaxSeriesBurst, 1, 1000)
}

// MaxEventAge bounds how old an unprocessed release event may be and still
// fan out. Events past the horizon (fanout disabled for a stretch, extended
// downtime) are suppressed as stale instead of being delivered long after the
// fact; retention deletes them.
func (s *Settings) MaxEventAge(ctx context.Context) time.Duration {
	return time.Duration(s.intSetting(ctx, SettingFanoutMaxEventAge, defaultMaxEventAgeHours, 1, 24*365)) * time.Hour
}

// ReadRetentionDays bounds how long read inbox rows are kept.
func (s *Settings) ReadRetentionDays(ctx context.Context) int {
	return s.intSetting(ctx, SettingRetentionReadDays, defaultRetentionReadDays, 1, 3650)
}

// UnreadRetentionDays bounds how long unread inbox rows are kept.
func (s *Settings) UnreadRetentionDays(ctx context.Context) int {
	return s.intSetting(ctx, SettingRetentionUnreadDays, defaultRetentionUnread, 1, 3650)
}

// EventRetentionDays bounds how long processed release events are kept for
// debugging.
func (s *Settings) EventRetentionDays(ctx context.Context) int {
	return s.intSetting(ctx, SettingRetentionEventDays, defaultRetentionEventDays, 1, 3650)
}

// WebhooksEnabled gates the outbound webhooks channel. Unlike the other
// channel flags this is opt-in: letting users point server-originated HTTP at
// arbitrary destinations is an admin decision, so creation, test sends, and
// delivery all stay off until an admin enables the setting.
func (s *Settings) WebhooksEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingWebhooksEnabled, false)
}

// WebhooksMaxPerProfile caps how many webhooks one profile may create.
func (s *Settings) WebhooksMaxPerProfile(ctx context.Context) int {
	return s.intSetting(ctx, SettingWebhooksMaxPerProfile, 10, 1, 100)
}

// WebhooksAllowPrivateDestinations disables the private-destination guard.
// Intended only for development environments.
func (s *Settings) WebhooksAllowPrivateDestinations(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingWebhooksAllowPrivate, false)
}

// WebhooksDeliveriesPerMinute is the per-profile webhook delivery rate limit.
// Over-limit notifications stay in the inbox; webhooks just don't fire.
func (s *Settings) WebhooksDeliveriesPerMinute(ctx context.Context) int {
	return s.intSetting(ctx, SettingWebhooksRatePerMinute, 60, 1, 10000)
}

// EmailEnabled gates the email notification channel (kill switch). Actual
// availability additionally requires a configured SMTP sender (mail.Sender).
func (s *Settings) EmailEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingEmailEnabled, true)
}

// EmailAllowPerEpisode controls whether users may choose per-episode email
// alerts. When off, accounts set to per-episode are coerced to the daily
// digest instead of going silent.
func (s *Settings) EmailAllowPerEpisode(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingEmailAllowPerEpisode, true)
}

// EmailDigestHour is the hour of day (0-23, server-local time) at which daily
// digest emails go out.
func (s *Settings) EmailDigestHour(ctx context.Context) int {
	return s.intSetting(ctx, SettingEmailDigestHour, defaultDigestHour, 0, 23)
}

// EmailExternalURL is the externally reachable base URL of this server, used
// for deep links inside notification emails. Empty renders emails without
// links (webhooks deliberately never leak the origin; email is opt-in here).
func (s *Settings) EmailExternalURL(ctx context.Context) string {
	return strings.TrimRight(strings.TrimSpace(s.raw(ctx, SettingEmailExternalURL)), "/")
}

// DiscordEnabled is the master switch for the Discord bot integration. Like
// webhooks it is opt-in: while off, the channel never delivers, linking is
// refused, and clients are told the channel is unavailable (which hides the
// Discord section in user settings). Actual availability additionally
// requires the configured bot credentials.
func (s *Settings) DiscordEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingDiscordEnabled, false)
}

// Discord embed poster modes (SettingDiscordPosterMode values).
const (
	// DiscordPostersOff renders all Discord embeds without artwork.
	DiscordPostersOff = "off"
	// DiscordPostersProvider (default) allows artwork served by public
	// provider CDNs only (image.tmdb.org, artworks.thetvdb.com).
	DiscordPostersProvider = "provider"
	// DiscordPostersServer additionally falls back to presigned URLs from
	// this server's own image storage for locally cached artwork. The
	// storage origin becomes visible to the destination and must be
	// reachable from the internet for Discord to render the image.
	DiscordPostersServer = "server"
)

// DiscordPosterMode controls artwork in outbound Discord embeds; unknown
// values fall back to the provider-CDN-only default.
func (s *Settings) DiscordPosterMode(ctx context.Context) string {
	switch strings.TrimSpace(s.raw(ctx, SettingDiscordPosterMode)) {
	case DiscordPostersOff:
		return DiscordPostersOff
	case DiscordPostersServer:
		return DiscordPostersServer
	default:
		return DiscordPostersProvider
	}
}

// DiscordAllowPerEpisode controls whether users may choose per-episode
// Discord DMs. When off, accounts set to per-episode are coerced to the daily
// digest instead of going silent.
func (s *Settings) DiscordAllowPerEpisode(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingDiscordAllowPerEpisode, true)
}

// DiscordDigestHour is the hour of day (0-23, server-local time) at which
// daily digest DMs go out.
func (s *Settings) DiscordDigestHour(ctx context.Context) int {
	return s.intSetting(ctx, SettingDiscordDigestHour, defaultDigestHour, 0, 23)
}

// ServerChannelsEnabled gates the admin server-channel feature (kill switch).
// Defaults to on: unlike profile webhooks, every destination is created by an
// admin, so creating a channel is itself the opt-in act.
func (s *Settings) ServerChannelsEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingServerChannelsEnabled, true)
}

// ServerChannelMentionRequesters controls whether server-channel request
// posts to Discord destinations @mention the requesting user (when that
// account has linked Discord). Opt-in: a mention pings the user, which an
// admin must choose deliberately for a shared channel.
func (s *Settings) ServerChannelMentionRequesters(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingServerChannelMentionRequesters, false)
}

// ServerChannelsBatchWindow is how old a release event must be before the
// server-channel sweep reads it (content posts batch within this window).
func (s *Settings) ServerChannelsBatchWindow(ctx context.Context) time.Duration {
	return time.Duration(s.intSetting(ctx, SettingServerChannelsBatchSeconds,
		defaultServerChannelsBatchSeconds, minServerChannelsBatchSeconds, 3600)) * time.Second
}

// ApplePushDeliveryEnabled gates relay sends and the capability endpoint's
// apple_push availability, mirroring how web push advertises itself. The
// device registration endpoint stays available independently so clients that
// already hold tokens keep them fresh across admin toggles.
func (s *Settings) ApplePushDeliveryEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingApplePushDeliveryEnabled, false)
}

// AndroidPushDeliveryEnabled is the Android counterpart of
// ApplePushDeliveryEnabled: it gates relay FCM sends and the capability
// endpoint's android_push availability.
func (s *Settings) AndroidPushDeliveryEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingAndroidPushDeliveryEnabled, false)
}

// PushDeliveryEnabled reports whether any push platform may deliver; the
// shared dispatcher uses it as its wake-up gate.
func (s *Settings) PushDeliveryEnabled(ctx context.Context) bool {
	return s.ApplePushDeliveryEnabled(ctx) || s.AndroidPushDeliveryEnabled(ctx)
}

// EnabledPushPlatforms lists the platforms whose admin delivery toggle is on,
// in the shape ListEnabledPushByProfiles expects.
func (s *Settings) EnabledPushPlatforms(ctx context.Context) []string {
	platforms := make([]string, 0, 2)
	if s.ApplePushDeliveryEnabled(ctx) {
		platforms = append(platforms, PushPlatformApple)
	}
	if s.AndroidPushDeliveryEnabled(ctx) {
		platforms = append(platforms, PushPlatformAndroid)
	}
	return platforms
}

// PushRelayURL is the public Silo relay origin.
func (s *Settings) PushRelayURL(ctx context.Context) string {
	value := strings.TrimRight(strings.TrimSpace(s.raw(ctx, SettingPushRelayURL)), "/")
	if value == "" {
		return DefaultPushRelayURL
	}
	return value
}

// PushRelayAPIKey is the bearer credential for the Silo relay.
func (s *Settings) PushRelayAPIKey(ctx context.Context) string {
	return strings.TrimSpace(s.raw(ctx, SettingPushRelayAPIKey))
}

// PushRelayDeploymentID is the relay account identifier returned during
// self-registration. The dispatcher does not need it; registration uses it for
// subsequent key rotations.
func (s *Settings) PushRelayDeploymentID(ctx context.Context) string {
	return strings.TrimSpace(s.raw(ctx, SettingPushRelayDeploymentID))
}

func (s *Settings) PushRelayExpiresAt(ctx context.Context) time.Time {
	value := strings.TrimSpace(s.raw(ctx, SettingPushRelayExpiresAt))
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func (s *Settings) PushRelayKeyPrefix(ctx context.Context) string {
	return strings.TrimSpace(s.raw(ctx, SettingPushRelayKeyPrefix))
}

func (s *Settings) PushRelayReregistrationRequired(ctx context.Context) bool {
	value, _ := strconv.ParseBool(strings.TrimSpace(s.raw(ctx, SettingPushRelayReregister)))
	return value
}

// DiscordClientID is the Discord application's OAuth2 client ID.
func (s *Settings) DiscordClientID(ctx context.Context) string {
	return strings.TrimSpace(s.raw(ctx, SettingDiscordClientID))
}

// DiscordClientSecret is the Discord application's OAuth2 client secret.
func (s *Settings) DiscordClientSecret(ctx context.Context) string {
	return strings.TrimSpace(s.raw(ctx, SettingDiscordClientSecret))
}

// DiscordBotToken is the Discord bot token used to open DM channels and send
// messages.
func (s *Settings) DiscordBotToken(ctx context.Context) string {
	return strings.TrimSpace(s.raw(ctx, SettingDiscordBotToken))
}
