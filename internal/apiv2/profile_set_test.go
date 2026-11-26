package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeProfileSets records the command it was asked to apply.
type fakeProfileSets struct {
	unsupported bool
	// applyErr is the service's refusal; a refused set is not counted as
	// applied.
	applyErr error
	calls    int
	cmd      handlers.ProfileSetCommand
	held     handlers.ProfileSetHeld
}

func (f *fakeProfileSets) ProfileSetSupported() bool { return !f.unsupported }

func (f *fakeProfileSets) ApplyProfileSet(_ context.Context, cmd handlers.ProfileSetCommand) (handlers.ProfileSetResult, error) {
	if f.applyErr != nil {
		return handlers.ProfileSetResult{}, f.applyErr
	}
	f.calls++
	f.cmd = cmd
	return handlers.ProfileSetResult{Version: 1}, nil
}

func (f *fakeProfileSets) HeldProfileSet(context.Context, int) (handlers.ProfileSetHeld, error) {
	return f.held, nil
}

// profileSetKeys answers a verified signed set for account 1, or verifyErr.
type profileSetKeys struct {
	*fakeAppKeys
	verifyErr  error
	sessionErr error
	// signed is what a verified envelope carried; nil is a set the fake
	// service accepts without reading.
	signed *auth.ProfileSetAuthority
}

func (k *profileSetKeys) VerifyProfileSet(context.Context, auth.KeyRequest) (*auth.ProfileSetAuthority, error) {
	if k.verifyErr != nil {
		return nil, k.verifyErr
	}
	if k.signed != nil {
		return k.signed, nil
	}
	return &auth.ProfileSetAuthority{UserID: 1, Set: []byte(testSet)}, nil
}

func (k *profileSetKeys) SessionMayManageKeys(context.Context, *auth.Claims) error {
	return k.sessionErr
}

// testSet is a well-formed set with a member a newer app might add.
const testSet = `{"version":1,"items":[{"sync_id":"0f8fad5b-d9cb-469f-a165-70867728950e","name":"Sam","picture":"","pin":{"keep":true},"is_child":false,"max_content_rating":"","max_advisory_age":null,"require_advisory_age":false,"requests_blocked":false,"settings":{},"a_newer_member":1}]}`

const testSessionSet = `{"set":` + testSet + `}`

// testOpenSessionSet carries members a newer app might add to the set and
// to a PIN, and a 64-character name padded with spaces.
var testOpenSessionSet = `{"set":{"version":1,"future":true,"items":[{"sync_id":"0f8fad5b-d9cb-469f-a165-70867728950e","name":"  ` +
	strings.Repeat("n", 64) + `  ","picture":"","pin":{"keep":true,"future":1},"is_child":false,"max_content_rating":"","max_advisory_age":null,"require_advisory_age":false,"requests_blocked":false,"settings":{}}]}}`

// TestProfileSetProblems: refusals the service reports reach the client as
// their own problems, never as a 500. It catches an account deleted during a
// set, or one with no main profile, answering internal_error.
func TestProfileSetProblems(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want ProblemType
	}{
		{fmt.Errorf("x: %w", userstore.ErrProfileSetAccountGone), TypeSessionExpired},
		{handlers.ErrProfileSetNoPrimary, TypeConflict},
		{handlers.ErrProfileSetStale, TypeProfileSetStale},
	} {
		var p *Problem
		if !errors.As(profileSetProblem(tc.err), &p) || p.Type != tc.want.URI() {
			t.Fatalf("%v answered %+v, want %s", tc.err, p, tc.want.ID)
		}
	}
}

