package apiv2

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
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
	deps.Devices = fixtureDevices()
	deps.Sessions = &fakeSessionService{}
	deps.OAuth = fakeOAuth{codes: map[string]auth.OAuthCompletion{"c0de": {AccessToken: "acc", RefreshToken: "ref", ExpiresIn: 3600, NextURL: "/me"}}}
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
	return deps
}

// fakeDevices stands in for the device-pairing seam on *handlers.AuthHandler:
// a fixed set of requests keyed by their codes, answering the way the real
// seam does (*handlers.APIError with the v1 status and code).
type fakeDevices struct {
	configured bool
	// requests maps a device, browser, or user code to its request.
	requests map[string]*fakeDeviceRequest
	err      error
}

type fakeDeviceRequest struct {
	Status    string
	Temporary bool
	Purpose   string
	ExpiresAt time.Time
	// approvedBy is set once approved; a poll then collects tokens.
	approvedBy int
	profileID  string
}

func (f fakeDevices) DeviceLoginConfigured() bool { return f.configured }

func (f fakeDevices) StartDeviceLogin(_ context.Context, in auth.DeviceLoginStartInput) (*auth.DeviceLoginStartResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	purpose := in.ClientPurpose
	if purpose == "" {
		purpose = auth.DeviceLoginPurposeLogin
	}
	if (purpose == auth.DeviceLoginPurposeRemote) != in.Temporary {
		return nil, &handlers.APIError{Status: 400, Code: "bad_request", Message: "Invalid device login purpose", Field: "client_purpose"}
	}
	return &auth.DeviceLoginStartResult{
		DeviceCode: "dev-1", UserCode: "ABCD-1234", MatchCode: "42",
		VerificationURI: in.BaseURL + "/link", VerificationURIComplete: in.BaseURL + "/link?code=ABCD-1234",
		ExpiresAt: fixedTime().Add(10 * time.Minute), ExpiresIn: 600, Interval: 5,
		DeviceName: in.DeviceName, DevicePlatform: in.DevicePlatform, ClientPurpose: purpose, Temporary: in.Temporary,
	}, nil
}

func (f fakeDevices) find(in auth.DeviceLoginLookupInput) (*fakeDeviceRequest, error) {
	if f.err != nil {
		return nil, f.err
	}
	if r := f.requests[in.BrowserCode]; r != nil && in.BrowserCode != "" {
		return r, nil
	}
	if r := f.requests[in.UserCode]; r != nil && in.UserCode != "" {
		return r, nil
	}
	return nil, &handlers.APIError{Status: 404, Code: "not_found", Message: "Device login request not found"}
}

func (f fakeDevices) LookupDeviceLogin(_ context.Context, in auth.DeviceLoginLookupInput) (*auth.DeviceLoginInfo, error) {
	r, err := f.find(in)
	if err != nil {
		return nil, err
	}
	info := &auth.DeviceLoginInfo{Status: r.Status, UserCode: "ABCD-1234", MatchCode: "42", DeviceName: "Living room TV",
		DevicePlatform: "tvos", IPAddressHint: "192.168.1.x", ExpiresAt: r.ExpiresAt, ClientPurpose: r.Purpose, Temporary: r.Temporary}
	if r.Status == "expired" {
		info.UserCode, info.MatchCode = "", ""
	}
	return info, nil
}

func (f fakeDevices) PollDeviceLogin(_ context.Context, deviceCode string) (*handlers.DeviceLoginPollView, error) {
	if f.err != nil {
		return nil, f.err
	}
	r := f.requests[deviceCode]
	if r == nil {
		return nil, &handlers.APIError{Status: 404, Code: "not_found", Message: "Device login request not found"}
	}
	view := &handlers.DeviceLoginPollView{Status: r.Status, PollAfter: 5}
	if r.Status == auth.DeviceLoginStatusApproved {
		view.Tokens = &handlers.TokenPairView{AccessToken: "acc", RefreshToken: "ref", ExpiresIn: 3600,
			User: handlers.UserView{ID: r.approvedBy, Username: "laura", Email: "laura@example.test", Role: "user", Permissions: []string{}, DownloadAllowed: true}}
		if r.Temporary {
			view.Temporary = true
			view.ProfileID = r.profileID
			view.ProfileToken = "ptok"
			view.SessionExpiresAt = fixedTime().Add(2 * time.Hour)
		}
	}
	return view, nil
}

func (f fakeDevices) decide(in auth.DeviceLoginLookupInput, want string) (handlers.DeviceLoginDecision, error) {
	r, err := f.find(in)
	if err != nil {
		return handlers.DeviceLoginDecision{}, err
	}
	switch r.Status {
	case "expired":
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 410, Code: "expired", Message: "Device login request has expired"}
	case auth.DeviceLoginStatusConsumed:
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 409, Code: "consumed", Message: "Device login request has already been used"}
	case auth.DeviceLoginStatusDenied:
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 409, Code: "denied", Message: "Device login request has already been denied"}
	}
	return handlers.DeviceLoginDecision{Status: want}, nil
}

func (f fakeDevices) ApproveDeviceLogin(_ context.Context, in auth.DeviceLoginLookupInput, userID int) (handlers.DeviceLoginDecision, error) {
	if userID == 0 {
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 401, Code: "unauthorized", Message: "Authentication required"}
	}
	r, err := f.find(in)
	if err == nil && r.Purpose == auth.DeviceLoginPurposeRemote {
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 409, Code: "purpose_mismatch", Message: "Device login purpose does not match this approval route"}
	}
	return f.decide(in, auth.DeviceLoginStatusApproved)
}

