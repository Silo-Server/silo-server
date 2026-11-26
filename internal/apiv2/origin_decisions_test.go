package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// documentedDecision reads an operation's published decision for a session
// another app's key opened, or nil.
func documentedDecision(t *testing.T, op documentOperation) *SessionDecision {
	t.Helper()
	raw, ok := op.Raw[extOriginDecisions]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var d OriginDecisions
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%s: %s: %v", op.ID, extOriginDecisions, err)
	}
	return d.GrantedApp
}

// judgedClass reports whether a session another app's key opened reaches
// an operation of the class through a gate that reads its decision.
func judgedClass(c Class) bool {
	return c == ClassAuthenticated || c == ClassProfileScoped || c == ClassPermissionGated
}

// TestEveryOperationDecidesForGrantedApps fails by name on an operation a
// session another app's key opened can reach, read or write, that states no
// decision for it, on a decision without its reason, and on an own-only
// decision that does not name the handler check enforcing it. Catches a new
// operation that ships without anyone deciding what such a session may do
// with it.
func TestEveryOperationDecidesForGrantedApps(t *testing.T) {
	for _, op := range documentOperations(t) {
		d := documentedDecision(t, op)
		if !judgedClass(op.Class) {
			if d != nil {
				t.Errorf("%s (%s) states a decision no gate reads", op.ID, op.Class)
			}
			continue
		}
		switch {
		case d == nil:
			t.Errorf("%s %s (%s) states no decision for a session another app's key opened: set Operation.Origins", op.Method, op.Path, op.ID)
		case strings.TrimSpace(d.Reason) == "":
			t.Errorf("%s: %s decision without a reason", op.ID, d.Verdict)
		case d.Verdict == VerdictOwnOnly && strings.TrimSpace(d.Check) == "":
			t.Errorf("%s: own-only decision names no handler check", op.ID)
		}
	}
}

// pathParam matches a path template parameter.
var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestRefusedOperationsAnswerGrantedAppsFirst: for every operation the
// document says a session another app's key opened is refused, a request
// from such a session answers 403 permission_denied with the refusal's
// detail, and the same request from the account's password session does not
// (it may answer anything else: 400, 404, 503). The requests are built from
// the document alone, so a refusal that is declared but not enforced, or one
// that leaks onto the person's own sessions, fails here by name.
func TestRefusedOperationsAnswerGrantedAppsFirst(t *testing.T) {
	deps := parityDeps(false)
	pass := func(next http.Handler) http.Handler { return next }
	deps.PermissionGates[policy.PermissionMetadataCuration] = pass
	h := newTestHandler(t, deps)
	refused := 0
	for _, op := range documentOperations(t) {
		d := documentedDecision(t, op)
		if !judgedClass(op.Class) || d == nil || d.Verdict != VerdictRefused {
			continue
		}
		refused++
		t.Run(op.ID, func(t *testing.T) {
			path := pathParam.ReplaceAllString(op.Path, "1")
			body := ""
			if _, ok := op.Raw["requestBody"]; ok {
				body = "{}"
			}
			headers := func(token string) map[string]string {
				return with(bearer(token), "X-Profile-Id", "p-owner")
			}
			p := requireProblem(t, do(t, h, op.Method, path, body, headers(grantedAppToken)), TypePermissionDenied)
			if p.Detail != handlers.SessionNotAllowedDetail {
				t.Fatalf("another app's session: detail %q", p.Detail)
			}
			rec := do(t, h, op.Method, path, body, headers(memberToken))
			var own problemDoc
			if rec.Code == http.StatusForbidden && json.Unmarshal(rec.Body.Bytes(), &own) == nil && own.Detail == handlers.SessionNotAllowedDetail {
				t.Fatalf("password session refused as another app: %s", rec.Body.String())
			}
		})
	}
	if refused == 0 {
		t.Fatal("no refused operation in the document")
	}
}

// TestOriginDecisionDeclarations: Register refuses a decision without its
// reason, an own-only decision that names no check, a check on another
// verdict, and a decision on a class no such session reaches through a gate.
// Catches a mark that publishes a verdict nobody can audit or that no gate
// ever reads.
func TestOriginDecisionDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   Operation
	}{
		{"no reason", Operation{Class: ClassProfileScoped, Origins: grantedApp(decideAllowed(""))}},
		{"own only without check", Operation{Class: ClassProfileScoped, Origins: grantedApp(decideOwnOnly("", "its own"))}},
		{"check on refusal", Operation{Class: ClassProfileScoped, Origins: grantedApp(&SessionDecision{Verdict: VerdictRefused, Reason: "r", Check: "c"})}},
		{"unknown verdict", Operation{Class: ClassProfileScoped, Origins: grantedApp(&SessionDecision{Verdict: "maybe", Reason: "r"})}},
		{"public", Operation{Class: ClassPublic, Origins: grantedApp(decideAllowed("r"))}},
		{"acting admin", Operation{Class: ClassActingAdmin, Origins: grantedApp(decideRefused("r"))}},
	} {
		if checkOriginDecisions(tc.op) == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if err := checkOriginDecisions(Operation{Class: ClassAuthenticated, Origins: grantedApp(decideOwnOnly("check", "its own"))}); err != nil {
		t.Errorf("own only with check: %v", err)
	}
}

