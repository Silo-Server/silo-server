package executor

import (
	"bytes"
	"encoding/json"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func assertSignupEffects(t *testing.T, e *Env, id, transport string, resp response, before, after map[string]json.RawMessage, dbStart, dbEnd, appStart, appEnd time.Time) {
	t.Helper()
	username, email := "fixture-newcomer", "fixture-newcomer@silo.example.test"
	if strings.TrimSuffix(id, ".r1") == "signup.user_meaning" {
		username, email = "Fixture-Newcomer", "Fixture-Newcomer@Silo.Example.Test"
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
				t.Errorf("unexpected signup column effect: %s", k)
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
			t.Errorf("signup time %s outside [%s,%s]", v, lo, hi)
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
	wantUser["username"] = encode(username)
	wantUser["email"] = encode(email)
	if err := bcrypt.CompareHashAndPassword([]byte(text(user["password_hash"])), []byte("fixture-newcomer-password")); err != nil {
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
	profile := added("user_profiles")
	if _, err := uuid.Parse(text(profile["id"])); err != nil {
		t.Error("default profile id is not UUID")
	}
	wantProfile := maps.Clone(find(rows(before["user_profiles"]), string(encode(profilePrimary))))
	wantProfile["id"] = profile["id"]
	wantProfile["user_id"] = encode(userID)
	wantProfile["name"] = encode(username)
	for _, key := range []string{"created_at", "updated_at"} {
		bounds(profile[key], appStart.Truncate(time.Second), appEnd)
		wantProfile[key] = profile[key]
	}
	equal(wantProfile, profile)
	session := added("auth_sessions")
	sid := text(session["id"])
	if _, err := uuid.Parse(sid); err != nil {
		t.Error("accepted login session id is not UUID")
	}
	wantSession := maps.Clone(find(rows(before["auth_sessions"]), string(encode(e.fixtures["member_session_id"]))))
	wantSession["id"] = session["id"]
	wantSession["user_id"] = encode(userID)
	wantSession["device_name"] = encode("silo-scenario-executor/1")
	bounds(session["created_at"], dbStart, dbEnd)
	wantSession["created_at"] = session["created_at"]
	bounds(session["expires_at"], appStart.Add(e.jwt.RefreshExpiry()).Truncate(time.Microsecond), appEnd.Add(e.jwt.RefreshExpiry()))
	wantSession["expires_at"] = session["expires_at"]
	equal(wantSession, session)
	oldCodes, newCodes := rows(before["invite_codes"]), rows(after["invite_codes"])
	if len(oldCodes) != len(newCodes) {
		t.Fatal("signup changed code row count")
	}
	consumed := 0
	for _, old := range oldCodes {
		got := find(newCodes, string(old["id"]))
		want := maps.Clone(old)
		if text(old["code"]) == inviteCode {
			consumed++
			var count int
			if err := json.Unmarshal(old["use_count"], &count); err != nil {
				t.Fatal(err)
			}
			want["use_count"] = encode(count + 1)
			bounds(got["updated_at"], dbStart, dbEnd)
			want["updated_at"] = got["updated_at"]
		}
		equal(want, got)
	}
	if consumed != 1 {
		t.Error("expected exactly one consumed code")
	}
	var wire struct {
		User map[string]any `json:"user"`
	}
	if err := json.Unmarshal(resp.Raw, &wire); err != nil {
		t.Fatal("signup response JSON invalid")
	}
	wireID := any(float64(userID))
	if transport == "v2" {
		wireID = strconv.Itoa(userID)
	}
	if wire.User["id"] != wireID || wire.User["username"] != username || wire.User["email"] != email || wire.User["role"] != "user" || wire.User["download_allowed"] != true {
		t.Error("signup response differs from provisioned account")
	}
	if _, ok := wire.User["impersonation"]; ok {
		t.Error("signup must not claim impersonation")
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
