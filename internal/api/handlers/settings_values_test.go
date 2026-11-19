package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newValuesTestHandler(t *testing.T) (*SettingValuesHandler, userstore.UserStore) {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(),
		userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	return NewSettingValuesHandler(testUserStoreProvider{store: store}, contract), store
}

type synchronizedShortcutStore struct {
	userstore.UserStore
	cas   userstore.SettingValueCompareAndSetter
	reads atomic.Int32
	ready chan struct{}
	once  sync.Once
	casMu sync.Mutex
}

func (s *synchronizedShortcutStore) GetSettingValue(ctx context.Context, id userstore.SettingIdentity) (*userstore.SettingValue, error) {
	value, err := s.UserStore.GetSettingValue(ctx, id)
	if err == nil && id.Key == settingskeys.NavShortcuts {
		read := s.reads.Add(1)
		if read <= 2 {
			if read == 2 {
				s.once.Do(func() { close(s.ready) })
			}
			<-s.ready
		}
	}
	return value, err
}

func (s *synchronizedShortcutStore) CompareAndSetSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
	expectedRevision int64,
) (*userstore.SettingValue, error) {
	s.casMu.Lock()
	defer s.casMu.Unlock()
	return s.cas.CompareAndSetSettingValue(ctx, id, value, expectedRevision)
}

type settingValuesLibraryLookup struct {
	existing map[int]bool
	err      error
}

func (l settingValuesLibraryLookup) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if l.err != nil {
		return nil, l.err
	}
	if !l.existing[id] {
		return nil, catalog.ErrFolderNotFound
	}
	return &models.MediaFolder{ID: id}, nil
}

// storedDeviceIDFor reports the device a profile_device row was written for, or
// "" when the key has no device-scoped row at all. The device-widening tests
// assert on stored rows rather than status codes: before the query parameter is
// honored a named device is silently ignored and the write lands on the header
// device, which is a 200 either way.
func storedDeviceIDFor(t *testing.T, store userstore.UserStore, key string) string {
	t.Helper()
	values, err := store.ListAllSettingValues(context.Background())
	if err != nil {
		t.Fatalf("listing stored values: %v", err)
	}
	for _, value := range values {
		if value.Key == key && value.Scope == settingscontract.ScopeProfileDevice {
			return value.DeviceID
		}
	}
	return ""
}

// --- Household widening: a primary profile addressing a sibling profile ---

// newHouseholdValuesHandler builds a handler with two profiles on one account:
// "profile-1" is the household parent, "profile-2" is another member.
func newHouseholdValuesHandler(t *testing.T, pin string) (*SettingValuesHandler, userstore.UserStore) {
	t.Helper()
	handler, store := newValuesTestHandler(t)

	// profile-1 is already the household parent: is_primary is assigned to the
	// first profile an account creates and is not settable afterwards.
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Robin"}); err != nil {
		t.Fatalf("create sibling: %v", err)
	}
	primary, err := store.GetProfile(ctx, "profile-1")
	if err != nil || primary == nil || !primary.IsPrimary {
		t.Fatalf("profile-1 is not the primary profile (%+v, %v)", primary, err)
	}
	if pin != "" {
		if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{
			PINHash: storedPIN(t, pin),
		}); err != nil {
			t.Fatalf("set pin: %v", err)
		}
	}

	registry := store.(userstore.DeviceRegistry)
	if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{
		ProfileID: "profile-2", DeviceID: "robin-ipad", DeviceName: "Robin's iPad",
	}); err != nil {
		t.Fatalf("registering sibling device: %v", err)
	}

	return handler, store
}

// captureAuditLogs swaps the default slog handler for the duration of a test.
// The returned function parses whatever has been emitted so far and keeps only
// the settings-audit records.
func captureAuditLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return func() []map[string]any {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if record["msg"] == settingsAuditMsg {
				records = append(records, record)
			}
		}
		return records
	}
}

type recordingLanguageSuggestionSource struct {
	filters  catalog.BrowseFilters
	original []string
	calls    int
}

func (s *recordingLanguageSuggestionSource) ListOriginalLanguages(
	_ context.Context, filters catalog.BrowseFilters,
) ([]string, error) {
	s.filters = filters
	s.calls++
	return s.original, nil
}

