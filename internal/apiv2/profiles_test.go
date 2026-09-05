package apiv2

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestUpdateProfile(t *testing.T) {
	profiles := &fakeProfiles{view: fixtureProfileView()}
	h := newTestHandler(t, pilotDeps(nil, profiles))
	rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura","subtitle_mode":"always","allowed_library_ids":["3","4"]}`, with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"id":"p-owner","name":"Laura","avatar":"preset:fox","avatar_url":"/avatars/presets/fox.png","avatar_source":"preset","has_pin":false,"is_child":false,"is_primary":true,"max_content_rating":"","quality_preference":"auto","language":"en","preferred_metadata_language":"","subtitle_language":"","subtitle_mode":"auto","auto_skip_intro":true,"auto_skip_credits":false,"auto_skip_recap":false,"auto_play_next_preview":false,"show_forced_subtitles":false,"library_restrictions_enabled":false,"allowed_library_ids":["3"],"max_playback_quality":"1080p","created_at":"2026-01-02T03:04:05.000Z","updated_at":"2026-01-02T03:04:05.000Z"}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	cmd := profiles.last
	if cmd.UserID != 1 || cmd.ProfileID != "p-owner" || cmd.ActiveProfileID != "p-owner" {
		t.Fatalf("command = %+v", cmd)
	}
	if cmd.Request.Name == nil || *cmd.Request.Name != "Laura" || cmd.Request.SubtitleMode == nil || *cmd.Request.SubtitleMode != "always" {
		t.Fatalf("request = %+v", cmd.Request)
	}
	if ids := cmd.Request.AllowedLibraryIDs; ids == nil || len(*ids) != 2 || (*ids)[1] != 4 {
		t.Fatalf("allowed_library_ids = %v", ids)
	}
	// The verifier answers from the viewer scope the gate resolved.
	if err := cmd.VerifyProfile("p-owner"); err != nil {
		t.Fatalf("verified profile rejected: %v", err)
	}
	if err := cmd.VerifyProfile("p-primary"); err != access.ErrProfileUnverified { //nolint:errorlint // the sentinel is returned directly
		t.Fatalf("other profile accepted: %v", err)
	}
}

