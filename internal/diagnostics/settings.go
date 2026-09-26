package diagnostics

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	KeyUploadsEnabled         = "diagnostics.uploads_enabled"
	KeyMaxBundleBytes         = "diagnostics.max_bundle_bytes"
	KeyMaxUncompressedBytes   = "diagnostics.max_uncompressed_bytes"
	KeyMaxReportsPerUserDay   = "diagnostics.max_reports_per_user_per_day"
	KeyRetentionDays          = "diagnostics.retention_days"
	KeyMaxBytesPerUser        = "diagnostics.max_bytes_per_user"
	KeyConsentNoticeVersion   = "diagnostics.consent_notice_version"
	KeyCleanupIntervalMinutes = "diagnostics.cleanup_interval_minutes"
	KeyServerInstanceID       = "diagnostics.server_instance_id"
	DefaultUploadsEnabled     = false
	DefaultMaxBundleBytes     = int64(10 * 1024 * 1024)
	DefaultMaxUncompressed    = int64(64 * 1024 * 1024)
	DefaultMaxReportsPerDay   = 20
	DefaultRetentionDays      = 30
	DefaultMaxBytesPerUser    = int64(200 * 1024 * 1024)
	DefaultConsentNoticeVer   = 1
	// DefaultCleanupIntervalMinutes matches the prior opslog-shared cadence so
	// splitting diagnostics onto its own key preserves current behavior.
	DefaultCleanupIntervalMinutes = 15
	// maxCleanupIntervalMinutes caps the parsed interval well below the point
	// where minutes * time.Minute overflows int64 nanoseconds (~1.5e8 minutes,
	// which would wrap a huge configured value into a tiny — or negative —
	// duration). 7 days is far longer than any sane cleanup cadence.
	maxCleanupIntervalMinutes = 7 * 24 * 60
	defaultUploadsEnabledStr  = "false"
	defaultMaxBundleStr       = "10485760"
	defaultMaxUncompressed    = "67108864"
	defaultMaxReportsStr      = "20"
	defaultRetentionDaysStr   = "30"
	defaultMaxBytesUserStr    = "209715200"
	defaultConsentNoticeStr   = "1"
	defaultCleanupIntervalStr = "15"
)

// SettingsStore is the read/write surface over server_settings used by the
// diagnostics feature gate. catalog.ServerSettingsRepo satisfies it.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// conditionalSettingsStore is the optional insert-if-absent surface used to seed
// generated singletons (the server instance ID) without racing concurrent
// nodes. catalog.ServerSettingsRepo satisfies it.
type conditionalSettingsStore interface {
	SetIfAbsent(ctx context.Context, key, value string) (bool, error)
}

type Settings struct {
	UploadsEnabled       bool
	MaxBundleBytes       int64
	MaxUncompressedBytes int64
	MaxReportsPerUserDay int
	RetentionDays        int
	MaxBytesPerUser      int64
	ConsentNoticeVersion int
	ServerInstanceID     string
}

func SeedDefaults(ctx context.Context, store SettingsStore) error {
	defaults := map[string]string{
		KeyUploadsEnabled:         defaultUploadsEnabledStr,
		KeyMaxBundleBytes:         defaultMaxBundleStr,
		KeyMaxUncompressedBytes:   defaultMaxUncompressed,
		KeyMaxReportsPerUserDay:   defaultMaxReportsStr,
		KeyRetentionDays:          defaultRetentionDaysStr,
		KeyMaxBytesPerUser:        defaultMaxBytesUserStr,
		KeyConsentNoticeVersion:   defaultConsentNoticeStr,
		KeyCleanupIntervalMinutes: defaultCleanupIntervalStr,
	}
	for key, value := range defaults {
		existing, err := store.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("seed diagnostics defaults for %s: %w", key, err)
		}
		if existing != "" {
			continue
		}
		if err := store.Set(ctx, key, value); err != nil {
			return fmt.Errorf("seed diagnostics default %s: %w", key, err)
		}
	}

	if _, err := ensureServerInstanceID(ctx, store); err != nil {
		return err
	}
	return nil
}

// ServerInstanceID returns the existing installation identity without depending
// on diagnostics being enabled. Atomic initialization is mandatory for callers
// that bind durable cross-node state to this identity.
func ServerInstanceID(ctx context.Context, store SettingsStore) (string, error) {
	if _, ok := store.(conditionalSettingsStore); !ok {
		return "", fmt.Errorf("installation identity requires atomic settings initialization")
	}
	return ensureServerInstanceID(ctx, store)
}