func TestNavigationShortcutCollectionLibraryIDValidationCannotMutateExistingTargets(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	mustSetShortcut(t, handler, `{"type":"collection","collection_id":"favorites","label":"All Favorites"}`, true)
	mustSetShortcut(t, handler, `{"type":"collection","library_id":42,"collection_id":"favorites","label":"Movie Favorites"}`, true)

	for name, libraryID := range map[string]string{
		"explicit null": `null`,
		"string":        `"42"`,
		"zero":          `0`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := setShortcut(t, handler, fmt.Sprintf(
				`{"type":"collection","library_id":%s,"collection_id":"favorites","label":"Ignored"}`, libraryID), false)
			if apiErr := requireSettingStatus(t, err, http.StatusBadRequest, "invalid collection library_id"); apiErr.Code != settingErrorInvalidValue {
				t.Fatalf("code = %q, want %q", apiErr.Code, settingErrorInvalidValue)
			}

			stored, err := store.GetSettingValue(context.Background(), navShortcutIdentity())
			if err != nil || stored == nil {
				t.Fatalf("read catalog after rejected remove: value=%+v err=%v", stored, err)
			}
			var document navigationShortcutDocument
			if err := json.Unmarshal(stored.Value, &document); err != nil {
				t.Fatalf("decode catalog after rejected remove: %v", err)
			}
			if stored.Revision != 2 || len(document.Items) != 2 {
				t.Fatalf("rejected remove mutated catalog: revision %d items %+v", stored.Revision, document.Items)
			}
			if document.Items[0].LibraryID != nil {
				t.Fatalf("global collection gained library identity: %+v", document.Items[0])
			}
			if document.Items[1].LibraryID == nil || *document.Items[1].LibraryID != 42 {
				t.Fatalf("library-specific collection changed identity: %+v", document.Items[1])
			}
		})
	}

	response, document := mustSetShortcut(t, handler, `{"type":"collection","collection_id":"favorites","label":"Ignored"}`, false)
	if response.Revision != 3 || len(document.Items) != 1 || document.Items[0].LibraryID == nil || *document.Items[0].LibraryID != 42 {
		t.Fatalf("global removal = revision %d document %+v, want scoped collection preserved", response.Revision, document)
	}
}

func TestNavigationShortcutWholeDocumentDeleteCannotCreateRevisionABA(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	if first, _ := mustSetShortcut(t, handler, `{"type":"library","library_id":42,"label":"Movies"}`, true); first.Revision != 1 {
		t.Fatalf("seed revision = %d, want 1", first.Revision)
	}
	identity := navShortcutIdentity()
	beforeDelete, err := store.GetSettingValue(context.Background(), identity)
	if err != nil || beforeDelete == nil {
		t.Fatalf("read shortcut before guarded delete: value=%+v err=%v", beforeDelete, err)
	}

	err = handler.DeleteSettingValue(seamCtx(t, "profile-1"), seamUserID,
		seamReq(settingskeys.NavShortcuts, settingscontract.ScopeProfile))
	if apiErr := requireSettingStatus(t, err, http.StatusBadRequest, "whole-document delete"); apiErr.Code != settingErrorAtomicRequired {
		t.Fatalf("code = %q, want %q", apiErr.Code, settingErrorAtomicRequired)
	}

	if second, document := mustSetShortcut(t, handler, `{"type":"library","library_id":99,"label":"Shows"}`, true); second.Revision != 2 || len(document.Items) != 2 {
		t.Fatalf("second shortcut = revision %d document %+v", second.Revision, document)
	}

	// Had the delete removed and recreated the row, its revision would return
	// to one and this stale writer could pass an ABA-shaped precondition.
	cas := store.(userstore.SettingValueCompareAndSetter)
	if _, err := cas.CompareAndSetSettingValue(context.Background(), identity,
		beforeDelete.Value, beforeDelete.Revision); !errors.Is(err, userstore.ErrSettingValueRevisionConflict) {
		t.Fatalf("stale CAS after guarded delete error = %v, want revision conflict", err)
	}
}