func TestUpdateProfileOmittedVersusNull(t *testing.T) {
	profiles := &fakeProfiles{view: fixtureProfileView()}
	h := newTestHandler(t, pilotDeps(nil, profiles))
	auth := bearer(memberToken)

	// Omitted: nothing about the avatar or PIN reaches the store.
	rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"is_child":true}`, auth)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if req := profiles.last.Request; req.Avatar != nil || req.PIN != nil || req.MaxContentRating != nil || req.IsChild == nil || !*req.IsChild {
		t.Fatalf("request = %+v", req)
	}
	// The header is optional on this operation: no active profile.
	if profiles.last.ActiveProfileID != "" {
		t.Fatalf("active profile = %q", profiles.last.ActiveProfileID)
	}

	// Explicit null: the clearing form ("" in the v1 request) is sent.
	rec = do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"avatar":null,"pin":null,"max_content_rating":null,"max_playback_quality":null}`, auth)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	req := profiles.last.Request
	for name, v := range map[string]*string{"avatar": req.Avatar, "pin": req.PIN, "max_content_rating": req.MaxContentRating, fieldMaxPlaybackQuality: req.MaxPlaybackQuality} {
		if v == nil || *v != "" {
			t.Errorf("%s: want clearing form, got %v", name, v)
		}
	}

	// A value.
	rec = do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"pin":"1234","max_playback_quality":"2160p"}`, auth)
	if rec.Code != 200 || *profiles.last.Request.PIN != "1234" || *profiles.last.Request.MaxPlaybackQuality != "2160p" {
		t.Fatalf("%d %+v", rec.Code, profiles.last.Request)
	}

	// Null on a member that does not admit clearing is a type failure.
	p := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"is_child":null}`, auth), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.is_child" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestUpdateProfileValidation(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	auth := bearer(memberToken)
	for _, tc := range []struct{ body, location, code string }{
		// quality_preference and subtitle_mode are free-form until their
		// vocabulary is ratified (#135): v1 never validated them and the
		// clients send values outside any inferred enum, so only length is
		// bounded. max_playback_quality has server constants and stays strict.
		{`{"subtitle_mode":"` + strings.Repeat("s", 33) + `"}`, "body.subtitle_mode", codeOutOfRange},
		{`{"quality_preference":"` + strings.Repeat("q", 33) + `"}`, "body.quality_preference", codeOutOfRange},
		{`{"max_playback_quality":"4K"}`, "body.max_playback_quality", codeInvalidEnum},
		{`{"nickname":"x"}`, "body.nickname", codeUnknownField},
		{`{"name":""}`, "body.name", codeOutOfRange},
		{`{"allowed_library_ids":["x"]}`, "body.allowed_library_ids", codeInvalid},
		// A repeated identifier would abort the store's primary-key insert;
		// it is the client's error, answered before the store sees it.
		{`{"allowed_library_ids":["1","1"]}`, "body.allowed_library_ids", codeInvalid},
		// An id no library carries would hit the store's foreign key (a 500
		// on Postgres) or persist dangling where no key is enforced.
		{`{"allowed_library_ids":["1","9"]}`, "body.allowed_library_ids", codeInvalid},
		// Only null clears the PIN: "" must never reach the store as the
		// clearing sentinel, and bcrypt refuses more than 72 bytes.
		{`{"pin":""}`, locationPIN, codeOutOfRange},
		{`{"pin":"` + strings.Repeat("7", 73) + `"}`, locationPIN, codeOutOfRange},
		{`{"pin":"` + strings.Repeat("é", 37) + `"}`, locationPIN, codeOutOfRange}, // 37 runes, 74 bytes
	} {
		p := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", tc.body, auth), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, p.Errors)
		}
	}
	long := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"pin":"`+strings.Repeat("é", 37)+`"}`, auth), TypeValidationFailed)
	if len(long.Errors) != 1 || long.Errors[0].Detail != "PIN must be at most 72 bytes" {
		t.Errorf("long pin detail: errors = %+v", long.Errors)
	}
	// A 72-byte PIN is the longest bcrypt accepts and reaches the store intact.
	profiles := &fakeProfiles{view: fixtureProfileView()}
	h = newTestHandler(t, pilotDeps(nil, profiles))
	// Values the clients send today (web onboarding, Android, Apple) reach
	// the store unchanged.
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"quality_preference":"1080p","subtitle_mode":"forced_only"}`, auth); rec.Code != 200 {
		t.Errorf("client vocabulary: %d %s", rec.Code, rec.Body.String())
	} else if req := profiles.last.Request; req.QualityPreference == nil || *req.QualityPreference != "1080p" || req.SubtitleMode == nil || *req.SubtitleMode != "forced_only" {
		t.Errorf("client vocabulary: request = %+v", req)
	}
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"pin":"`+strings.Repeat("7", 72)+`"}`, auth); rec.Code != 200 || profiles.last.Request.PIN == nil || len(*profiles.last.Request.PIN) != 72 {
		t.Errorf("72-byte pin: %d %s", rec.Code, rec.Body.String())
	}
	dup := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"allowed_library_ids":["2","3","2"]}`, auth), TypeValidationFailed)
	if len(dup.Errors) != 1 || dup.Errors[0].Detail != "duplicate library identifier" {
		t.Errorf("duplicate detail: errors = %+v", dup.Errors)
	}
	unknown := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"allowed_library_ids":["3","9"]}`, auth), TypeValidationFailed)
	if len(unknown.Errors) != 1 || unknown.Errors[0].Detail != "unknown library identifier: 9" {
		t.Errorf("unknown detail: errors = %+v", unknown.Errors)
	}
	// Every id known: the allowlist reaches the store as parsed.
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"allowed_library_ids":["3","1"]}`, auth); rec.Code != 200 || profiles.last.Request.AllowedLibraryIDs == nil || len(*profiles.last.Request.AllowedLibraryIDs) != 2 {
		t.Errorf("known ids: %d %s", rec.Code, rec.Body.String())
	}
	// An empty allowlist has nothing to look up and clears the list without
	// the library service; a non-empty one needs it.
	deps := pilotDeps(nil, nil)
	deps.Libraries = nil
	h = newTestHandler(t, deps)
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"allowed_library_ids":[]}`, auth); rec.Code != 200 {
		t.Errorf("empty allowlist without library service: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"allowed_library_ids":["1"]}`, auth), TypeDependencyUnavailable)
	deps.Libraries = fakeLibraries{err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPatch, "/api/v2/profiles/p-owner", `{"allowed_library_ids":["1"]}`, auth), TypeInternalError)
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name": "x"`, auth), TypeMalformedRequest)
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/", `{}`, auth), TypeNotFound)
}