// TestPutProfileSetAuthority: who may send a profile set, in which form. It
// catches an admin account changed by a signature alone, a held-back
// session (another app, a TV, an administrator acting as the account)
// sending one, the switch closing the session form, and a stale profile
// header refusing a signed set.
func TestPutProfileSetAuthority(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		headers    map[string]string
		enabled    bool
		verifyErr  error
		sessionErr error
		sets       fakeProfileSets
		want       ProblemType
		signed     bool
		demo       bool
	}{
		{name: "signed form in demo mode", body: testEnvelope, enabled: true, demo: true, want: TypePermissionDenied},
		{name: "session form in demo mode", body: testSessionSet, demo: true, headers: map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-owner"}, want: TypePermissionDenied},
		{name: "unknown set and pin members, padded name", body: testOpenSessionSet, headers: map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-owner"}},
		{name: "API key in the session form", body: testSessionSet, headers: bearer(apiKeyToken), want: TypeAuthenticationRequired},
		{name: "admin account by signature", body: testEnvelope, enabled: true, verifyErr: auth.ErrProfileSetAdminAccount, want: TypePermissionDenied},
		{name: "admin account by session", body: testSessionSet, headers: map[string]string{"Authorization": "Bearer " + adminToken, profileHeader: "p-owner"}},
		{name: "switch off, signed", body: testEnvelope, want: TypeCapabilityDisabled},
		{name: "switch off, session", body: testSessionSet, headers: map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-owner"}},
		{name: "held-back session", body: testSessionSet, headers: bearer(memberToken), sessionErr: auth.ErrSessionNotAllowed, want: TypePermissionDenied},
		{name: "session form without a session", body: testSessionSet, want: TypeAuthenticationRequired},
		// A client reads 401 as signed out; this session is only told to
		// change its temporary password first.
		{name: "session form, session must change its password", body: testSessionSet,
			headers: map[string]string{"Authorization": "Bearer " + temporaryPasswordToken, profileHeader: "p-owner"}, want: TypePasswordChangeRequired},
		{name: "session form, unknown token", body: testSessionSet, headers: bearer("tok-unknown"), want: TypeAuthenticationRequired},
		{name: "session form, ended session", body: testSessionSet, headers: bearer(expiredToken), want: TypeAuthenticationRequired},
		{name: "signed with a session that must change its password", body: testEnvelope, enabled: true, signed: true,
			headers: bearer(temporaryPasswordToken)},
		{name: "session form not as the main profile", body: testSessionSet, headers: map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-owner"},
			sets: fakeProfileSets{applyErr: handlers.ErrProfileSetNotMainProfile}, want: TypeMainProfileRequired},
		{name: "main profile PIN not proven", body: testSessionSet, headers: map[string]string{"Authorization": "Bearer " + householdToken, profileHeader: "p-primary-locked"}, want: TypeProfileVerificationRequired},
		{name: "both forms", body: `{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln","set":{"version":1,"items":[]}}`, enabled: true, want: TypeValidationFailed},
		{name: "neither form", body: `{}`, enabled: true, want: TypeValidationFailed},
		{name: "SQLite user store", body: testSessionSet, sets: fakeProfileSets{unsupported: true}, want: TypeCapabilityUnsupported},
		{name: "signed with a stale profile header", body: testEnvelope, enabled: true, signed: true,
			headers: map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-gone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := appKeyDeps(&fakeAppKeys{enabled: tc.enabled})
			deps.AppKeys = &profileSetKeys{fakeAppKeys: &fakeAppKeys{enabled: tc.enabled}, verifyErr: tc.verifyErr, sessionErr: tc.sessionErr}
			sets := tc.sets
			deps.ProfileSet = &sets
			if tc.demo {
				deps.DemoSettings = fakeSettings{demo: true}
			}
			rec := do(t, NewHandler(deps), http.MethodPut, Prefix+"/profiles/set", tc.body, tc.headers)
			if tc.want != (ProblemType{}) {
				requireProblem(t, rec, tc.want)
				if sets.calls != 0 {
					t.Fatal("the set was applied")
				}
				return
			}
			if rec.Code != http.StatusOK || sets.calls != 1 || sets.cmd.Signed != tc.signed {
				t.Fatalf("status %d, calls %d, cmd %+v: %s", rec.Code, sets.calls, sets.cmd, rec.Body.String())
			}
		})
	}
}

// TestProfileSetFormsValidateAlike: what a set may hold does not depend on
// how it arrives. Each set below is sent in the session form and inside a
// verified signed envelope, and the two must answer alike: the same status
// and, for a refusal, the same problem with the same errors (location, code
// and detail). It catches the forms drifting apart, as when a missing member
// was "required" at its own location in one form and "invalid" at the payload
// in the other, and a member one form required being optional in the other.
func TestProfileSetFormsValidateAlike(t *testing.T) {
	cases := profileSetFormCases()

	type answer struct {
		Status int
		Type   string
		Detail string
		Errors []ProblemError
	}
	read := func(t *testing.T, rec *httptest.ResponseRecorder) answer {
		t.Helper()
		out := answer{Status: rec.Code}
		if rec.Code == http.StatusOK {
			return out
		}
		var p Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("answer %d is not a problem: %s", rec.Code, rec.Body.String())
		}
		out.Type, out.Detail, out.Errors = p.Type, p.Detail, p.Errors
		slices.SortFunc(out.Errors, func(a, b ProblemError) int {
			return cmp.Or(cmp.Compare(a.Location, b.Location), cmp.Compare(a.Code, b.Code), cmp.Compare(a.Detail, b.Detail))
		})
		return out
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setJSON, _ := json.Marshal(tc.set)
			members := map[string]any{"set": tc.set}
			// What a verified envelope hands the route: the payload's members
			// as sent, and edits_seen as a string when it is one.
			authority := &auth.ProfileSetAuthority{UserID: 1, Set: setJSON}
			if tc.pairs != nil {
				members["pairs"] = tc.pairs
				authority.Pairs, _ = json.Marshal(tc.pairs)
			}
			if tc.editsSeen != nil {
				members["edits_seen"] = tc.editsSeen
				authority.EditsSeenRaw, _ = json.Marshal(tc.editsSeen)
				authority.EditsSeen, _ = tc.editsSeen.(string)
			}
			sessionBody, _ := json.Marshal(members)
			send := func(body string, headers map[string]string) answer {
				deps := appKeyDeps(&fakeAppKeys{enabled: true})
				deps.AppKeys = &profileSetKeys{fakeAppKeys: &fakeAppKeys{enabled: true}, signed: authority}
				deps.ProfileSet = &fakeProfileSets{}
				if tc.stale {
					deps.ProfileSet = &fakeProfileSets{applyErr: handlers.ErrProfileSetStale}
				}
				return read(t, do(t, NewHandler(deps), http.MethodPut, Prefix+"/profiles/set", body, headers))
			}
			session := send(string(sessionBody), map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-owner"})
			signed := send(testEnvelope, nil)
			if !reflect.DeepEqual(session, signed) {
				t.Fatalf("the two forms answer differently:\nsession: %+v\nsigned:  %+v", session, signed)
			}
			if tc.ok != (session.Status == http.StatusOK) {
				t.Fatalf("answer = %+v, want accepted %v", session, tc.ok)
			}
			if tc.wantType != (ProblemType{}) {
				if session.Type != tc.wantType.URI() {
					t.Fatalf("answer = %+v, want %s", session, tc.wantType.ID)
				}
				return
			}
			if !tc.ok && (session.Status != http.StatusUnprocessableEntity || len(session.Errors) == 0) {
				t.Fatalf("a malformed set answered %+v, want 422 with errors", session)
			}
			if tc.wantAt != "" && (len(session.Errors) != 1 || session.Errors[0].Location != tc.wantAt || session.Errors[0].Code != tc.wantCode) {
				t.Fatalf("errors = %+v, want one %s at %s", session.Errors, tc.wantCode, tc.wantAt)
			}
		})
	}
}

