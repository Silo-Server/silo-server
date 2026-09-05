package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The pilot fakes stand in for the v1 handlers' extracted business logic:
// in-memory answers with the same shapes and *handlers.APIError failures.

type fakeAccounts struct {
	needsSetup bool
	users      map[int]handlers.UserView
	err        error
}

func (f fakeAccounts) NeedsSetup(context.Context) (bool, error) { return f.needsSetup, f.err }

func (f fakeAccounts) CurrentUser(_ context.Context, claims *auth.Claims) (handlers.UserView, error) {
	if f.err != nil {
		return handlers.UserView{}, f.err
	}
	view, ok := f.users[claims.UserID]
	if !ok {
		return handlers.UserView{}, &handlers.APIError{Status: 500, Code: TypeInternalError.ID, Message: "An unexpected error occurred"}
	}
	return view, nil
}

// fakeProgressQuery is one ListProgressPage call as the handler made it.
type fakeProgressQuery struct {
	Status    string
	LibraryID int
	After     *userstore.ProgressKey
	Limit     int
}

// fakeProgress stands in for handlers.ProgressHandler.ListProgressPage: a
// keyset store over entries plus the library filter (libraries maps
// media_item_id to its library; a nil map leaves the filter a no-op, a
// non-nil one puts unknown ids in no library), applied the way the real seam
// does — fetch, filter, re-fetch until limit+1 matches.
type fakeProgress struct {
	entries   []userstore.WatchProgress
	libraries map[string]int
	// calls records each query so a test can assert the window and filter
	// the handler asked for.
	calls []fakeProgressQuery
	err   error
}

func (f *fakeProgress) ListProgressPage(_ context.Context, _ int, profileID string, status string, libraryID int, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, bool, error) {
	f.calls = append(f.calls, fakeProgressQuery{Status: status, LibraryID: libraryID, After: after, Limit: limit})
	if f.err != nil {
		return nil, false, f.err
	}
	want := limit + 1
	var matches []userstore.WatchProgress
	for len(matches) < want {
		batch := f.page(profileID, status, after, want)
		for _, e := range batch {
			if libraryID == 0 || f.libraries == nil || f.libraries[e.MediaItemID] == libraryID {
				matches = append(matches, e)
			}
		}
		if len(batch) < want {
			break
		}
		last := batch[len(batch)-1]
		after = &userstore.ProgressKey{UpdatedAt: last.UpdatedAt, MediaItemID: last.MediaItemID}
	}
	if len(matches) > limit {
		return matches[:limit], true, nil
	}
	return matches, false, nil
}

// keyBefore reports whether e sorts strictly after the key in
// (UpdatedAt desc, MediaItemID desc) order, i.e. its own key is smaller.
func keyBefore(e userstore.WatchProgress, key userstore.ProgressKey) bool {
	if e.UpdatedAt != key.UpdatedAt {
		return e.UpdatedAt < key.UpdatedAt
	}
	return e.MediaItemID < key.MediaItemID
}