func TestUpdateProfileDecisions(t *testing.T) {
	auth := bearer(memberToken)
	for _, tc := range []struct {
		err  error
		want ProblemType
	}{
		{&handlers.APIError{Status: 404, Code: TypeNotFound.ID, Message: "Profile not found"}, TypeNotFound},
		{&handlers.APIError{Status: 409, Code: "name_conflict", Message: "A profile with this name already exists"}, TypeConflict},
		{&handlers.APIError{Status: 403, Code: "forbidden", Message: "You can only update the active profile's playback preferences"}, TypePermissionDenied},
		{&handlers.APIError{Status: 403, Code: codeProfileManagement, Message: "Profile management requires verifying the primary profile PIN"}, TypeProfileVerificationRequired},
		{&handlers.APIError{Status: 400, Code: "bad_request", Message: "Invalid max_playback_quality", Field: fieldMaxPlaybackQuality}, TypeValidationFailed},
		{&handlers.APIError{Status: 500, Code: TypeInternalError.ID, Message: "Failed to store profile preferences"}, TypeInternalError},
		{errStore, TypeInternalError},
	} {
		h := newTestHandler(t, pilotDeps(nil, &fakeProfiles{err: tc.err}))
		p := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, auth), tc.want)
		if tc.want == TypeValidationFailed && (len(p.Errors) != 1 || p.Errors[0].Location != "body.max_playback_quality") {
			t.Fatalf("errors = %+v", p.Errors)
		}
		if tc.want == TypeInternalError && p.Detail != "An unexpected error occurred." {
			t.Fatalf("detail leaked: %q", p.Detail)
		}
	}

	// A PIN-locked primary profile manages the household only once its PIN
	// is verified. An API key passes the viewer-access gate without the PIN
	// (SkipPINVerification), and v1 verifyProfileToken still refuses it (no
	// login session, no profile token), so the v2 verifier refuses it too.
	profiles := &fakeProfiles{view: fixtureProfileView(), lockedPrimary: "p-primary-locked"}
	h := newTestHandler(t, pilotDeps(nil, profiles))
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, with(bearer(apiKeyToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
	if err := profiles.last.VerifyProfile("p-primary-locked"); !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("api key stood in for the PIN: %v", err)
	}
	// The same API key on an unlocked primary needs no PIN and manages the
	// household, as in v1.
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, with(bearer(apiKeyToken), "X-Profile-Id", "p-primary")); rec.Code != 200 {
		t.Fatalf("api key on unlocked primary: %d %s", rec.Code, rec.Body.String())
	}
	if err := profiles.last.VerifyProfile("p-primary"); err != nil {
		t.Fatalf("unlocked primary rejected: %v", err)
	}
	// A login session that presented the profile token is verified.
	if rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, with(with(bearer(memberToken), "X-Profile-Id", "p-primary-locked"), "X-Profile-Token", "t")); rec.Code != 200 {
		t.Fatalf("session with token: %d %s", rec.Code, rec.Body.String())
	}
	if err := profiles.last.VerifyProfile("p-primary-locked"); err != nil {
		t.Fatalf("token-verified primary rejected: %v", err)
	}
}

