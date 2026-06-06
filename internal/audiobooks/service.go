package audiobooks

import (
	"context"
	"fmt"
)

// SettingsReader is the minimal slice of the server-settings store that
// the audiobooks service needs. The production implementation is
// internal/catalog.ServerSettingsRepo (or whatever silo names that helper at
// wiring time); tests pass a fake.
type SettingsReader interface {
	GetString(ctx context.Context, key string) (string, error)
}

// Service is the audiobooks feature's top-level orchestrator. Sub-plan 1
// exposes only Enabled(); subsequent sub-plans hang additional methods
// off Service as new capabilities (scanner branches, ABS handlers, etc.)
// come online.
type Service struct {
	settings SettingsReader
}

// New constructs a Service. The constructor takes the dependencies it
// will actually use; current sub-plan needs only the settings reader.
func New(settings SettingsReader) *Service {
	return &Service{settings: settings}
}

// Enabled reports whether the audiobooks feature flag (set by
// 160_audiobooks_feature_flag and toggled by operators) is currently true.
// Any value other than the literal string "true" reads as false; this matches how silo
// treats other boolean server_settings rows.
func (s *Service) Enabled(ctx context.Context) (bool, error) {
	if s == nil || s.settings == nil {
		return false, nil
	}
	value, err := s.settings.GetString(ctx, "audiobooks.enabled")
	if err != nil {
		return false, fmt.Errorf("read audiobooks.enabled: %w", err)
	}
	return value == "true", nil
}
