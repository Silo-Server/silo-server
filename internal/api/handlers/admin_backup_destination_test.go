package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// TestBackupDestinationS3NeedsTheConfirmedKey: backups move to S3 only after
// the admin confirmed this server's SECRET_KEY check value, through the batch
// and the single-key writes alike. The confirmation itself is not a setting
// the settings API writes, and a changed key pauses S3 backups instead of
// blocking other backup settings.
func TestBackupDestinationS3NeedsTheConfirmedKey(t *testing.T) {
	const check = "1a2b-3c4d-5e6f-7a8b"
	withBucket := func(values map[string]string) map[string]string {
		values[config.BackupS3EndpointSettingKey] = "https://s3.example.com"
		values[config.BackupS3BucketSettingKey] = "backups"
		return values
	}
	// outcome is the status and error code a write answers.
	outcome := func(err error) (int, string) {
		if err == nil {
			return http.StatusOK, ""
		}
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			return apiErr.Status, apiErr.Code
		}
		return http.StatusInternalServerError, err.Error()
	}
	put := func(h *AdminHandler, values map[string]string) (int, string) {
		_, err := h.UpdateAdminSettings(t.Context(), values, nil)
		return outcome(err)
	}
	putOne := func(h *AdminHandler, key, value string) (int, string) {
		_, err := h.UpdateAdminSetting(t.Context(), key, value, nil)
		return outcome(err)
	}
	toS3 := func() map[string]string {
		return withBucket(map[string]string{config.BackupDestinationSettingKey: "s3"})
	}

	// Another key's confirmation does not count.
	store := &fakeServerSettingsStore{values: map[string]string{config.BackupS3KeyAcknowledgedSettingKey: "0000-0000-0000-0000"}}
	h := &AdminHandler{SettingsRepo: store, BackupKeyCheck: check}
	if status, code := put(h, toS3()); status != http.StatusConflict || code != "backup_key_not_confirmed" {
		t.Fatalf("switch before confirming: %d %s", status, code)
	}
	if status, code := put(h, map[string]string{config.BackupS3KeyAcknowledgedSettingKey: check}); status != http.StatusBadRequest {
		t.Fatalf("writing the confirmation through settings: %d %s", status, code)
	}
	if _, stored := store.values[config.BackupDestinationSettingKey]; stored {
		t.Fatalf("a refused write stored the destination: %v", store.values)
	}

	store.values[config.BackupS3KeyAcknowledgedSettingKey] = check
	if status, code := putOne(h, config.BackupDestinationSettingKey, "s3"); status != http.StatusBadRequest {
		t.Fatalf("switch without a bucket: %d %s", status, code)
	}
	if status, code := put(h, toS3()); status != http.StatusOK || store.values[config.BackupDestinationSettingKey] != "s3" {
		t.Fatalf("switch after confirming: %d %s", status, code)
	}

	// SECRET_KEY changed: the destination stays editable, and backups pause.
	changed := &AdminHandler{SettingsRepo: store, BackupKeyCheck: "9999-9999-9999-9999"}
	if status, code := putOne(changed, config.BackupS3BucketSettingKey, "other"); status != http.StatusOK {
		t.Fatalf("editing the bucket after the key changed: %d %s", status, code)
	}
}