// ensureServerInstanceID returns the diagnostics server instance ID, generating
// and persisting one when absent. Seeding is atomic when the store supports
// insert-if-absent: concurrent nodes converge on the single winning value
// instead of overwriting each other (which would reject already-issued
// destination IDs as destination_mismatch).
func ensureServerInstanceID(ctx context.Context, store SettingsStore) (string, error) {
	existing, err := store.Get(ctx, KeyServerInstanceID)
	if err != nil {
		return "", fmt.Errorf("seed diagnostics server instance id: %w", err)
	}
	if id := strings.TrimSpace(existing); id != "" {
		return id, nil
	}

	instanceID, err := newServerInstanceID()
	if err != nil {
		return "", err
	}

	conditional, ok := store.(conditionalSettingsStore)
	if !ok {
		if err := store.Set(ctx, KeyServerInstanceID, instanceID); err != nil {
			return "", fmt.Errorf("seed diagnostics server instance id: %w", err)
		}
		return instanceID, nil
	}

	if _, err := conditional.SetIfAbsent(ctx, KeyServerInstanceID, instanceID); err != nil {
		return "", fmt.Errorf("seed diagnostics server instance id: %w", err)
	}
	// Re-read so a node that lost the insert race adopts the winning value.
	winner, err := store.Get(ctx, KeyServerInstanceID)
	if err != nil {
		return "", fmt.Errorf("seed diagnostics server instance id: %w", err)
	}
	if id := strings.TrimSpace(winner); id != "" {
		return id, nil
	}
	return instanceID, nil
}

// LoadCleanupInterval returns the configured diagnostics cleanup interval. It is
// intentionally independent of the opslog cleanup cadence so tuning one does not
// silently move the other.
func LoadCleanupInterval(ctx context.Context, store SettingsStore) time.Duration {
	minutes := DefaultCleanupIntervalMinutes
	if store == nil {
		return time.Duration(minutes) * time.Minute
	}
	if raw, err := store.Get(ctx, KeyCleanupIntervalMinutes); err == nil && strings.TrimSpace(raw) != "" {
		if parsed := parseInt(raw); parsed > 0 {
			minutes = parsed
			if minutes > maxCleanupIntervalMinutes {
				minutes = maxCleanupIntervalMinutes
			}
		}
	}
	return time.Duration(minutes) * time.Minute
}

func DefaultSettings() Settings {
	return Settings{
		UploadsEnabled:       DefaultUploadsEnabled,
		MaxBundleBytes:       DefaultMaxBundleBytes,
		MaxUncompressedBytes: DefaultMaxUncompressed,
		MaxReportsPerUserDay: DefaultMaxReportsPerDay,
		RetentionDays:        DefaultRetentionDays,
		MaxBytesPerUser:      DefaultMaxBytesPerUser,
		ConsentNoticeVersion: DefaultConsentNoticeVer,
	}
}

func LoadSettings(ctx context.Context, store SettingsStore) (Settings, error) {
	settings := DefaultSettings()
	if store == nil {
		return settings, nil
	}
	// get distinguishes a genuine read failure — propagated so callers can
	// surface a retryable error — from a missing or empty value, which falls
	// back to the default. Silently defaulting on a transient DB error could
	// report uploads disabled or the wrong quota limits to clients making
	// decisions from /diagnostics/status.
	get := func(key string) (string, error) {
		raw, err := store.Get(ctx, key)
		if err != nil {
			return "", fmt.Errorf("load diagnostics setting %s: %w", key, err)
		}
		return strings.TrimSpace(raw), nil
	}

	if raw, err := get(KeyUploadsEnabled); err != nil {
		return Settings{}, err
	} else if raw != "" {
		if parsed, parseErr := strconv.ParseBool(raw); parseErr == nil {
			settings.UploadsEnabled = parsed
		}
	}
	if raw, err := get(KeyMaxBundleBytes); err != nil {
		return Settings{}, err
	} else if parsed := parseInt64(raw); parsed > 0 {
		settings.MaxBundleBytes = parsed
	}
	if raw, err := get(KeyMaxUncompressedBytes); err != nil {
		return Settings{}, err
	} else if parsed := parseInt64(raw); parsed > 0 {
		settings.MaxUncompressedBytes = parsed
	}
	if raw, err := get(KeyMaxReportsPerUserDay); err != nil {
		return Settings{}, err
	} else if parsed := parseInt(raw); parsed > 0 {
		settings.MaxReportsPerUserDay = parsed
	}
	if raw, err := get(KeyRetentionDays); err != nil {
		return Settings{}, err
	} else if parsed := parseInt(raw); parsed > 0 {
		settings.RetentionDays = parsed
	}
	if raw, err := get(KeyMaxBytesPerUser); err != nil {
		return Settings{}, err
	} else if parsed := parseInt64(raw); parsed > 0 {
		settings.MaxBytesPerUser = parsed
	}
	if raw, err := get(KeyConsentNoticeVersion); err != nil {
		return Settings{}, err
	} else if parsed := parseInt(raw); parsed > 0 {
		settings.ConsentNoticeVersion = parsed
	}
	if raw, err := get(KeyServerInstanceID); err != nil {
		return Settings{}, err
	} else {
		settings.ServerInstanceID = raw
	}
	return settings, nil
}

func newServerInstanceID() (string, error) {
	return uuid.NewString(), nil
}

func parseInt(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return v
}

func parseInt64(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
