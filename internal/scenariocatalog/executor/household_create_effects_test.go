package executor

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func householdCreates(id string) bool {
	switch id {
	case "profiles_create.ok", "profiles_create.meaning", "profiles_create.admin_any_profile", "profiles_create.limit", "profiles_create.shape":
		return true
	}
	return false
}

// Compare full rows with explicit expected changes, including the post-create PIN
// policy revision and the canonical settings written by the creation transaction.
func assertHouseholdCreation(t *testing.T, e *Env, id string, resp response, sequence int64, appStart, appEnd, dbStart, dbEnd time.Time, before, after map[string]json.RawMessage) {
	t.Helper()
	decode := func(raw json.RawMessage) []map[string]any {
		t.Helper()
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	bounded := func(value any, lo, hi time.Time) {
		t.Helper()
		text, ok := value.(string)
		if !ok {
			t.Fatalf("timestamp type %T", value)
		}
		stamp, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || stamp.Before(lo) || stamp.After(hi) {
			t.Errorf("timestamp outside creation bounds: %v (%v)", value, err)
		}
	}
	equal := func(want, got map[string]any) {
		t.Helper()
		if !reflect.DeepEqual(want, got) {
			for key, value := range want {
				if !reflect.DeepEqual(value, got[key]) {
					t.Errorf("unexpected creation field: %s", key)
				}
			}
			if len(want) != len(got) {
				t.Error("creation column count changed")
			}
		}
	}
	oldProfiles, newProfiles := decode(before["user_profiles"]), decode(after["user_profiles"])
	if len(newProfiles) != len(oldProfiles)+1 {
		t.Fatal("expected exactly one new profile")
	}
	known := map[any]map[string]any{}
	var template map[string]any
	for _, row := range oldProfiles {
		known[row["id"]] = row
		if row["id"] == profileSecondary {
			template = row
		}
	}
	var added map[string]any
	count := 0
	for _, row := range newProfiles {
		if old, ok := known[row["id"]]; ok {
			equal(old, row)
			delete(known, row["id"])
		} else {
			added = row
			count++
		}
	}
	if count != 1 || len(known) != 0 || template == nil {
		t.Fatal("unexpected profile addition/deletion")
	}
	profileID, ok := added["id"].(string)
	if !ok {
		t.Fatal("profile id type")
	}
	parsed, err := uuid.Parse(profileID)
	if err != nil || parsed.Version() != 4 {
		t.Fatal("new profile is not UUIDv4")
	}
	owner := e.users[fixtureMember].ID
	if id == "profiles_create.admin_any_profile" {
		owner = e.users[fixtureAdmin].ID
	}
	want := maps.Clone(template)
	want["id"] = profileID
	want["user_id"] = float64(owner)
	want["name"] = "Fixture New"
	for _, key := range []string{"created_at", "updated_at"} {
		bounded(added[key], appStart, appEnd)
		want[key] = added[key]
	}
	if id == "profiles_create.meaning" {
		want["avatar"] = "preset:avatar-2"
		hash, ok := added["pin_hash"].(string)
		if !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte("1357")) != nil {
			t.Error("new PIN hash does not verify")
		}
		for _, row := range oldProfiles {
			if row["pin_hash"] == hash {
				t.Error("new PIN hash reused")
			}
		}
		want["pin_hash"] = hash
	}
	equal(want, added)
	if resp.Status != http.StatusCreated {
		t.Error("creation response status")
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != profileID {
		t.Error("response profile id differs from inserted row")
	}
	// Legacy account settings are empty in this fixture; no inherited settings are expected.
	if len(decode(before["user_settings"])) != 0 || len(decode(before["user_setting_values"])) != 0 {
		t.Fatal("unexpected inherited canonical fixture")
	}
	settings := decode(after["user_setting_values"])
	expected := map[string]any{"playback.auto_skip_intro": false, "playback.intro_skip_mode": "ask", "playback.auto_skip_credits": false, "playback.auto_skip_recap": false, "playback.auto_play_next_preview": false}
	if len(settings) != len(expected) {
		t.Fatal("expected exactly five canonical creation settings")
	}
	seenIDs := map[float64]bool{}
	for _, row := range settings {
		key, ok := row["key"].(string)
		if !ok {
			t.Fatal("setting key type")
		}
		value, ok := expected[key]
		if !ok {
			t.Fatal("unexpected canonical key")
		}
		delete(expected, key)
		settingID, ok := row["id"].(float64)
		if !ok || settingID <= float64(sequence) || settingID > float64(sequence+5) || seenIDs[settingID] {
			t.Error("unexpected canonical identity allocation")
		}
		seenIDs[settingID] = true
		bounded(row["created_at"], dbStart, dbEnd)
		bounded(row["updated_at"], dbStart, dbEnd)
		if row["created_at"] != row["updated_at"] {
			t.Error("canonical creation timestamps differ")
		}
		equal(map[string]any{"id": settingID, "user_id": float64(owner), "profile_id": profileID, "key": key, "value": value, "scope": "profile", "client_family": nil, "device_id": nil, "library_id": nil, "series_id": nil, "revision": float64(1), "created_at": row["created_at"], "updated_at": row["updated_at"]}, row)
	}
	if id == "profiles_create.meaning" {
		oldUsers, newUsers := decode(before["users"]), decode(after["users"])
		if len(oldUsers) != len(newUsers) {
			t.Fatal("account count changed")
		}
		indexed := map[any]map[string]any{}
		for _, row := range newUsers {
			indexed[row["id"]] = row
		}
		matched := 0
		for _, row := range oldUsers {
			want := maps.Clone(row)
			if row["id"] == float64(owner) {
				matched++
				for _, key := range []string{"access_policy_revision", "admin_revision"} {
					value, ok := row[key].(float64)
					if !ok {
						t.Fatal("revision type")
					}
					want[key] = value + 1
				}
			}
			equal(want, indexed[row["id"]])
		}
		if matched != 1 {
			t.Fatal("expected one PIN owner revision update")
		}
	}
}