// TestRefusedFamiliesNeverReachTheirService: one refused operation or more
// per family, sent by a session another app's key opened, is answered 403
// permission_denied and the family's service is never called; the same
// request from the account's password session reaches it. Each service is a
// tripwire (an interface value with no implementation) that panics when
// called, which the router answers 500. Catches a refusal that moves into, or
// behind, the handler, so the service runs before the session is judged.
func TestRefusedFamiliesNeverReachTheirService(t *testing.T) {
	for _, tc := range []struct {
		family, method, path, body string
		wire                       func(*Dependencies)
	}{
		{"webhook sync", http.MethodPost, "/webhook-sync/connections", `{"provider":"plex","server_name":"Plex","default_profile_id":"p-owner"}`, func(d *Dependencies) {
			d.WebhookSync = struct{ WebhookSyncService }{}
			d.WebhookSyncManagement = struct{ WebhookSyncManagementService }{}
		}},
		{"webhook sync list", http.MethodGet, "/webhook-sync/connections", "", func(d *Dependencies) {
			d.WebhookSync = struct{ WebhookSyncService }{}
			d.WebhookSyncManagement = struct{ WebhookSyncManagementService }{}
		}},
		{"notification webhooks", http.MethodPost, "/notifications/webhooks", `{"name":"hook","url":"https://example.test/hook"}`, func(d *Dependencies) {
			d.NotificationDestinationCreate = struct {
				NotificationDestinationCreateService
			}{}
		}},
		{"notification webhook list", http.MethodGet, "/notifications/webhooks", "", func(d *Dependencies) {
			d.NotificationDestinations = struct{ NotificationDestinationService }{}
		}},
		{"plugin notification destinations", http.MethodPost, "/notifications/plugin-destinations", `{"plugin_id":"p","capability_id":"c","name":"n"}`, func(d *Dependencies) {
			d.NotificationPluginDestinations = struct {
				NotificationPluginDestinationService
			}{}
		}},
		{"notification email address", http.MethodPut, "/notifications/email-preferences/address", `{"verification_id":"6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c","email":"a@example.test"}`, func(d *Dependencies) {
			d.NotificationEmailVerification = struct {
				NotificationEmailVerificationService
			}{}
		}},
		{"discord link", http.MethodPost, "/notifications/discord/link/init", `{}`, func(d *Dependencies) {
			d.NotificationDiscordManagement = struct {
				NotificationDiscordManagementService
			}{}
		}},
		{"watch-provider link", http.MethodPost, "/watch-providers/trakt/auth/api-key", `{"api_key":"synthetic-token"}`, func(d *Dependencies) {
			d.WatchProviders = struct{ WatchProviderService }{}
		}},
		{"history-import link", http.MethodPost, "/history-imports/plex/auth/pin", "", func(d *Dependencies) {
			d.HistoryImports = struct{ HistoryImportService }{}
		}},
		{"sign-in provider unlink", http.MethodDelete, "/account/identities/4", "", func(d *Dependencies) {
			d.ExternalSignIn = struct{ ExternalSignInService }{}
		}},
		{"password change", http.MethodPost, "/account/password", `{"current_password":"pw","new_password":"margin fossil quench hollow"}`, func(d *Dependencies) {
			d.Accounts = struct{ AccountService }{}
		}},
		{"personal API key list", http.MethodGet, "/api-keys", "", func(d *Dependencies) {
			d.PersonalAPIKeys = struct{ PersonalAPIKeyService }{}
		}},
		{"personal API key revoke", http.MethodDelete, "/api-keys/1", "", func(d *Dependencies) {
			d.PersonalAPIKeys = struct{ PersonalAPIKeyService }{}
		}},
		{"profile create", http.MethodPost, "/profiles", `{"name":"Kid"}`, func(d *Dependencies) { d.Profiles = struct{ ProfileService }{} }},
		{"profile change", http.MethodPatch, "/profiles/p-kid", `{}`, func(d *Dependencies) { d.Profiles = struct{ ProfileService }{} }},
		{"profile delete", http.MethodDelete, "/profiles/p-kid", "", func(d *Dependencies) { d.Profiles = struct{ ProfileService }{} }},
		{"device registry", http.MethodGet, "/devices", "", func(d *Dependencies) { d.DeviceSettings = struct{ DeviceSettingsService }{} }},
		{"downloads list", http.MethodGet, "/downloads", "", func(d *Dependencies) { d.Downloads = struct{ DownloadRegistryService }{} }},
		{"downloads create", http.MethodPost, "/downloads", `{"content_id":"movie","media_file_id":"42"}`, func(d *Dependencies) { d.DownloadCreation = struct{ DownloadCreationService }{} }},
	} {
		t.Run(tc.family, func(t *testing.T) {
			deps := parityDeps(false)
			tc.wire(&deps)
			h := newTestHandler(t, deps)
			headers := func(token string) map[string]string { return with(bearer(token), "X-Profile-Id", "p-owner") }
			p := requireProblem(t, do(t, h, tc.method, Prefix+tc.path, tc.body, headers(grantedAppToken)), TypePermissionDenied)
			if p.Detail != handlers.SessionNotAllowedDetail {
				t.Fatalf("another app's session: detail %q", p.Detail)
			}
			if rec := do(t, h, tc.method, Prefix+tc.path, tc.body, headers(memberToken)); rec.Code != http.StatusInternalServerError {
				t.Fatalf("password session did not reach the service: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestGrantedAppPlayerStateWritesStayOnTheActingProfile: the playback, audio
// and subtitle preference writes and the ebook reader settings are a player's
// own state, so a session another app's key opened may make them, and each
// lands on the profile the request acts as and on no other profile. Catches
// one of them being refused again, or a write that reaches past the acting
// profile (another profile's row, or none keyed by profile at all).
func TestGrantedAppPlayerStateWritesStayOnTheActingProfile(t *testing.T) {
	const other = "p-kid"
	audio := &fakeAudioPreferences{prefs: map[string]userstore.AudioPreference{other + "/series-8f2c1a": fixtureAudioPreference()}}
	library := &fakeLibraryPlaybackPreferences{known: map[int]bool{1: true}, prefs: []userstore.LibraryPlaybackPreference{{ProfileID: other, LibraryID: 1}}}
	deps := preferenceDeps(audio, library)
	subtitles := &fakeSubtitlePreferences{prefs: map[string]userstore.SubtitlePreference{other + "/series-8f2c1a": fixtureSubtitlePreference()}}
	deps.SubtitlePreferences = subtitles
	ebook := &fakeEbookConfig{}
	deps.EbookConfig = ebook
	h := newTestHandler(t, deps)
	app := with(bearer(grantedAppToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		landed                   func() bool
	}{
		{"audio preference", http.MethodPut, "/audio-prefs/series-8f2c1a", `{"audio_track_index":3}`, app, func() bool {
			return audio.prefs["p-owner/series-8f2c1a"].AudioTrackIndex == 3 && audio.prefs[other+"/series-8f2c1a"].AudioTrackIndex == fixtureAudioPreference().AudioTrackIndex
		}},
		{"audio preference delete", http.MethodDelete, "/audio-prefs/series-8f2c1a", "", app, func() bool {
			_, own := audio.prefs["p-owner/series-8f2c1a"]
			_, kept := audio.prefs[other+"/series-8f2c1a"]
			return !own && kept
		}},
		{"subtitle preference", http.MethodPut, "/subtitle-prefs/series-8f2c1a", `{"subtitle_language":"eng","subtitle_track_index":0,"subtitle_mode":"always"}`, app, func() bool {
			return subtitles.last != nil && subtitles.last.ProfileID == "p-owner" && subtitles.prefs[other+"/series-8f2c1a"].SubtitleMode == fixtureSubtitlePreference().SubtitleMode
		}},
		{"subtitle preference delete", http.MethodDelete, "/subtitle-prefs/series-8f2c1a", "", app, func() bool {
			_, own := subtitles.prefs["p-owner/series-8f2c1a"]
			_, kept := subtitles.prefs[other+"/series-8f2c1a"]
			return !own && kept
		}},
		{"library playback preference", http.MethodPatch, "/library-playback-prefs/1", `{"audio_language":"eng"}`, app, func() bool {
			return slices.ContainsFunc(library.prefs, func(p userstore.LibraryPlaybackPreference) bool { return p.ProfileID == "p-owner" && p.LibraryID == 1 }) &&
				slices.ContainsFunc(library.prefs, func(p userstore.LibraryPlaybackPreference) bool { return p.ProfileID == other && p.AudioLanguage == "" })
		}},
		{"library playback preference delete", http.MethodDelete, "/library-playback-prefs/1", "", app, func() bool {
			return !slices.ContainsFunc(library.prefs, func(p userstore.LibraryPlaybackPreference) bool { return p.ProfileID == "p-owner" }) &&
				slices.ContainsFunc(library.prefs, func(p userstore.LibraryPlaybackPreference) bool { return p.ProfileID == other })
		}},
		{"ebook reader settings", http.MethodPut, "/ebooks/book/reader-config", `{"config":{"settings":{"theme":"dark"}}}`,
			with(app, "If-Match", ebookConfigTag(1, "p-owner", "book", nil).String()), func() bool {
				return ebook.current != nil && ebook.current.ProfileID == "p-owner" && ebook.current.UserID == 1
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, Prefix+tc.path, tc.body, tc.headers)
			if rec.Code < 200 || rec.Code > 299 {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if !tc.landed() {
				t.Fatal("the write did not land on the acting profile alone")
			}
		})
	}
}

// TestCurrentUserLeavesTheEmailOutForAnotherApp: who the account is stays an
// app's to read, its email does not, so a session another app's key opened
// gets the email member as the shape's "none" (the empty string), and the
// person's own session gets it as before. Catches the email reaching such a
// session again, or leaving the person's own answer.
func TestCurrentUserLeavesTheEmailOutForAnotherApp(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	for token, want := range map[string]string{grantedAppToken: "", memberToken: "laura@example.test"} {
		rec := do(t, h, http.MethodGet, Prefix+"/account/me", "", bearer(token))
		var body Account
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", token, rec.Code, rec.Body.String())
		}
		if body.Email != want || body.Username != "laura" {
			t.Fatalf("%s: email %q, username %q", token, body.Email, body.Username)
		}
	}
}

// TestOriginDecisionsTakeAnotherKind: a second kind of session is one more
// OriginDecisions member and its row in originMembers; the gate, the
// registration check and the published extension then judge it without
// changing. This adds a scratch kind in the test only and checks the gate
// refuses it where its decision says so, passes the granted app as before,
// validates the scratch decision and publishes it. Catches any of the four
// reading the granted app's member alone.
func TestOriginDecisionsTakeAnotherKind(t *testing.T) {
	const scratch auth.SessionKind = "scratch_kind"
	scratchDecision := decideRefused("scratch reason")
	saved := originMembers
	t.Cleanup(func() { originMembers = saved })
	originMembers = append(slices.Clone(saved), originMember{scratch, func(OriginDecisions) *SessionDecision { return scratchDecision }})

	decisions := grantedApp(decideAllowed("r"))
	passed := false
	gate := originGate(decisions)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))
	for kind, wantPass := range map[auth.SessionKind]bool{scratch: false, auth.SessionKindGrantedApp: true, "": true} {
		passed = false
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, SessionID: "s", TokenType: auth.TokenTypeAccess, SessionKind: kind}))
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, r)
		if passed != wantPass || !wantPass && rec.Code != http.StatusForbidden {
			t.Fatalf("kind %q: passed %v, status %d", kind, passed, rec.Code)
		}
	}
	if got := decisions.byKind()[scratch]; got != scratchDecision {
		t.Fatalf("published decisions lack the scratch kind: %v", decisions.byKind())
	}
	scratchDecision = &SessionDecision{Verdict: VerdictRefused}
	if checkOriginDecisions(Operation{Class: ClassProfileScoped, Origins: decisions}) == nil {
		t.Fatal("a scratch decision without its reason was accepted")
	}
}

