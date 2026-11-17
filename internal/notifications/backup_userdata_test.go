package notifications

import (
	"encoding/json"
	"testing"
)

// A backup written before Next Up was the only reason carries the favorite,
// watchlist and continue watching switches. The strict decoder must still
// accept it, or such a backup could no longer be restored.
func TestNotificationsBackupAcceptsRetiredPreferences(t *testing.T) {
	older := json.RawMessage(`{"preferences":{"enabled":true,"notify_favorites":false,"notify_watchlist":true,` +
		`"notify_continue_watching":false,"notify_next_up":true},"muted_series":[]}`)
	sum, err := BackupData{}.Validate(older)
	if err != nil || sum.Count != 1 {
		t.Fatalf("Validate = %+v, %v; want the preferences counted", sum, err)
	}
	doc, _, err := decodeNotifications(older)
	if err != nil || doc.Preferences == nil || !doc.Preferences.Enabled || !doc.Preferences.NextUp {
		t.Fatalf("decoded = %+v, %v", doc.Preferences, err)
	}
	if _, err := (BackupData{}).Validate(json.RawMessage(`{"preferences":{"notify_unknown":true},"muted_series":[]}`)); err == nil {
		t.Fatal("an unknown preference was accepted")
	}
}
