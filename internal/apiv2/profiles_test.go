package apiv2

import (
	"net/http"
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
		{`{"subtitle_mode":"loud"}`, "body.subtitle_mode", codeInvalidEnum},
		{`{"quality_preference":"best"}`, "body.quality_preference", codeInvalidEnum},
		{`{"max_playback_quality":"4K"}`, "body.max_playback_quality", codeInvalidEnum},
		{`{"nickname":"x"}`, "body.nickname", codeUnknownField},
		{`{"name":""}`, "body.name", codeOutOfRange},
		{`{"allowed_library_ids":["x"]}`, "body.allowed_library_ids", codeInvalid},
	} {
		p := requireProblem(t, do(t, h, http.MethodPatch, "/api/v2/profiles/p-owner", tc.body, auth), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.body, p.Errors)
		}
	}
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
		{&handlers.APIError{Status: 403, Code: "profile_management", Message: "Profile management requires verifying the primary profile PIN"}, TypeProfileVerificationRequired},
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