// TestDeviceLoginLookupRefusesAnotherApp: the lookup is public, so the origin
// gate does not run on it, and an attached session would let a session
// another app's key opened look up the account's Quick Connect codes and mark
// requests opened. The handler refuses such a session; a caller with no
// session and the person's own session get the lookup as before. Catches the
// handler refusal going missing on an operation no gate covers.
func TestDeviceLoginLookupRefusesAnotherApp(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	const path = Prefix + "/auth/device?code=4821-7730"
	p := requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(grantedAppToken)), TypePermissionDenied)
	if p.Detail != handlers.SessionNotAllowedDetail {
		t.Fatalf("detail %q", p.Detail)
	}
	for name, headers := range map[string]map[string]string{"no session": nil, "password session": bearer(memberToken)} {
		if rec := do(t, h, http.MethodGet, path, "", headers); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestDisplayTokenOperationsStayAllowed: the Apple push display-token path
// builds claims with no session kind and runs no origin gate, so an operation
// served through notificationDisplayGateChain must stay allowed for a session
// another app's key opened; a refusal marked there would not be enforced on
// that path. Catches such an operation being marked refused, or own-only,
// without the chain learning to enforce it.
func TestDisplayTokenOperationsStayAllowed(t *testing.T) {
	for _, op := range documentOperations(t) {
		if op.ID != notificationApplePushDisplayOperation {
			continue
		}
		if d := documentedDecision(t, op); d == nil || d.Verdict != VerdictAllowed {
			t.Fatalf("%s is served on the display-token path, which enforces no origin decision; it must stay allowed, got %+v", op.ID, d)
		}
		return
	}
	t.Fatalf("%s is not in the document", notificationApplePushDisplayOperation)
}

// capabilityAllowed reads a capability response's permission answer.
func capabilityAllowed(t *testing.T, h http.Handler, path, token string) (allowed, ok bool) {
	t.Helper()
	rec := do(t, h, http.MethodGet, path, "", with(bearer(token), "X-Profile-Id", "p-owner"))
	var body struct {
		Allowed *bool `json:"allowed"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Allowed == nil {
		return false, false
	}
	return *body.Allowed, true
}

// TestCapabilitiesDoNotOfferRefusedActions: a capability read never reports
// as allowed an action every one of whose operations is refused to the
// session asking. For every capability the document lists with
// x-silo-capability-for, each named operation exists, and where the
// person's own session is told allowed, a session another app's key opened
// is told not allowed when the decisions refuse it all of them. Catches a
// capability that offers such a session a form the gate will refuse, and one
// that names an operation that does not exist.
func TestCapabilitiesDoNotOfferRefusedActions(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Accounts = passwordChanger{deps.Accounts.(fakeAccounts)}
	wireCapabilityServices(&deps)
	h := newTestHandler(t, deps)
	ops := documentOperations(t)
	byID := map[string]documentOperation{}
	for _, op := range ops {
		byID[op.ID] = op
	}
	exercised := []string{}
	for _, op := range ops {
		raw, ok := op.Raw[extCapabilityFor].([]any)
		if !ok {
			continue
		}
		allRefused := true
		for _, v := range raw {
			id, _ := v.(string)
			governed, ok := byID[id]
			if !ok {
				t.Fatalf("%s names %s, which is not an operation", op.ID, id)
			}
			if d := documentedDecision(t, governed); d == nil || d.Verdict != VerdictRefused {
				allRefused = false
			}
		}
		member, ok := capabilityAllowed(t, h, op.Path, memberToken)
		if !ok || !member {
			continue
		}
		exercised = append(exercised, op.ID)
		app, ok := capabilityAllowed(t, h, op.Path, grantedAppToken)
		if !ok || app == allRefused {
			t.Errorf("%s: another app's session told allowed=%v (ok %v); every named operation refused: %v", op.ID, app, ok, allRefused)
		}
	}
	t.Logf("exercised: %v", exercised)
	for _, want := range []string{"getAccountPasswordCapability", "getNotificationEmailVerificationCapabilities", "getProgressBootstrapCapabilities"} {
		if !slices.Contains(exercised, want) {
			t.Errorf("%s was not exercised: the person's own session was not told allowed", want)
		}
	}
}

// wireCapabilityServices gives the capability reads the services they ask,
// so the person's own session is told allowed where the server offers it.
func wireCapabilityServices(deps *Dependencies) {
	deps.NotificationEmailVerification = &fakeEmailVerification{current: true, dispatch: true}
	deps.ProgressBootstrap = &fakeBootstrap{}
	deps.CursorSecret = []byte("synthetic-shared-bootstrap-secret")
}

// TestRefusedOperationsDeclareTheir403: an operation that refuses, or limits
// to its own things, some judged kind of session answers that session 403,
// so the document declares 403 for it, whatever else it declares. Catches a
// refusal the gate enforces but the contract does not document, as on an
// authenticated operation with no demo restriction and no profile.
func TestRefusedOperationsDeclareTheir403(t *testing.T) {
	for _, op := range documentOperations(t) {
		d := documentedDecision(t, op)
		if d == nil || (d.Verdict != VerdictRefused && d.Verdict != VerdictOwnOnly) {
			continue
		}
		responses, _ := op.Raw["responses"].(map[string]any)
		if _, ok := responses["403"]; !ok {
			t.Errorf("%s %s (%s) is %s for another app's session but declares no 403", op.Method, op.Path, op.ID, d.Verdict)
		}
	}
}
