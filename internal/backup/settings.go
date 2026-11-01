package backup

// The backup settings and the destination they select.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Settings are read with their errors: a value that is absent or invalid
// takes its default, but a read that failed fails the operation. Falling
// back on a failed read could write a backup to another destination, skip
// the S3 key check, or rotate with the wrong count.

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

// destinationSetting is backup.destination: DestinationDefault or
// DestinationS3. A failed read is an error, never the default destination,
// which would write unencrypted backups where encrypted ones were set up.
func (s *Service) destinationSetting(ctx context.Context) (string, error) {
	raw, err := s.readSetting(ctx, config.BackupDestinationSettingKey)
	if err != nil {
		return "", err
	}
	if raw == DestinationS3 {
		return DestinationS3, nil
	}
	return DestinationDefault, nil
}

// S3Settings reads the backup bucket's settings, secret key included.
func (s *Service) S3Settings(ctx context.Context) (S3Config, error) {
	if s.opts.Settings == nil {
		return S3Config{}, errors.New("backup settings are unavailable")
	}
	var c S3Config
	var err error
	for _, field := range []struct {
		key string
		to  *string
	}{
		{config.BackupS3EndpointSettingKey, &c.Endpoint}, {config.BackupS3RegionSettingKey, &c.Region},
		{config.BackupS3BucketSettingKey, &c.Bucket}, {config.BackupS3KeyPrefixSettingKey, &c.KeyPrefix},
		{config.BackupS3AccessKeySettingKey, &c.AccessKey}, {config.BackupS3SecretKeySettingKey, &c.SecretKey},
	} {
		if *field.to, err = s.readSetting(ctx, field.key); err != nil {
			return S3Config{}, err
		}
	}
	if c.PathStyle, err = s.settingBool(ctx, config.BackupS3PathStyleSettingKey, false); err != nil {
		return S3Config{}, err
	}
	return c, nil
}

// ErrS3NotConfigured reports backup.destination=s3 with no bucket set up.
var ErrS3NotConfigured = errors.New("backups go to an S3 bucket, and no bucket is set up; set one up on the Backups page")

// ErrNoArchiveKey refuses an encrypted destination on a server without the
// archive key.
var ErrNoArchiveKey = errors.New("backups in an S3 bucket are encrypted, and this server has no SECRET_KEY to encrypt them with")

// target resolves the destination an operation starting now uses. Backups
// at every destination but the local filesystem are encrypted: the
// dedicated bucket, and the default destination when the operational store
// is the private bucket.
func (s *Service) target(ctx context.Context) (target, error) {
	destination, err := s.destinationSetting(ctx)
	if err != nil {
		return target{}, err
	}
	d, err := s.openDestination(ctx, destination)
	if err != nil {
		return target{}, err
	}
	if !d.Encrypted() {
		return target{Destination: d}, nil
	}
	if s.opts.ArchiveKey == nil {
		return target{}, ErrNoArchiveKey
	}
	return target{Destination: d, key: s.opts.ArchiveKey}, nil
}

// openDestination opens the named destination: the dedicated bucket from
// the backup.s3.* settings, or the operational store.
func (s *Service) openDestination(ctx context.Context, destination string) (Destination, error) {
	if destination != DestinationS3 {
		if s.opts.Destination == nil {
			return nil, ErrNoDestination
		}
		return s.opts.Destination, nil
	}
	c, err := s.S3Settings(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Valid() {
		return nil, ErrS3NotConfigured
	}
	return NewS3Destination(c), nil
}

// encrypts reports whether backups written to destination are encrypted,
// without opening it.
func (s *Service) encrypts(destination string) bool {
	if destination == DestinationS3 {
		return true
	}
	return s.opts.Destination != nil && s.opts.Destination.Encrypted()
}

// keyConfirmed reports whether the admin confirmed this server's SECRET_KEY:
// backup.s3.key_acknowledged holds its check value. Only a read that
// succeeded can say it is not; a failed read is an error.
func (s *Service) keyConfirmed(ctx context.Context) (bool, error) {
	if s.opts.Settings == nil || s.opts.KeyCheck == "" {
		return false, nil
	}
	raw, err := s.readSetting(ctx, config.BackupS3KeyAcknowledgedSettingKey)
	return err == nil && raw == s.opts.KeyCheck, err
}

// ErrKeyCheckChanged refuses an acknowledgement of a check value that is not
// this server's: the key changed after the admin read it.
var ErrKeyCheckChanged = errors.New("this server's SECRET_KEY check value is not the one you confirmed; read it again")

// AcknowledgeKey records that the admin keeps a copy of the SECRET_KEY whose
// check value is check, which must be this server's. Encrypted backups (any
// S3 bucket) run only while the recorded value is the current one.
func (s *Service) AcknowledgeKey(ctx context.Context, check string) error {
	if s.opts.SettingsWriter == nil || s.opts.KeyCheck == "" {
		return errors.New("confirming the key is not available on this server")
	}
	if strings.TrimSpace(check) != s.opts.KeyCheck {
		return ErrKeyCheckChanged
	}
	return s.opts.SettingsWriter.Set(ctx, config.BackupS3KeyAcknowledgedSettingKey, s.opts.KeyCheck)
}

// KeyCheck is this server's SECRET_KEY check value.
func (s *Service) KeyCheck() string { return s.opts.KeyCheck }

// TestS3 runs a connection test of c. An empty secret key uses the stored
// one, but only for the stored endpoint and access key, so a test cannot
// send the stored secret to another endpoint.
func (s *Service) TestS3(ctx context.Context, c S3Config) (S3TestResult, error) {
	if c.SecretKey == "" {
		stored, err := s.S3Settings(ctx)
		if err != nil {
			return S3TestResult{}, err
		}
		if stored.SecretKey != "" && sameS3Account(stored, c) {
			c.SecretKey = stored.SecretKey
		}
	}
	return TestS3(ctx, c), nil
}

func sameS3Account(a, b S3Config) bool {
	return strings.TrimRight(strings.TrimSpace(a.Endpoint), "/") == strings.TrimRight(strings.TrimSpace(b.Endpoint), "/") &&
		strings.TrimSpace(a.AccessKey) == strings.TrimSpace(b.AccessKey)
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