func TestNavigationShortcutConcurrentAddsMerge(t *testing.T) {
	baseHandler, baseStore := newValuesTestHandler(t)
	wrapped := &synchronizedShortcutStore{
		UserStore: baseStore,
		cas:       baseStore.(userstore.SettingValueCompareAndSetter),
		ready:     make(chan struct{}),
	}
	handler := NewSettingValuesHandler(testUserStoreProvider{store: wrapped}, baseHandler.contract)
	items := []string{
		`{"type":"library","library_id":42,"label":"Movies"}`,
		`{"type":"library","library_id":99,"label":"Shows"}`,
	}

	errs := make(chan error, len(items))
	for _, item := range items {
		go func() {
			_, err := handler.SetNavigationShortcut(t.Context(), seamUserID, "profile-1", json.RawMessage(item), true)
			errs <- err
		}()
	}
	for range items {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}

	got, err := baseStore.GetSettingValue(context.Background(), navShortcutIdentity())
	if err != nil || got == nil {
		t.Fatalf("read merged catalog: value=%+v err=%v", got, err)
	}
	var document navigationShortcutDocument
	if err := json.Unmarshal(got.Value, &document); err != nil {
		t.Fatalf("decode merged catalog: %v", err)
	}
	if got.Revision != 2 || len(document.Items) != 2 {
		t.Fatalf("merged catalog = revision %d items %+v, want both adds at revision 2", got.Revision, document.Items)
	}
}