// profileSetFormCase is one set sent in both forms.
type profileSetFormCase struct {
	name string
	set  any
	// pairs and editsSeen are sent as they are, whatever their type; nil
	// leaves the member out.
	pairs     any
	editsSeen any
	ok        bool
	// stale has the service answer profile_set_stale to any set that
	// reaches it, as for a set older than the held one.
	stale bool
	// wantAt and wantCode, when set, are the one error the refusal carries.
	wantAt, wantCode string
	// wantType, when set, is a refusal other than validation_failed.
	wantType ProblemType
}

// profileSetFormCases are well-formed sets and one of each kind of malformed
// one: every required member left out, each member with a wrong type or out
// of range, and the faults only the parser sees.
func profileSetFormCases() []profileSetFormCase {
	whole := func() map[string]any {
		return map[string]any{
			"sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e", "name": "Sam", "picture": "", "pin": map[string]any{"keep": true},
			"is_child": false, "max_content_rating": "", "max_advisory_age": nil, "require_advisory_age": false,
			"requests_blocked": false, "settings": map[string]any{},
		}
	}
	with := func(member string, value any) map[string]any {
		item := whole()
		item[member] = value
		return item
	}
	set := func(items ...any) map[string]any { return map[string]any{"version": 1, "items": items} }
	second := with("sync_id", "7c9e6679-7425-40de-944b-e07fc1f90ae7")
	second["name"] = "Alex"
	cases := []profileSetFormCase{
		{name: "a whole item", set: set(whole()), ok: true},
		{name: "settings and a reset", set: set(with("settings", map[string]any{"playback.subtitle_mode": "always"}), with2(second, "settings_reset", []string{"playback.audio_language"})), ok: true},
		{name: "a pair", set: set(whole()), pairs: []any{map[string]any{"sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e", "local_id": "p-owner"}}, ok: true},
		{name: "name a number", set: set(with("name", 5))},
		{name: "name null", set: set(with("name", nil))},
		{name: "name empty", set: set(with("name", ""))},
		{name: "name only spaces", set: set(with("name", "   "))},
		{name: "name 65 characters", set: set(with("name", strings.Repeat("n", 65)))},
		{name: "sync_id upper case", set: set(with("sync_id", "0F8FAD5B-D9CB-469F-A165-70867728950E"))},
		{name: "sync_id not a UUID", set: set(with("sync_id", "x"))},
		{name: "picture too long", set: set(with("picture", strings.Repeat("p", 201)))},
		{name: "pin empty", set: set(with("pin", map[string]any{}))},
		{name: "pin two digits", set: set(with("pin", map[string]any{"set": "12"}))},
		{name: "pin two instructions", set: set(with("pin", map[string]any{"set": "1234", "keep": true}))},
		{name: "pin a string", set: set(with("pin", "1234"))},
		{name: "is_child a string", set: set(with("is_child", "yes"))},
		{name: "max_advisory_age 0", set: set(with("max_advisory_age", 0))},
		{name: "max_advisory_age 22", set: set(with("max_advisory_age", 22))},
		{name: "max_advisory_age a string", set: set(with("max_advisory_age", "12"))},
		{name: "max_content_rating too long", set: set(with("max_content_rating", strings.Repeat("r", 33)))},
		{name: "settings an array", set: set(with("settings", []any{}))},
		{name: "settings null", set: set(with("settings", nil))},
		{name: "settings_reset a string", set: set(with("settings_reset", "x"))},
		{name: "a key set and reset", set: set(with2(with("settings", map[string]any{"playback.subtitle_mode": "always"}), "settings_reset", []string{"playback.subtitle_mode"}))},
		{name: "no items", set: set()},
		{name: "items not an array", set: map[string]any{"version": 1, "items": "x"}},
		{name: "an item not an object", set: set(5)},
		{name: "version negative", set: map[string]any{"version": -1, "items": []any{whole()}}},
		{name: "version a fraction", set: map[string]any{"version": 1.5, "items": []any{whole()}}},
		{name: "version a string", set: map[string]any{"version": "1", "items": []any{whole()}}},
		{name: "version missing", set: map[string]any{"items": []any{whole()}}},
		{name: "two items with one sync_id", set: set(whole(), with("name", "Alex")), wantAt: "body.set.items[1].sync_id", wantCode: codeInvalid},
		// Two items of one name are well formed: whether two names are the
		// same is the server's to judge, after the version, so a stale set
		// with such a pair is told it is stale.
		{name: "two items with one name, against a newer held set", set: set(whole(), with("sync_id", "7c9e6679-7425-40de-944b-e07fc1f90ae7")),
			stale: true, wantType: TypeProfileSetStale},
		{name: "a pair for no item", set: set(whole()), pairs: []any{map[string]any{"sync_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7", "local_id": "p-owner"}}},
		{name: "a pair without local_id", set: set(whole()), pairs: []any{map[string]any{"sync_id": "0f8fad5b-d9cb-469f-a165-70867728950e"}}},
		{name: "pairs not an array", set: set(whole()), pairs: "x"},
		{name: "pairs null", set: set(whole()), pairs: json.RawMessage("null")},
		{name: "edits_seen null", set: set(whole()), editsSeen: json.RawMessage("null")},
		{name: "edits_seen too long", set: set(whole()), editsSeen: strings.Repeat("e", 65)},
		{name: "edits_seen a number", set: set(whole()), editsSeen: 5},
		{name: "edits_seen with a control character", set: set(whole()), editsSeen: "e1.\x00"},
		{name: "edits_seen as the server gave it", set: set(whole()), editsSeen: "", ok: true},
		{name: "items null", set: map[string]any{"version": 1, "items": nil}},
		{name: "version 2^53", set: map[string]any{"version": float64(1 << 53), "items": []any{whole()}}},
		{name: "a pair not an object", set: set(whole()), pairs: []any{5}},
	}
	for member := range whole() {
		item := whole()
		delete(item, member)
		cases = append(cases, profileSetFormCase{name: "without " + member, set: set(item)})
	}
	return cases
}