// page is the store: the status set sorted by (UpdatedAt desc, MediaItemID
// desc), strictly after the key, at most limit rows.
func (f *fakeProgress) page(profileID, status string, after *userstore.ProgressKey, limit int) []userstore.WatchProgress {
	var out []userstore.WatchProgress
	for _, e := range f.entries {
		if e.ProfileID != profileID {
			continue
		}
		switch status {
		case "completed":
			if !e.Completed {
				continue
			}
		case "in_progress":
			if e.Completed {
				continue
			}
		}
		if after != nil && !keyBefore(e, *after) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].MediaItemID > out[j].MediaItemID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

type fakeProfiles struct {
	last *handlers.ProfileUpdateCommand
	view handlers.ProfileView
	err  error
	// lockedPrimary, when set, is the PIN-locked primary profile the fake
	// treats as managing the household: like v1 canManageHouseholdAs it runs
	// the command's verifier for it and answers an unverified one with the
	// 403 profile_management the real service returns.
	lockedPrimary string
}

func (f *fakeProfiles) UpdateProfile(_ context.Context, cmd handlers.ProfileUpdateCommand) (handlers.ProfileView, error) {
	f.last = &cmd
	if f.err != nil {
		return handlers.ProfileView{}, f.err
	}
	if f.lockedPrimary != "" && cmd.ActiveProfileID == f.lockedPrimary {
		if err := cmd.VerifyProfile(f.lockedPrimary); err != nil {
			return handlers.ProfileView{}, &handlers.APIError{Status: http.StatusForbidden, Code: codeProfileManagement, Message: "Profile management requires verifying the primary profile PIN"}
		}
	}
	return f.view, nil
}

// fakeLibraries knows the libraries whose ids are in known.
type fakeLibraries struct {
	known []int
	err   error
}

func (f fakeLibraries) ExistingIDs(_ context.Context, ids []int) ([]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []int
	for _, id := range ids {
		if slices.Contains(f.known, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

type fakeAdminUsers struct {
	users []handlers.AdminUserView
	err   error
}

// ListAdminUsersPage mirrors the handler seam: users are kept in id order,
// the page starts strictly after afterID, and has_more is decided by the
// limit+1 probe.
func (f fakeAdminUsers) ListAdminUsersPage(_ context.Context, afterID, limit int) ([]handlers.AdminUserView, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	page := make([]handlers.AdminUserView, 0, limit)
	for _, u := range f.users {
		if u.ID <= afterID {
			continue
		}
		if len(page) == limit {
			return page, true, nil
		}
		page = append(page, u)
	}
	return page, false, nil
}

var errStore = errors.New("store unavailable")

func fixedTime() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC) }

func fixtureProfileView() handlers.ProfileView {
	return handlers.ProfileView{
		ID: "p-owner", Name: "Laura", Avatar: "preset:fox", AvatarURL: "/avatars/presets/fox.png", AvatarSource: "preset",
		IsPrimary: true, QualityPreference: "auto", Language: "en", SubtitleMode: "auto",
		AutoSkipIntro: true, AllowedLibraryIDs: []int{3}, MaxPlaybackQuality: "1080p",
		CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-01-02T03:04:05Z",
	}
}

// pilotDeps is parityDeps with every pilot service wired to a fake.
func pilotDeps(progress *fakeProgress, profiles *fakeProfiles) Dependencies {
	deps := parityDeps(false)
	deps.Accounts = fakeAccounts{
		users: map[int]handlers.UserView{
			1: {ID: 1, Username: "laura", Email: "laura@example.test", Role: "user", Permissions: []string{"marker_edit"}, DownloadAllowed: true},
			2: {ID: 2, Username: "ada", Email: "ada@example.test", Role: "admin", Permissions: []string{"marker_edit", "metadata_curation"}},
		},
	}
	if progress == nil {
		progress = &fakeProgress{}
	}
	deps.Progress = progress
	if profiles == nil {
		profiles = &fakeProfiles{view: fixtureProfileView()}
	}
	deps.Profiles = profiles
	deps.Libraries = fakeLibraries{known: []int{1, 2, 3, 4}}
	two, yes := 2, true
	groupID := int64(2)
	last := fixedTime()
	deps.AdminUsers = fakeAdminUsers{users: []handlers.AdminUserView{
		{ID: 1, Username: "laura", Email: "laura@example.test", Role: "user", Permissions: []string{}, Enabled: true,
			MaxStreams: &two, DownloadAllowed: &yes, MaxProfiles: 5, AccessGroupID: &groupID,
			EffectivePolicy: handlers.EffectivePolicyView{LibraryIDs: []int{3}, MaxPlaybackQuality: "1080p", MaxStreams: 2, TranscodeAllowed: true, Permissions: []string{}},
			CreatedAt:       fixedTime(), UpdatedAt: fixedTime(), LastActiveAt: &last},
		{ID: 2, Username: "ada", Role: "admin", Permissions: []string{"marker_edit"}, Enabled: true, LibraryIDs: []int{}, MaxProfiles: 5,
			EffectivePolicy: handlers.EffectivePolicyView{Permissions: []string{"marker_edit", "metadata_curation"}},
			CreatedAt:       fixedTime(), UpdatedAt: fixedTime()},
	}}
	deps.SettingsContract, deps.Settings, deps.PluginSettings, deps.SettingValues = settingsFakes()
	return deps
}

// The settings fakes stand in for the extracted settings seams.

type fakeSettingsContract struct {
	view handlers.SettingsCapabilitiesView
	err  error
}

func (f fakeSettingsContract) Capabilities(context.Context) (handlers.SettingsCapabilitiesView, error) {
	return f.view, f.err
}

// fakeSettingsSeam keeps device overrides in memory keyed by profile and
// device, the way the store does, and answers the resolution the real seam
// performs: the device override when present, else the profile-wide value.
type fakeSettingsSeam struct {
	overlay   handlers.OverlayConfigView
	global    map[string]string
	overrides map[string]handlers.EffectiveSubtitleAppearanceView
	err       error
}

func overrideKey(profileID, deviceID string) string { return profileID + "\x00" + deviceID }

func (f *fakeSettingsSeam) OverlayConfig(context.Context) handlers.OverlayConfigView {
	return f.overlay
}

func (f *fakeSettingsSeam) EffectiveSubtitleAppearance(_ context.Context, _ int, profileID string, device handlers.DeviceMetadata) (handlers.EffectiveSubtitleAppearanceView, error) {
	if f.err != nil {
		return handlers.EffectiveSubtitleAppearanceView{}, f.err
	}
	view := handlers.EffectiveSubtitleAppearanceView{Key: handlers.SubtitleAppearanceSettingKey, ProfileID: profileID, GlobalValue: f.global[profileID], EffectiveValue: f.global[profileID]}
	if o, ok := f.overrides[overrideKey(profileID, device.DeviceID)]; ok {
		view.DeviceValue, view.EffectiveValue, view.HasDeviceOverride = o.DeviceValue, o.DeviceValue, true
		view.DeviceID, view.DeviceName, view.DevicePlatform, view.UpdatedAt = o.DeviceID, o.DeviceName, o.DevicePlatform, o.UpdatedAt
	}
	return view, nil
}

func (f *fakeSettingsSeam) SetDeviceSetting(_ context.Context, cmd handlers.DeviceSettingCommand, value string) error {
	if f.err != nil {
		return f.err
	}
	if cmd.Device.DeviceID == "" {
		return &handlers.APIError{Status: 400, Code: "bad_request", Message: "Device id is required", Field: "device_id"}
	}
	if !json.Valid([]byte(value)) {
		return &handlers.APIError{Status: 400, Code: "bad_request", Message: "subtitle_appearance must be valid JSON", Field: "value"}
	}
	if f.overrides == nil {
		f.overrides = map[string]handlers.EffectiveSubtitleAppearanceView{}
	}
	f.overrides[overrideKey(cmd.ProfileID, cmd.Device.DeviceID)] = handlers.EffectiveSubtitleAppearanceView{
		DeviceValue: value, DeviceID: cmd.Device.DeviceID, DeviceName: cmd.Device.DeviceName, DevicePlatform: cmd.Device.DevicePlatform,
		UpdatedAt: "2026-01-02 03:04:05.678+00",
	}
	return nil
}

func (f *fakeSettingsSeam) DeleteDeviceSetting(_ context.Context, cmd handlers.DeviceSettingCommand) error {
	if f.err != nil {
		return f.err
	}
	delete(f.overrides, overrideKey(cmd.ProfileID, cmd.Device.DeviceID))
	return nil
}

type fakePluginSettingsSeam struct {
	installations []handlers.PluginUserSettingsView
	values        map[int]map[string]string
	err           error
}

func (f *fakePluginSettingsSeam) find(id int) (handlers.PluginUserSettingsView, error) {
	for _, v := range f.installations {
		if v.ID == id {
			return v, nil
		}
	}
	return handlers.PluginUserSettingsView{}, &handlers.APIError{Status: 404, Code: "not_found", Message: "Plugin installation not found"}
}

func (f *fakePluginSettingsSeam) ListUserPluginSettings(context.Context) ([]handlers.PluginUserSettingsView, error) {
	return f.installations, f.err
}

func (f *fakePluginSettingsSeam) GetUserPluginSettings(_ context.Context, _ int, id int) (handlers.PluginUserSettingsDetailView, error) {
	if f.err != nil {
		return handlers.PluginUserSettingsDetailView{}, f.err
	}
	v, err := f.find(id)
	if err != nil {
		return handlers.PluginUserSettingsDetailView{}, err
	}
	return handlers.PluginUserSettingsDetailView{Installation: v, Values: f.values[id]}, nil
}

func (f *fakePluginSettingsSeam) SetUserPluginSettings(_ context.Context, _ int, id int, values map[string]string) error {
	if f.err != nil {
		return f.err
	}
	if _, err := f.find(id); err != nil {
		return err
	}
	if f.values == nil {
		f.values = map[int]map[string]string{}
	}
	f.values[id] = values
	return nil
}

func settingsFakes() (fakeSettingsContract, *fakeSettingsSeam, *fakePluginSettingsSeam, *fakeSettingValuesSeam) {
	contract := fakeSettingsContract{view: handlers.SettingsCapabilitiesView{
		APIVersion: 1, Revision: 12, ContractETag: `"etag-12"`, DefinitionCount: 40,
		Scopes: []string{"account", "profile"}, ClientFamilies: []string{"tv", "web"},
		SupportsBatchedEffective: true, SupportsIdempotentWrites: true, SupportsAtomicShortcuts: true,
	}}
	settings := &fakeSettingsSeam{
		overlay: handlers.OverlayConfigView{Enabled: true, QuickActionsDefault: "both"},
		global:  map[string]string{"p-owner": `{"fontSize":"large"}`},
	}
	plugins := &fakePluginSettingsSeam{
		installations: []handlers.PluginUserSettingsView{{
			ID: 3, PluginID: "org.example.subtitles", Version: "1.2.0",
			UserConfigSchema: []handlers.PluginConfigSchemaView{{Key: "region", Title: "Region", JSONSchema: `{"type":"string"}`}},
			Routes:           []handlers.PluginRouteView{{ID: "dashboard", Method: "GET", Path: "/dashboard", Access: "user", Navigable: true, NavigationLabel: "Dashboard", NavigationKind: "user"}},
			Assets:           []handlers.PluginAssetView{},
			Category:         "Tools",
		}},
		values: map[int]map[string]string{3: {"region": "us"}},
	}
	return contract, settings, plugins, newFakeSettingValuesSeam()
}

// fakeSettingValuesSeam is an in-memory stand-in for the settings values
// core. It keeps the seam's error contract (an *handlers.APIError whose
// Field names the request member) for the decisions the tests exercise:
// unknown keys, missing scope or profile, the nav.shortcuts atomic rule, a
// value the schema refuses, and reads of unset rows.
type fakeSettingValuesSeam struct {
	known    map[string]json.RawMessage // key -> contract default
	values   map[string]handlers.SettingValueView
	revision int64
	err      error
}

func newFakeSettingValuesSeam() *fakeSettingValuesSeam {
	return &fakeSettingValuesSeam{
		known: map[string]json.RawMessage{
			"ui.theme":                   json.RawMessage(`"midnight-cinema"`),
			"playback.preferred_quality": json.RawMessage(`"auto"`),
			"nav.shortcuts":              json.RawMessage(`{"items":[]}`),
		},
		values: map[string]handlers.SettingValueView{},
	}
}

func (f *fakeSettingValuesSeam) ContractRevision() int { return 8 }

func settingAPIError(status int, code, field, msg string) *handlers.APIError {
	return &handlers.APIError{Status: status, Code: code, Message: msg, Field: field}
}

func (f *fakeSettingValuesSeam) identity(req handlers.SettingIdentityRequest) (handlers.SettingValueView, *handlers.APIError) {
	if req.Key == "" {
		return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "key", "A setting key is required")
	}
	if _, ok := f.known[req.Key]; !ok {
		return handlers.SettingValueView{}, settingAPIError(404, "unknown_setting", "key", "No setting named "+req.Key+" exists in this server's contract")
	}
	if req.Scope == "" {
		return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "scope", "A scope is required")
	}
	v := handlers.SettingValueView{Key: req.Key, Scope: req.Scope}
	if req.Scope != "account" {
		v.ProfileID = req.ActiveProfileID
		if v.ProfileID == "" {
			return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "profile_header", "X-Profile-Id header is required for this scope")
		}
		if req.ProfileID != "" && req.ProfileID != v.ProfileID {
			if req.ProfileID != "p-other" {
				return handlers.SettingValueView{}, settingAPIError(404, "not_found", "profile_id", "Profile not found")
			}
			if err := req.VerifyProfile(v.ProfileID); err != nil {
				return handlers.SettingValueView{}, settingAPIError(403, "forbidden", "", "Profile verification required")
			}
			v.ProfileID = req.ProfileID
		}
	}
	if req.Scope == "profile_device" {
		v.DeviceID = req.DeviceID
		if v.DeviceID == "" {
			v.DeviceID = req.Device.DeviceID
		}
		if v.DeviceID == "" {
			return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "device_header", "X-Silo-Device-Id header is required for a device override")
		}
	}
	if req.Scope == "profile_client" {
		if req.ClientFamily == "" {
			return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "client_family", "X-Silo-Client-Family header must be one of tv, mobile, tablet, desktop or web")
		}
		v.ClientFamily = req.ClientFamily
	}
	if req.Scope == "profile_library" {
		if req.LibraryID != "3" {
			return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "library_id", "library_id must be a positive integer")
		}
		v.LibraryID = 3
	}
	return v, nil
}

