package backup

// The backup settings and the destination they select.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Settings are read with their errors: a value that is absent or invalid
// takes its default, but a read that failed fails the operation. Falling
// back on a failed read could rotate with the wrong count or leave out what
// a backup was set to include.

func (s *Service) readSetting(ctx context.Context, key string) (string, error) {
	if s.opts.Settings == nil {
		return "", nil
	}
	raw, err := s.opts.Settings.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", key, err)
	}
	return strings.TrimSpace(raw), nil
}

func (s *Service) settingBool(ctx context.Context, key string, fallback bool) (bool, error) {
	raw, err := s.readSetting(ctx, key)
	if err != nil || raw == "" {
		return fallback, err
	}
	if value, parseErr := strconv.ParseBool(raw); parseErr == nil {
		return value, nil
	}
	return fallback, nil
}

func (s *Service) settingKeep(ctx context.Context) (int, error) {
	raw, err := s.readSetting(ctx, config.BackupKeepSettingKey)
	if err != nil {
		return 0, err
	}
	if keep, parseErr := strconv.Atoi(raw); parseErr == nil && keep >= config.MinBackupKeep && keep <= config.MaxBackupKeep {
		return keep, nil
	}
	return config.DefaultBackupKeep, nil
}

// target resolves the destination an operation starting now uses: the
// operational store on this server's local storage.
func (s *Service) target(context.Context) (target, error) {
	if s.opts.Destination == nil {
		return target{}, ErrNoDestination
	}
	return target{Destination: s.opts.Destination}, nil
}

// contentSettings reads what a backup includes: intro fingerprints and
// downloaded subtitles.
func (s *Service) contentSettings(ctx context.Context) (fingerprints, subtitles bool, err error) {
	if fingerprints, err = s.settingBool(ctx, config.BackupIncludeIntroFingerprintsSettingKey, false); err != nil {
		return false, false, err
	}
	subtitles, err = s.settingBool(ctx, config.BackupIncludeSubtitlesSettingKey, true)
	return fingerprints, subtitles, err
}