func TestUpdateProfileDenied(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, nil), TypeAuthenticationRequired)
	// A declared profile is still judged even though it is optional.
	requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	// Demo mode refuses the mutation to a non-admin.
	demo := pilotDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, bearer(memberToken)), TypePermissionDenied)
}

// A legacy stored quality_preference (the pre-validation schema default) is
// served unchanged: the read model documents canonical values but does not
// constrain what older profiles carry.
func TestUpdateProfileServesLegacyStoredEnum(t *testing.T) {
	view := fixtureProfileView()
	view.QualityPreference = "1080p"
	profiles := &fakeProfiles{view: view}
	h := newTestHandler(t, pilotDeps(nil, profiles))

	rec := do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", `{"name":"Laura"}`, bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"quality_preference":"1080p"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestListProfiles(t *testing.T) {
	profiles := &fakeProfiles{view: fixtureProfileView(), avatarStore: true}
	h := newTestHandler(t, pilotDeps(nil, profiles))
	// No profile header: the picker calls this before one is selected.
	rec := do(t, h, http.MethodGet, "/api/v2/profiles", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	want := `{"items":[{"id":"p-owner","name":"Laura","avatar":"preset:fox","avatar_url":"/avatars/presets/fox.png","avatar_source":"preset","has_pin":false,"is_child":false,"is_primary":true,"max_content_rating":"","quality_preference":"auto","language":"en","preferred_metadata_language":"","subtitle_language":"","subtitle_mode":"auto","auto_skip_intro":true,"auto_skip_credits":false,"auto_skip_recap":false,"auto_play_next_preview":false,"show_forced_subtitles":false,"library_restrictions_enabled":false,"allowed_library_ids":["3"],"max_playback_quality":"1080p","created_at":"2026-01-02T03:04:05.000Z","updated_at":"2026-01-02T03:04:05.000Z"}],"avatar_upload_enabled":true}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A declared profile is still judged; the offset parameter is unknown.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/profiles", "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/profiles?offset=1", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/profiles", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, newTestHandler(t, pilotDeps(nil, &fakeProfiles{err: errors.New("boom")})), http.MethodGet, "/api/v2/profiles", "", bearer(memberToken)), TypeInternalError)
}

func TestCreateProfile(t *testing.T) {
	profiles := &fakeProfiles{view: fixtureProfileView()}
	h := newTestHandler(t, pilotDeps(nil, profiles))
	rec := do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Kid","is_child":true,"pin":"1234","allowed_library_ids":["3","4"],"max_playback_quality":"1080p"}`, with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v2/profiles/p-new" {
		t.Fatalf("Location = %q", loc)
	}
	if !strings.Contains(rec.Body.String(), `"id":"p-new","name":"Kid"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	cmd := profiles.lastCreate
	if cmd == nil || cmd.UserID != 1 || cmd.ActiveProfileID != "p-owner" {
		t.Fatalf("command = %+v", cmd)
	}
	req := cmd.Request
	if req.Name != "Kid" || !req.IsChild || req.PIN != "1234" || len(req.AllowedLibraryIDs) != 2 || req.AllowedLibraryIDs[1] != 4 || req.MaxPlaybackQuality != "1080p" {
		t.Fatalf("request = %+v", req)
	}
	// Omitted members take v1's defaults: show_forced_subtitles is left to
	// the handler (nil), the rest are the zero value.
	if req.ShowForcedSubtitles != nil || req.AutoSkipIntro || req.Avatar != "" || req.AllowedLibraryIDs == nil {
		t.Fatalf("defaults = %+v", req)
	}
	if err := cmd.VerifyProfile("p-owner"); err != nil {
		t.Fatalf("verified profile rejected: %v", err)
	}
	// The first profile on an account is created without a profile header.
	rec = do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Laura","show_forced_subtitles":false}`, bearer(memberToken))
	if rec.Code != 201 || profiles.lastCreate.ActiveProfileID != "" || profiles.lastCreate.Request.ShowForcedSubtitles == nil || *profiles.lastCreate.Request.ShowForcedSubtitles {
		t.Fatalf("%d %+v", rec.Code, profiles.lastCreate)
	}
}

