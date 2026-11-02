package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	bucket := `"` + config.BackupS3EndpointSettingKey + `":"https://s3.example.com","` + config.BackupS3BucketSettingKey + `":"backups"`
	put := func(h *AdminHandler, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"values":{`+body+`}}`)))
		return rec
	}
	putOne := func(h *AdminHandler, key, value string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/"+key, strings.NewReader(`{"value":"`+value+`"}`))
		h.HandleUpdateSetting(rec, withURLParam(req, "key", key))
		return rec
	}
	toS3 := `"` + config.BackupDestinationSettingKey + `":"s3",` + bucket

	// Another key's confirmation does not count.
	store := &fakeServerSettingsStore{values: map[string]string{config.BackupS3KeyAcknowledgedSettingKey: "0000-0000-0000-0000"}}
	h := &AdminHandler{SettingsRepo: store, BackupKeyCheck: check}
	if rec := put(h, toS3); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "backup_key_not_confirmed") {
		t.Fatalf("switch before confirming: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put(h, `"`+config.BackupS3KeyAcknowledgedSettingKey+`":"`+check+`"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("writing the confirmation through settings: %d %s", rec.Code, rec.Body.String())
	}
	if _, stored := store.values[config.BackupDestinationSettingKey]; stored {
		t.Fatalf("a refused write stored the destination: %v", store.values)
	}

	store.values[config.BackupS3KeyAcknowledgedSettingKey] = check
	if rec := putOne(h, config.BackupDestinationSettingKey, "s3"); rec.Code != http.StatusBadRequest {
		t.Fatalf("switch without a bucket: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put(h, toS3); rec.Code != http.StatusOK || store.values[config.BackupDestinationSettingKey] != "s3" {
		t.Fatalf("switch after confirming: %d %s", rec.Code, rec.Body.String())
	}

	// SECRET_KEY changed: the destination stays editable, and backups pause.
	changed := &AdminHandler{SettingsRepo: store, BackupKeyCheck: "9999-9999-9999-9999"}
	if rec := putOne(changed, config.BackupS3BucketSettingKey, "other"); rec.Code != http.StatusOK {
		t.Fatalf("editing the bucket after the key changed: %d %s", rec.Code, rec.Body.String())
	}
}
