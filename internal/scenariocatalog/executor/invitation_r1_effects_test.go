package executor

import (
	"bytes"
	"encoding/json"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const invitationR1AcceptedPassword = "fixture-invitee-password"
const invitationR1AcceptedEmail = "fixture-invitee@silo.example.test"

func invitationR1TokenCreates(id string) bool {
	return id == "inv_accept.ok" || id == "inv_accept.meaning" || id == "inv_accept.shape" || id == "inv_accept.single_use"
}
func invitationR1TokenChangedTable(id, table string) bool {
	return invitationR1TokenCreates(id) && (table == "users" || table == "profiles" || table == "sessions" || table == "invitations")
}

func assertInvitationR1Effects(t *testing.T, e *Env, id string, resp response, before, after map[string]json.RawMessage, dbStart, dbEnd, appStart, appEnd time.Time) {
	t.Helper()
	if !invitationR1TokenCreates(id) {
		return
	}
	type row = map[string]json.RawMessage
	rows := func(raw json.RawMessage) []row {
		t.Helper()
		var r []row
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	text := func(raw json.RawMessage) string {
		t.Helper()
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	encode := func(v any) json.RawMessage {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	equal := func(want, got row) {
		t.Helper()
		if len(want) != len(got) {
			t.Error("column count changed")
		}
		for k, v := range want {
			if !bytes.Equal(v, got[k]) {
				t.Errorf("unexpected invitation acceptance column effect: %s", k)
			}
		}
	}
	bounds := func(raw json.RawMessage, lo, hi time.Time) {
		t.Helper()
		v, err := time.Parse(time.RFC3339Nano, text(raw))
		if err != nil {
			t.Fatal(err)
		}
		if v.Before(lo) || v.After(hi) {
			t.Errorf("acceptance time %s outside [%s,%s]", v, lo, hi)
		}
	}
	find := func(rs []row, key string) row {
		t.Helper()
		for _, r := range rs {
			if string(r["id"]) == key {
				return r
			}
		}
		t.Fatal("required fixture row absent")
		return nil
	}
	added := func(table string) row {
		t.Helper()
		old, newRows := rows(before[table]), rows(after[table])
		known := map[string]row{}
		for _, r := range old {
			known[string(r["id"])] = r
		}
		var result row
		count := 0
		for _, r := range newRows {
			if prior, ok := known[string(r["id"])]; ok {
				equal(prior, r)
				delete(known, string(r["id"]))
			} else {
				result = r
				count++
			}
		}
		if len(known) != 0 || count != 1 || len(newRows) != len(old)+1 {
			t.Fatal("expected exactly one added row and all prior rows intact:", table)
		}
		return result
	}
	user := added("users")
	userID, err := strconv.Atoi(string(user["id"]))
	if err != nil || userID <= 0 {
		t.Fatal("invalid accepted account id")
	}
	wantUser := maps.Clone(find(rows(before["users"]), e.fixtures["member_user_id"]))
	// Existing member PIN setup increments these; a newly inserted account uses schema defaults.
	wantUser["admin_revision"] = encode(1)
	wantUser["access_policy_revision"] = encode(1)
	wantUser["id"] = user["id"]
	wantUser["username"] = encode(invitationR1AcceptedEmail)
	wantUser["email"] = encode(invitationR1AcceptedEmail)
	if err := bcrypt.CompareHashAndPassword([]byte(text(user["password_hash"])), []byte(invitationR1AcceptedPassword)); err != nil {
		t.Error("accepted account password hash does not verify")
	}
	for _, r := range rows(before["users"]) {
		if bytes.Equal(r["password_hash"], user["password_hash"]) {
			t.Error("accepted password reused an existing hash")
		}
	}
	wantUser["password_hash"] = user["password_hash"]
	for _, key := range []string{"created_at", "updated_at"} {
		bounds(user[key], dbStart, dbEnd)
		wantUser[key] = user[key]
	}
	equal(wantUser, user)
	profile := added("profiles")
	if _, err := uuid.Parse(text(profile["id"])); err != nil {
		t.Error("default profile id is not UUID")
	}
	wantProfile := maps.Clone(find(rows(before["profiles"]), string(encode(profilePrimary))))
	wantProfile["id"] = profile["id"]
	wantProfile["user_id"] = encode(userID)
	wantProfile["name"] = encode("Fixture-invitee")
	for _, key := range []string{"created_at", "updated_at"} {
		bounds(profile[key], appStart.Truncate(time.Second), appEnd)
		wantProfile[key] = profile[key]
	}
	equal(wantProfile, profile)
	session := added("sessions")
	sid := text(session["id"])
	if _, err := uuid.Parse(sid); err != nil {
		t.Error("accepted login session id is not UUID")
	}
	wantSession := maps.Clone(find(rows(before["sessions"]), string(encode(e.fixtures["member_session_id"]))))
	wantSession["id"] = session["id"]
	wantSession["user_id"] = encode(userID)
	wantSession["device_name"] = encode("silo-scenario-executor/1")
	bounds(session["created_at"], dbStart, dbEnd)
	wantSession["created_at"] = session["created_at"]
	bounds(session["expires_at"], appStart.Add(e.jwt.RefreshExpiry()).Truncate(time.Microsecond), appEnd.Add(e.jwt.RefreshExpiry()))
	wantSession["expires_at"] = session["expires_at"]
	equal(wantSession, session)
	oldInv, newInv := rows(before["invitations"]), rows(after["invitations"])
	if len(oldInv) != len(newInv) {
		t.Fatal("invitation count changed during acceptance")
	}
	matched := 0
	for _, old := range oldInv {
		got := find(newInv, string(old["id"]))
		want := maps.Clone(old)
		if string(old["id"]) == e.fixtures["invitation_pending_id"] {
			matched++
			for _, key := range []string{"accepted_at", "updated_at"} {
				bounds(got[key], dbStart, dbEnd)
				want[key] = got[key]
			}
			want["accepted_user_id"] = encode(userID)
		}
		equal(want, got)
	}
	if matched != 1 {
		t.Error("expected exactly one consumed invitation")
	}
	var payload struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Tokens  *struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			Expires int    `json:"expires_in"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(resp.Raw, &payload); err != nil {
		t.Fatal(err)
	}
	access, refresh, expires := payload.Access, payload.Refresh, payload.Expires
	if payload.Tokens != nil {
		access, refresh, expires = payload.Tokens.Access, payload.Tokens.Refresh, payload.Tokens.Expires
	}
	if expires != int(e.jwt.AccessExpiry().Seconds()) {
		t.Error("token response expiry differs from configured lifetime")
	}
	for _, token := range []struct {
		raw, kind string
		ttl       time.Duration
	}{{access, auth.TokenTypeAccess, e.jwt.AccessExpiry()}, {refresh, auth.TokenTypeRefresh, e.jwt.RefreshExpiry()}} {
		claims, err := e.jwt.ValidateToken(token.raw)
		if err != nil {
			t.Fatal("issued token signature or claims invalid")
		}
		if claims.UserID != userID || claims.SessionID != sid || claims.Role != "user" || claims.TokenType != token.kind || claims.ProfileID != "" || claims.ImpersonatorUserID != nil {
			t.Error("issued token identity differs from accepted account/session")
		}
		if claims.ExpiresAt == nil || claims.ExpiresAt.Before(appStart.Add(token.ttl).Truncate(time.Second)) || claims.ExpiresAt.After(appEnd.Add(token.ttl)) {
			t.Error("issued token lifetime outside acceptance bounds")
		}
	}
}
