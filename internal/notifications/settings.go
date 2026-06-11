package notifications

import (
	"context"
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

// Server-setting keys for the notification system. All keys are live (no
// restart required): consumers read them through Settings, which caches reads
// briefly. The enabled flags default to on and act as kill switches; flood
// safety comes from per-library seed markers, not from staged flag flips.
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
)

const (
	defaultSettleSeconds      = 30
	defaultMaxSeriesBurst     = 3
	defaultMaxEventAgeHours   = 72
	defaultRetentionReadDays  = 90
	defaultRetentionUnread    = 180
	defaultRetentionEventDays = 30

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

// WebhooksEnabled gates the outbound webhooks channel (kill switch).
func (s *Settings) WebhooksEnabled(ctx context.Context) bool {
	return s.boolSetting(ctx, SettingWebhooksEnabled, true)
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