// with2 is a copy of item with one more member set.
func with2(item map[string]any, member string, value any) map[string]any {
	out := maps.Clone(item)
	out[member] = value
	return out
}

// TestPutProfileSetSpendsTheAccountBudget: a set is limited by account, not
// by address. The budget is spent by the session's account, or by the
// account a signed set names once its signature has verified; a set whose
// signature doesn't verify spends none of it; and a spent budget is 429 with
// Retry-After and applies nothing. It catches a household behind one address
// sharing one person's allowance, and an unverified request spending
// someone's budget.
func TestPutProfileSetSpendsTheAccountBudget(t *testing.T) {
	session := map[string]string{"Authorization": "Bearer " + adminToken, profileHeader: "p-owner"}
	for _, tc := range []struct {
		name      string
		body      string
		headers   map[string]string
		verifyErr error
		spent     bool
		account   int
	}{
		{name: "session form", body: testSessionSet, headers: session, spent: true, account: 2},
		{name: "signed form", body: testEnvelope, spent: true, account: 1},
		{name: "signature that doesn't verify", body: testEnvelope, verifyErr: auth.ErrKeySignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, denied := range []bool{false, true} {
				var spent []string
				deps := appKeyDeps(&fakeAppKeys{enabled: true})
				deps.AppKeys = &profileSetKeys{fakeAppKeys: &fakeAppKeys{enabled: true}, verifyErr: tc.verifyErr}
				sets := fakeProfileSets{}
				deps.ProfileSet = &sets
				deps.AccountRateLimit = func(_ context.Context, bucket string, accountID int) ratelimit.AllowResult {
					spent = append(spent, fmt.Sprintf("%s:%d", bucket, accountID))
					return ratelimit.AllowResult{Allowed: !denied, RetryAfter: 7 * time.Second}
				}
				rec := do(t, NewHandler(deps), http.MethodPut, Prefix+"/profiles/set", tc.body, tc.headers)
				want := []string(nil)
				if tc.spent {
					want = []string{fmt.Sprintf("profile_set:%d", tc.account)}
				}
				if !slices.Equal(spent, want) {
					t.Fatalf("budgets spent = %v, want %v", spent, want)
				}
				if !tc.spent {
					continue
				}
				if !denied {
					if rec.Code != http.StatusOK || sets.calls != 1 {
						t.Fatalf("with budget left: status %d, applied %d times: %s", rec.Code, sets.calls, rec.Body.String())
					}
					continue
				}
				requireProblem(t, rec, TypeRateLimited)
				if rec.Header().Get("Retry-After") != "7" || sets.calls != 0 {
					t.Fatalf("with the budget spent: Retry-After %q, applied %d times", rec.Header().Get("Retry-After"), sets.calls)
				}
			}
		})
	}
}