func TestCreateProfileValidation(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	auth := bearer(memberToken)
	for _, tc := range []struct{ body, location, code string }{
		{`{}`, "body.name", codeRequired},
		{`{"name":""}`, "body.name", codeOutOfRange},
		{`{"name":"Kid","is_child":null}`, "body.is_child", codeInvalidType},
		{`{"name":"Kid","pin":null}`, locationPIN, codeInvalidType},
		{`{"name":"Kid","pin":""}`, locationPIN, codeOutOfRange},
		{`{"name":"Kid","pin":"` + strings.Repeat("é", 37) + `"}`, locationPIN, codeOutOfRange},
		{`{"name":"Kid","max_playback_quality":"4K"}`, "body.max_playback_quality", codeInvalidEnum},
		{`{"name":"Kid","allowed_library_ids":["1","1"]}`, locationAllowedLibraryIDs, codeInvalid},
		{`{"name":"Kid","allowed_library_ids":["9"]}`, locationAllowedLibraryIDs, codeInvalid},
		{`{"name":"Kid","nickname":"x"}`, "body.nickname", codeUnknownField},
	} {
		p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/profiles", tc.body, auth), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, p.Errors)
		}
	}
}

func TestCreateProfileDecisions(t *testing.T) {
	auth := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct {
		err  error
		want ProblemType
	}{
		{&handlers.APIError{Status: http.StatusConflict, Code: "name_conflict", Message: "A profile with this name already exists"}, TypeConflict},
		{&handlers.APIError{Status: http.StatusConflict, Code: "profile_limit_reached", Message: "This account has reached its profile limit (5)"}, TypeConflict},
		{&handlers.APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "Profile management requires the primary profile or admin access"}, TypePermissionDenied},
		{&handlers.APIError{Status: http.StatusForbidden, Code: codeProfileManagement, Message: "Profile management requires verifying the primary profile PIN"}, TypeProfileVerificationRequired},
		{&handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Invalid avatar", Field: "avatar"}, TypeValidationFailed},
		{errors.New("boom"), TypeInternalError},
	} {
		h := newTestHandler(t, pilotDeps(nil, &fakeProfiles{err: tc.err}))
		requireProblem(t, do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, auth), tc.want)
	}
	// A PIN-locked primary profile manages the household only once the
	// gate verified it by X-Profile-Token.
	profiles := &fakeProfiles{view: fixtureProfileView(), lockedPrimary: "p-primary-locked"}
	h := newTestHandler(t, pilotDeps(nil, profiles))
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, with(bearer(memberToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
	if rec := do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, with(with(bearer(memberToken), "X-Profile-Id", "p-primary-locked"), "X-Profile-Token", "t")); rec.Code != 201 {
		t.Fatalf("verified: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, with(bearer(apiKeyToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
}

func TestCreateProfileDenied(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	demo := pilotDeps(nil, nil)
	demo.DemoSettings = fakeSettings{demo: true}
	requireProblem(t, do(t, newTestHandler(t, demo), http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, bearer(memberToken)), TypePermissionDenied)
	unwired := pilotDeps(nil, nil)
	unwired.Profiles = nil
	requireProblem(t, do(t, newTestHandler(t, unwired), http.MethodPost, "/api/v2/profiles", `{"name":"Kid"}`, bearer(memberToken)), TypeDependencyUnavailable)
}