func (f fakeDevices) ApproveDeviceHandoff(_ context.Context, in auth.DeviceLoginLookupInput, userID int, profileID string) (handlers.DeviceLoginDecision, error) {
	if userID == 0 || profileID == "" {
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 403, Code: "profile_required", Message: "An active verified profile is required"}
	}
	r, err := f.find(in)
	if err == nil && r.Purpose != auth.DeviceLoginPurposeRemote {
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 409, Code: "purpose_mismatch", Message: "Device login purpose does not match this approval route"}
	}
	return f.decide(in, auth.DeviceLoginStatusApproved)
}

func (f fakeDevices) DenyDeviceLogin(_ context.Context, in auth.DeviceLoginLookupInput, userID int) (handlers.DeviceLoginDecision, error) {
	if userID == 0 {
		return handlers.DeviceLoginDecision{}, &handlers.APIError{Status: 401, Code: "unauthorized", Message: "Authentication required"}
	}
	return f.decide(in, auth.DeviceLoginStatusDenied)
}

// fixtureDevices is the device-pairing fake every device test starts from.
func fixtureDevices() fakeDevices {
	exp := fixedTime().Add(10 * time.Minute)
	pending := &fakeDeviceRequest{Status: auth.DeviceLoginStatusPending, Purpose: auth.DeviceLoginPurposeLogin, ExpiresAt: exp}
	approved := &fakeDeviceRequest{Status: auth.DeviceLoginStatusApproved, Purpose: auth.DeviceLoginPurposeLogin, ExpiresAt: exp, approvedBy: 1}
	handoff := &fakeDeviceRequest{Status: auth.DeviceLoginStatusApproved, Purpose: auth.DeviceLoginPurposeRemote, Temporary: true, ExpiresAt: exp, approvedBy: 1, profileID: "p-owner"}
	remotePending := &fakeDeviceRequest{Status: auth.DeviceLoginStatusPending, Purpose: auth.DeviceLoginPurposeRemote, Temporary: true, ExpiresAt: exp}
	return fakeDevices{configured: true, requests: map[string]*fakeDeviceRequest{
		"dev-pending": pending, "br-pending": pending, "ABCD-1234": pending,
		"dev-approved": approved,
		"dev-handoff":  handoff,
		"br-remote":    remotePending,
		"br-expired":   {Status: "expired", Purpose: auth.DeviceLoginPurposeLogin},
		"br-consumed":  {Status: auth.DeviceLoginStatusConsumed, Purpose: auth.DeviceLoginPurposeLogin, ExpiresAt: exp},
		"br-denied":    {Status: auth.DeviceLoginStatusDenied, Purpose: auth.DeviceLoginPurposeLogin, ExpiresAt: exp},
	}}
}

// fakeSessionService stands in for the login-session seam on
// *handlers.AuthHandler.
type fakeSessionService struct {
	// loggedOut and ended record the session ids the calls received.
	loggedOut []string
	ended     []string
	err       error
}

func (f *fakeSessionService) Login(_ context.Context, in handlers.LoginInput) (handlers.TokenPairView, error) {
	if f.err != nil {
		return handlers.TokenPairView{}, f.err
	}
	if in.Username == "" || in.Password == "" {
		return handlers.TokenPairView{}, &handlers.APIError{Status: 400, Code: "bad_request", Message: "Username and password are required"}
	}
	switch {
	case in.Username == "laura" && in.Password == "pw":
		return handlers.TokenPairView{AccessToken: "acc", RefreshToken: "ref", ExpiresIn: 3600,
			User: handlers.UserView{ID: 1, Username: "laura", Email: "laura@example.test", Role: "user", Permissions: []string{"marker_edit"}, DownloadAllowed: true}}, nil
	case in.Username == "off":
		return handlers.TokenPairView{}, &handlers.APIError{Status: 403, Code: "user_disabled", Message: "User account is disabled"}
	}
	return handlers.TokenPairView{}, &handlers.APIError{Status: 401, Code: "invalid_credentials", Message: "Invalid username or password"}
}

func (f *fakeSessionService) Logout(_ context.Context, claims *auth.Claims) error {
	if f.err != nil {
		return f.err
	}
	f.loggedOut = append(f.loggedOut, claims.SessionID)
	return nil
}

func (f *fakeSessionService) EndImpersonation(_ context.Context, claims *auth.Claims) error {
	if f.err != nil {
		return f.err
	}
	if claims.ImpersonatorUserID == nil {
		return &handlers.APIError{Status: 400, Code: "not_impersonating", Message: "No active impersonation session"}
	}
	f.ended = append(f.ended, claims.SessionID)
	return nil
}

// fakeOAuth stands in for *auth.OAuthHandler.Complete: one redeemable code.
type fakeOAuth struct {
	codes map[string]auth.OAuthCompletion
}

func (f fakeOAuth) Complete(_ context.Context, code string) (auth.OAuthCompletion, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return auth.OAuthCompletion{}, auth.ErrOAuthCodeRequired
	}
	c, ok := f.codes[code]
	if !ok {
		return auth.OAuthCompletion{}, auth.ErrOAuthCompletionInvalid
	}
	return c, nil
}