func settingRowKey(v handlers.SettingValueView) string {
	return v.Key + "|" + v.Scope + "|" + v.ProfileID + "|" + v.ClientFamily + "|" + v.DeviceID + "|" + fmt.Sprint(v.LibraryID) + "|" + v.SeriesID
}

func (f *fakeSettingValuesSeam) GetSettingValue(_ context.Context, _ int, req handlers.SettingIdentityRequest) (handlers.SettingValueView, error) {
	if f.err != nil {
		return handlers.SettingValueView{}, f.err
	}
	id, apiErr := f.identity(req)
	if apiErr != nil {
		return handlers.SettingValueView{}, apiErr
	}
	stored, ok := f.values[settingRowKey(id)]
	if !ok {
		return handlers.SettingValueView{}, &handlers.APIError{Status: 404, Code: "not_found", Message: "No value is set at this scope"}
	}
	return stored, nil
}

func (f *fakeSettingValuesSeam) ListSettingValues(_ context.Context, _ int, keys []string, req handlers.SettingIdentityRequest) ([]handlers.ExplicitSettingValueView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(keys) == 0 {
		return nil, settingAPIError(400, "bad_request", "keys", "Query parameter keys is required")
	}
	req.Key = keys[0]
	id, apiErr := f.identity(req)
	if apiErr != nil {
		if apiErr.Field == "key" {
			apiErr.Field = "keys"
		}
		return nil, apiErr
	}
	out := make([]handlers.ExplicitSettingValueView, 0, len(keys))
	for _, key := range keys {
		if _, ok := f.known[key]; !ok {
			return nil, settingAPIError(404, "unknown_setting", "keys", "No setting named "+key+" exists in this server's contract")
		}
		row := id
		row.Key = key
		e := handlers.ExplicitSettingValueView{Key: key, Scope: row.Scope, ProfileID: row.ProfileID, ClientFamily: row.ClientFamily, DeviceID: row.DeviceID, LibraryID: row.LibraryID}
		if stored, ok := f.values[settingRowKey(row)]; ok {
			e.IsSet, e.Value, e.Revision, e.UpdatedAt = true, stored.Value, stored.Revision, stored.UpdatedAt
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeSettingValuesSeam) SetSettingValue(_ context.Context, _ int, req handlers.SettingIdentityRequest, value json.RawMessage) (handlers.SettingValueView, error) {
	if f.err != nil {
		return handlers.SettingValueView{}, f.err
	}
	id, apiErr := f.identity(req)
	if apiErr != nil {
		return handlers.SettingValueView{}, apiErr
	}
	if id.Key == "nav.shortcuts" {
		return handlers.SettingValueView{}, settingAPIError(400, "atomic_update_required", "key", "nav.shortcuts is updated through the item route")
	}
	if len(value) == 0 {
		return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "value", "value is required")
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil || s == "" {
		return handlers.SettingValueView{}, settingAPIError(400, "invalid_value", "value", "value must be a non-empty string")
	}
	f.revision++
	id.Value, id.Revision, id.UpdatedAt = value, f.revision, fixedTime().Format(time.RFC3339Nano)
	f.values[settingRowKey(id)] = id
	return id, nil
}

func (f *fakeSettingValuesSeam) DeleteSettingValue(_ context.Context, _ int, req handlers.SettingIdentityRequest) error {
	if f.err != nil {
		return f.err
	}
	id, apiErr := f.identity(req)
	if apiErr != nil {
		return apiErr
	}
	if id.Key == "nav.shortcuts" {
		return settingAPIError(400, "atomic_update_required", "key", "nav.shortcuts is updated through the item route")
	}
	if _, ok := f.values[settingRowKey(id)]; !ok {
		return &handlers.APIError{Status: 404, Code: "not_found", Message: "No value is set at this scope"}
	}
	delete(f.values, settingRowKey(id))
	return nil
}

func (f *fakeSettingValuesSeam) SetNavigationShortcut(_ context.Context, _ int, profileID string, item json.RawMessage, present bool) (handlers.SettingValueView, error) {
	if f.err != nil {
		return handlers.SettingValueView{}, f.err
	}
	if profileID == "" {
		return handlers.SettingValueView{}, settingAPIError(400, "bad_request", "profile_header", "X-Profile-Id header is required")
	}
	var parsed struct {
		Type  string `json:"type"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal(item, &parsed); err != nil || parsed.Type == "" || parsed.Label == "" {
		return handlers.SettingValueView{}, settingAPIError(400, "invalid_value", "item", "shortcut requires type and label")
	}
	row := handlers.SettingValueView{Key: "nav.shortcuts", Scope: "profile", ProfileID: profileID}
	items := []json.RawMessage{}
	if present {
		items = append(items, item)
	}
	doc, _ := json.Marshal(map[string]any{"items": items})
	f.revision++
	row.Value, row.Revision, row.UpdatedAt = doc, f.revision, fixedTime().Format(time.RFC3339Nano)
	f.values[settingRowKey(row)] = row
	return row, nil
}

func (f *fakeSettingValuesSeam) effective(q handlers.EffectiveSettingsQuery, keys []string, field string) ([]handlers.EffectiveSettingValueView, *handlers.APIError) {
	if len(keys) == 0 {
		keys = []string{"ui.theme", "playback.preferred_quality", "nav.shortcuts"}
	}
	profileID := q.ActiveProfileID
	if q.ProfileID != "" && q.ProfileID != profileID {
		if q.ProfileID != "p-other" {
			return nil, settingAPIError(404, "not_found", "profile_id", "Profile not found")
		}
		if err := q.VerifyProfile(profileID); err != nil {
			return nil, settingAPIError(403, "forbidden", "", "Profile verification required")
		}
		profileID = q.ProfileID
	}
	out := make([]handlers.EffectiveSettingValueView, 0, len(keys))
	for _, key := range keys {
		def, ok := f.known[key]
		if !ok {
			return nil, settingAPIError(404, "unknown_setting", field, "No setting named "+key+" exists in this server's contract")
		}
		e := handlers.EffectiveSettingValueView{Key: key, Value: def, Source: "default", DefinitionRevision: 3}
		row := handlers.SettingValueView{Key: key, Scope: "profile", ProfileID: profileID}
		if stored, ok := f.values[settingRowKey(row)]; ok {
			e.Value, e.Source, e.Scope, e.ProfileID, e.UpdatedAt = stored.Value, "profile", "profile", profileID, stored.UpdatedAt
		}
		if key == "playback.preferred_quality" && string(e.Value) == `"2160p"` {
			e.StoredValue, e.Value, e.Constrained, e.ConstraintKind = e.Value, json.RawMessage(`"1080p"`), true, "ceiling"
			e.PermittedValues = []json.RawMessage{json.RawMessage(`"auto"`), json.RawMessage(`"1080p"`)}
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeSettingValuesSeam) ResolveEffectiveSettings(_ context.Context, _ int, q handlers.EffectiveSettingsQuery) ([]handlers.EffectiveSettingValueView, error) {
	if f.err != nil {
		return nil, f.err
	}
	views, apiErr := f.effective(q, q.Keys, "keys")
	if apiErr != nil {
		return nil, apiErr
	}
	return views, nil
}

func (f *fakeSettingValuesSeam) ResolveEffectiveSettingContexts(_ context.Context, _ int, q handlers.EffectiveSettingsQuery, contexts []handlers.EffectiveContextRequest) ([]handlers.EffectiveSettingContextView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(q.Keys) == 0 {
		return nil, settingAPIError(400, "bad_request", "keys", "keys must contain at least one setting")
	}
	if len(contexts) == 0 {
		return nil, settingAPIError(400, "bad_request", "contexts", "contexts must contain at least one content context")
	}
	out := make([]handlers.EffectiveSettingContextView, 0, len(contexts))
	seen := map[string]bool{}
	for _, c := range contexts {
		if seen[c.ContextID] {
			return nil, settingAPIError(400, "bad_request", "contexts", "context_id values must be unique")
		}
		seen[c.ContextID] = true
		if len(c.LibraryID) == 0 && c.SeriesID == "" {
			return nil, settingAPIError(400, "bad_request", "contexts", "Every context requires library_id or series_id")
		}
		views, apiErr := f.effective(q, q.Keys, "keys")
		if apiErr != nil {
			return nil, apiErr
		}
		for i := range views {
			views[i].SourceContext = &handlers.EffectiveSourceContextView{ProfileID: q.ActiveProfileID, SeriesID: c.SeriesID}
			if len(c.LibraryID) > 0 {
				var text string
				_ = json.Unmarshal(c.LibraryID, &text)
				n, _ := strconv.Atoi(text)
				views[i].SourceContext.LibraryID = n
			}
		}
		out = append(out, handlers.EffectiveSettingContextView{ContextID: c.ContextID, Settings: views})
	}
	return out, nil
}