// TestSettingAuthorityForOtherProfilesAndDevices: a request may name another
// profile only as the household parent, and another device only when it
// belongs to the addressed profile. Unknown ids answer 404 so the caller
// learns nothing about ids elsewhere.
func TestSettingAuthorityForOtherProfilesAndDevices(t *testing.T) {
	type call int
	const (
		set call = iota
		clearValue
		resolve
	)
	for name, tc := range map[string]struct {
		pin        string
		acting     string
		admin      bool
		profileID  string
		deviceID   string
		op         call
		wantStatus int
		wantMsg    string
		// wantRow is the (profile, device) a successful write lands on.
		wantRow [2]string
	}{
		"write to a device the profile does not own":    {acting: "profile-1", deviceID: "device-someone-else", wantStatus: http.StatusNotFound},
		"clear on a device the profile does not own":    {acting: "profile-1", deviceID: "device-someone-else", op: clearValue, wantStatus: http.StatusNotFound},
		"resolve a device the profile does not own":     {acting: "profile-1", deviceID: "not-mine", op: resolve, wantStatus: http.StatusNotFound},
		"non-primary names a sibling":                   {acting: "profile-2", profileID: "profile-1", deviceID: "device-1", wantStatus: http.StatusForbidden},
		"non-primary resolves a sibling":                {acting: "profile-2", profileID: "profile-1", op: resolve, wantStatus: http.StatusForbidden},
		"non-primary on an admin account":               {acting: "profile-2", admin: true, profileID: "profile-1", deviceID: "device-1", wantStatus: http.StatusForbidden},
		"primary with an unverified PIN":                {pin: "1234", acting: "profile-1", profileID: "profile-2", deviceID: "robin-ipad", wantStatus: http.StatusForbidden, wantMsg: "PIN"},
		"profile from another account":                  {acting: "profile-1", profileID: "someone-elses-profile", deviceID: "device-1", wantStatus: http.StatusNotFound},
		"primary names a sibling with a foreign device": {acting: "profile-1", profileID: "profile-2", deviceID: "not-robins-device", wantStatus: http.StatusNotFound},
		"primary writes a sibling's device":             {acting: "profile-1", profileID: "profile-2", deviceID: "robin-ipad", wantRow: [2]string{"profile-2", "robin-ipad"}},
		"anyone may name their own profile":             {acting: "profile-2", profileID: "profile-2", deviceID: "robin-ipad", wantRow: [2]string{"profile-2", "robin-ipad"}},
	} {
		t.Run(name, func(t *testing.T) {
			handler, store := newHouseholdValuesHandler(t, tc.pin)
			ctx := seamCtx(t, tc.acting)
			if tc.admin {
				ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: seamUserID, Role: "admin"})
			}
			const key = "player.hdr_enabled"
			req := seamReq(key, settingscontract.ScopeProfileDevice)
			req.ActiveProfileID, req.ProfileID, req.DeviceID = tc.acting, tc.profileID, tc.deviceID

			var err error
			switch tc.op {
			case set:
				_, err = handler.SetSettingValue(ctx, seamUserID, req, json.RawMessage(`false`))
			case clearValue:
				err = handler.DeleteSettingValue(ctx, seamUserID, req)
			case resolve:
				q := seamQuery("playback.subtitle_mode")
				q.ActiveProfileID, q.ProfileID, q.DeviceID = tc.acting, tc.profileID, tc.deviceID
				_, err = handler.ResolveEffectiveSettings(ctx, seamUserID, q)
			}

			if tc.wantStatus != 0 {
				apiErr := requireSettingStatus(t, err, tc.wantStatus, "refused call")
				if !strings.Contains(apiErr.Message, tc.wantMsg) {
					t.Errorf("message %q does not mention %q", apiErr.Message, tc.wantMsg)
				}
				if got := storedDeviceIDFor(t, store, key); got != "" {
					t.Errorf("refused call wrote a row on device %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			values, err := store.ListAllSettingValues(context.Background())
			if err != nil || len(values) != 1 || values[0].ProfileID != tc.wantRow[0] || values[0].DeviceID != tc.wantRow[1] {
				t.Fatalf("stored rows = %+v (%v), want one on %v", values, err, tc.wantRow)
			}
		})
	}
}

// TestSettingWritesRegisterOnlyTheCallersOwnDevice: registration means "this
// device is in use by this profile". A write aimed at another device, or made
// on another profile's behalf, must not file the actor's device anywhere.
func TestSettingWritesRegisterOnlyTheCallersOwnDevice(t *testing.T) {
	for name, tc := range map[string]struct {
		profileID, deviceID string
		checkProfile        string
	}{
		"another device of the same profile": {deviceID: "apple-tv", checkProfile: "profile-1"},
		"a sibling profile's device":         {profileID: "profile-2", deviceID: "robin-ipad", checkProfile: "profile-2"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, store := newHouseholdValuesHandler(t, "")
			registry := store.(userstore.DeviceRegistry)
			if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
				ProfileID: "profile-1", DeviceID: "apple-tv", DeviceName: "Apple TV",
			}); err != nil {
				t.Fatalf("registering apple-tv: %v", err)
			}
			req := seamReq("player.hdr_enabled", settingscontract.ScopeProfileDevice)
			req.ProfileID, req.DeviceID = tc.profileID, tc.deviceID
			seamSet(t, seamCtx(t, "profile-1"), handler, req, `false`)

			exists, err := registry.DeviceExists(context.Background(), tc.checkProfile, "device-1")
			if err != nil {
				t.Fatalf("DeviceExists: %v", err)
			}
			if exists {
				t.Errorf("registered the acting device-1 under %s", tc.checkProfile)
			}
		})
	}
}

func TestSetValue_AuditsCrossProfileWrite(t *testing.T) {
	handler, _ := newHouseholdValuesHandler(t, "")
	audited := captureAuditLogs(t)

	req := seamReq("playback.subtitle_mode", settingscontract.ScopeProfileDevice)
	req.ProfileID, req.DeviceID = "profile-2", "robin-ipad"
	seamSet(t, seamCtx(t, "profile-1"), handler, req, `"always"`)

	records := audited()
	if len(records) != 1 {
		t.Fatalf("emitted %d audit records, want 1: %+v", len(records), records)
	}
	record := records[0]
	if record["actor_profile_id"] != "profile-1" || record["target_profile_id"] != "profile-2" {
		t.Errorf("actor/target = %v/%v, want profile-1/profile-2",
			record["actor_profile_id"], record["target_profile_id"])
	}
	if record["setting_key"] != "playback.subtitle_mode" || record["device_id"] != "robin-ipad" {
		t.Errorf("key/device = %v/%v", record["setting_key"], record["device_id"])
	}
	// Identity only: the value must never reach an operator's log.
	for key, value := range record {
		if text, ok := value.(string); ok && text == "always" {
			t.Errorf("audit record leaked the value under %q", key)
		}
	}
}
