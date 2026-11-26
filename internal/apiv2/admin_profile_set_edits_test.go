package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// adminProfileSetEditFixtureCases pin the shapes the Users page reads.
func adminProfileSetEditFixtureCases() []fixtureCase {
	base := "/api/v2/admin/users/7"
	return []fixtureCase{
		{name: "admin_profile_set_edits_ok", operationID: "listAdminUserProfileSetEdits",
			scenario: "An account's edits on this server: an owner's override, a profile the owner removed, the person's edit not yet merged, and the settled record of the person's edit over the owner's.",
			method:   http.MethodGet, path: base + "/profile-set/edits", headers: actingRequestAdmin,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminProfileSetEdits"},
		{name: "admin_profile_override_clear_ok", operationID: "clearAdminUserProfileOverride",
			scenario: "The owner clears an override: the set's value is back now, and no push could be queued.",
			method:   http.MethodPatch, path: base + "/profiles/p-kid/overrides/max_content_rating", body: `{"state":"cleared"}`, headers: with(actingRequestAdmin, "If-Match", `"pso1-fixture"`),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminProfileSetCleared"},
		{name: "admin_profile_override_clear_not_found", operationID: "clearAdminUserProfileOverride",
			scenario: "A field with no override, such as one the person has since changed themselves.",
			method:   http.MethodPatch, path: base + "/profiles/p-kid/overrides/pin", body: `{"state":"cleared"}`, headers: with(actingRequestAdmin, "If-Match", `"pso1-fixture"`),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem"},
		{name: "admin_profile_hidden_clear_ok", operationID: "clearAdminUserHiddenProfile",
			scenario: "The owner clears a removed profile's hidden mark; it can return with the person's next set if that set still has it, and a push to their phone was queued.",
			method:   http.MethodDelete, path: base + "/profile-set/hidden/6c9a2f2e-4f3b-4c55-9d43-1f0e2b7a8c11", headers: actingRequestAdmin,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminProfileSetCleared"},
		{name: "admin_profile_settled_edit_dismiss", operationID: "dismissAdminUserSettledProfileEdit",
			scenario: "The owner dismisses the record that the person's own change replaced the owner's value.",
			method:   http.MethodDelete, path: base + "/profiles/p-kid/settled-edits/max_content_rating", headers: actingRequestAdmin,
			status: http.StatusNoContent},
		{name: "admin_profile_override_clear_name_taken", operationID: "clearAdminUserProfileOverride",
			scenario: "Putting the set's name back would give two profiles one name, so the clear is refused as the owner's own rename would be, and the override stays.",
			method:   http.MethodPatch, path: base + "/profiles/p-kid/overrides/name", body: `{"state":"cleared"}`, headers: with(actingRequestAdmin, "If-Match", `"pso1-fixture"`),
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem"},
	}
}

func (fixtureProfileSets) ListProfileSetEdits(context.Context, int) ([]handlers.ProfileSetEditView, error) {
	kid, guest, at := "0f8fad5b-d9cb-469f-a165-70867728950e", "6c9a2f2e-4f3b-4c55-9d43-1f0e2b7a8c11", time.Date(2026, 11, 25, 3, 50, 0, 0, time.UTC)
	admin := handlers.ProfileSetEditActor{UserID: 1, Username: "admin"}
	person := handlers.ProfileSetEditActor{UserID: 7, Username: "sam"}
	return []handlers.ProfileSetEditView{
		{Kind: handlers.ProfileSetEditOverride, SyncID: kid, ProfileID: "p-kid", ProfileName: "Kid", Field: "max_content_rating", By: "owner", Actor: admin, At: at,
			Value: json.RawMessage(`"G"`), SetValue: json.RawMessage(`"PG"`), ClearApplies: true, Token: "fixture"},
		{Kind: handlers.ProfileSetEditOverride, SyncID: kid, ProfileID: "p-kid", ProfileName: "Kid", Field: "pin", By: "owner", Actor: admin, At: at,
			Value: json.RawMessage(`"set"`), SetValue: json.RawMessage(`"set"`), Token: "fixture-pin"},
		{Kind: handlers.ProfileSetEditUnmerged, SyncID: kid, ProfileID: "p-kid", ProfileName: "Kid", Field: "settings.playback.audio_language", By: "account", Actor: person, At: at,
			Value: json.RawMessage(`"en"`), Inherited: true, SetResets: true, ReplacedOwner: true},
		{Kind: handlers.ProfileSetEditSettled, SyncID: kid, ProfileID: "p-kid", ProfileName: "Kid", Field: "requests_blocked", By: "account", Actor: person, At: at,
			Value: json.RawMessage(`false`), SetValue: json.RawMessage(`false`)},
		{Kind: handlers.ProfileSetEditHidden, SyncID: guest, ProfileName: "Guest", By: "owner", Actor: handlers.ProfileSetEditActor{UserID: 3}, At: at},
	}, nil
}

func (fixtureProfileSets) ClearProfileSetOverride(_ context.Context, cmd handlers.ProfileSetClearCommand) (handlers.ProfileSetClearResult, error) {
	if cmd.Field == "max_content_rating" || cmd.Field == "name" {
		if cmd.Precondition == nil {
			return handlers.ProfileSetClearResult{}, errors.New("the route sent no precondition")
		}
		if err := cmd.Precondition("fixture"); err != nil {
			return handlers.ProfileSetClearResult{}, err
		}
	}
	if cmd.Field == "name" {
		return handlers.ProfileSetClearResult{}, &handlers.APIError{Status: http.StatusConflict, Code: "name_conflict", Message: "A profile with this name already exists"}
	}
	if cmd.Field != "max_content_rating" {
		return handlers.ProfileSetClearResult{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "No such edit on this profile"}
	}
	return handlers.ProfileSetClearResult{Applied: true}, nil
}

func (fixtureProfileSets) ClearProfileSetHidden(context.Context, handlers.ProfileSetClearCommand) (handlers.ProfileSetClearResult, error) {
	return handlers.ProfileSetClearResult{WakeQueued: true}, nil
}

func (fixtureProfileSets) DismissProfileSetEdit(context.Context, handlers.ProfileSetClearCommand) error {
	return nil
}

// The edit routes take the admin profile routes' authority: an account that
// is not an administrator never reaches the service, and the service gets
// the addressed account.
func TestAdminProfileSetEditsAuthority(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = fixtureAdminAccounts()
	deps.ProfileSetEdits = recordingProfileSetEdits{seen: new(handlers.ProfileSetClearCommand)}
	h := NewHandler(deps)
	base := Prefix + "/admin/users/7"
	routes := []struct{ method, path string }{
		{http.MethodGet, base + "/profile-set/edits"},
		{http.MethodPatch, base + "/profiles/p-kid/overrides/max_content_rating"},
		{http.MethodDelete, base + "/profile-set/hidden/6c9a2f2e-4f3b-4c55-9d43-1f0e2b7a8c11"},
		{http.MethodDelete, base + "/profiles/p-kid/settled-edits/max_content_rating"},
	}
	for _, r := range routes {
		requireProblem(t, do(t, h, r.method, r.path, "", bearer(memberToken)), TypePermissionDenied)
	}
	seen := deps.ProfileSetEdits.(recordingProfileSetEdits).seen
	if seen.UserID != 0 || seen.ProfileID != "" || seen.Field != "" || seen.SyncID != "" {
		t.Fatalf("a member reached the service: %+v", *seen)
	}
	if rec := do(t, h, http.MethodPatch, routes[1].path, `{"state":"cleared"}`, with(actingRequestAdmin, "If-Match", `"pso1-fixture"`)); rec.Code != http.StatusOK || seen.UserID != 7 || seen.ProfileID != "p-kid" {
		t.Fatalf("clear: %d %s, command %+v", rec.Code, rec.Body, *seen)
	}
	requireProblem(t, do(t, h, http.MethodPatch, base+"/profiles/p-kid/overrides/max_playback_quality", `{"state":"cleared"}`, with(actingRequestAdmin, "If-Match", `"pso1-fixture"`)), TypeValidationFailed)
}

type recordingProfileSetEdits struct {
	fixtureProfileSets
	seen *handlers.ProfileSetClearCommand
}

func (r recordingProfileSetEdits) ClearProfileSetOverride(ctx context.Context, cmd handlers.ProfileSetClearCommand) (handlers.ProfileSetClearResult, error) {
	*r.seen = cmd
	return r.fixtureProfileSets.ClearProfileSetOverride(ctx, cmd)
}

func (r recordingProfileSetEdits) ListProfileSetEdits(ctx context.Context, id int) ([]handlers.ProfileSetEditView, error) {
	r.seen.UserID = id
	return r.fixtureProfileSets.ListProfileSetEdits(ctx, id)
}

// The override clear is in the record of guarded operations, a PATCH that
// answers a receipt, and the DELETE it replaced is gone from the document.
// It catches the clear dropping out of the guarded record, which would let
// it run without If-Match, and the old unguarded route coming back.
func TestOverrideClearIsGuarded(t *testing.T) {
	doc := generatedDocument(t)
	item, ok := doc["paths"].(map[string]any)["/api/v2/admin/users/{id}/profiles/{profile_id}/overrides/{field}"].(map[string]any)
	if !ok {
		t.Fatal("no override clear path in the document")
	}
	if _, has := item["delete"]; has {
		t.Fatal("the unguarded DELETE is still in the document")
	}
	patch, _ := item["patch"].(map[string]any)
	if patch == nil || patch["operationId"] != "clearAdminUserProfileOverride" || patch[extGuarded] != true {
		t.Fatalf("override clear = %v; want the guarded PATCH", patch)
	}
	for _, status := range []string{"412", "428"} {
		if _, ok := patch["responses"].(map[string]any)[status]; !ok {
			t.Fatalf("the clear does not document %s", status)
		}
	}
}