// TestGetProfileSetETag: an app polls the set with If-None-Match and gets
// 304 while it is unchanged.
func TestGetProfileSetETag(t *testing.T) {
	deps := appKeyDeps(&fakeAppKeys{})
	deps.ProfileSet = &fakeProfileSets{held: handlers.ProfileSetHeld{Version: 3, Items: []handlers.ProfileSetHeldItem{{SyncID: "0f8fad5b-d9cb-469f-a165-70867728950e", ID: "12", Name: "Sam"}}}}
	h := NewHandler(deps)
	first := do(t, h, http.MethodGet, Prefix+"/profiles/set", "", bearer(memberToken))
	tag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || tag == "" {
		t.Fatalf("first read = %d, ETag %q", first.Code, tag)
	}
	if again := do(t, h, http.MethodGet, Prefix+"/profiles/set", "", map[string]string{"Authorization": "Bearer " + memberToken, "If-None-Match": tag}); again.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match = %d, want 304", again.Code)
	}
	// A compressing intermediary rewrites the tag to its weak form; the
	// comparison is weak, so it still matches.
	if weak := do(t, h, http.MethodGet, Prefix+"/profiles/set", "", map[string]string{"Authorization": "Bearer " + memberToken, "If-None-Match": "W/" + tag}); weak.Code != http.StatusNotModified {
		t.Fatalf("weak If-None-Match = %d, want 304", weak.Code)
	}
	if other := do(t, h, http.MethodGet, Prefix+"/profiles/set", "", map[string]string{"Authorization": "Bearer " + memberToken, "If-None-Match": `"ps1-other"`}); other.Code != http.StatusOK {
		t.Fatalf("another If-None-Match = %d, want 200", other.Code)
	}
}
