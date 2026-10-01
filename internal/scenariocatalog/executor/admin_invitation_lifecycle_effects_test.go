package executor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/invitations"
)

func assertInvitationLifecycleEffects(t *testing.T, e *Env, id string, resp response, before, after json.RawMessage, started, finished, applicationStarted, applicationFinished time.Time) {
	t.Helper()
	var oldRows, newRows []map[string]json.RawMessage
	if err := json.Unmarshal(before, &oldRows); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &newRows); err != nil {
		t.Fatal(err)
	}
	sending := strings.HasPrefix(id, "adm_inv_create.") || strings.HasPrefix(id, "adm_inv_resend.")
	supersedes := strings.HasPrefix(id, "adm_inv_resend.") || id == "adm_inv_create.supersedes"
	revokes := strings.HasPrefix(id, "adm_inv_revoke.") && id != "adm_inv_revoke.accepted"
	count := len(oldRows)
	if sending {
		count++
	}
	if len(newRows) != count {
		t.Fatal("unexpected invitation row count")
	}
	bounded := func(raw json.RawMessage, lower, upper time.Time) {
		t.Helper()
		var v time.Time
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		if v.Before(lower) || v.After(upper) {
			t.Errorf("invitation timestamp %s outside bounds [%s,%s]", v, lower, upper)
		}
	}
	equal := func(want, got map[string]json.RawMessage) {
		t.Helper()
		if len(want) != len(got) {
			t.Error("invitation column count changed")
		}
		for k, v := range want {
			if !bytes.Equal(v, got[k]) {
				t.Errorf("unexpected invitation column effect: %s", k)
			}
		}
	}
	var pending map[string]json.RawMessage
	matched := 0
	for i, old := range oldRows {
		want := maps.Clone(old)
		got := newRows[i]
		if string(old["id"]) == e.fixtures["invitation_pending_id"] {
			pending = old
			matched++
			if revokes || supersedes {
				bounded(got["revoked_at"], started, finished)
				bounded(got["updated_at"], started, finished)
				want["revoked_at"] = got["revoked_at"]
				want["updated_at"] = got["updated_at"]
			}
		}
		equal(want, got)
	}
	if matched != 1 {
		t.Fatal("expected exactly one seeded pending invitation")
	}
	if !sending {
		return
	}
	got := newRows[len(oldRows)]
	want := maps.Clone(pending)
	newID, err := strconv.ParseInt(string(got["id"]), 10, 64)
	if err != nil || newID <= 0 {
		t.Fatal("invalid new invitation id")
	}
	for _, old := range oldRows {
		if bytes.Equal(old["id"], got["id"]) {
			t.Fatal("reused invitation id")
		}
	}
	var delivery map[string]json.RawMessage
	if err := json.Unmarshal(resp.Raw, &delivery); err != nil {
		t.Fatal(err)
	}
	if value, ok := delivery["email_sent"]; ok {
		if string(value) != "false" {
			t.Error("synthetic SMTP must not report delivery")
		}
	} else if string(delivery["delivery_status"]) != `"not_configured"` {
		t.Error("synthetic SMTP status differs")
	}
	var body struct {
		ClaimURL   string `json:"claim_url"`
		Invitation struct {
			ID json.RawMessage `json:"id"`
		} `json:"invitation"`
	}
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatal(err)
	}
	wireID := strings.Trim(string(body.Invitation.ID), "\"")
	if wireID != strconv.FormatInt(newID, 10) {
		t.Error("response id differs from inserted row")
	}
	if !strings.HasPrefix(body.ClaimURL, publicURL+"/invite/") {
		t.Fatal("claim link base differs")
	}
	token := strings.TrimPrefix(body.ClaimURL, publicURL+"/invite/")
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || len(token) != 43 {
		t.Fatal("invalid fresh claim token")
	}
	hash, _ := json.Marshal(invitations.HashToken(token))
	want["token_hash"] = hash
	for _, old := range oldRows {
		if bytes.Equal(old["token_hash"], hash) {
			t.Error("claim token was reused")
		}
	}
	want["id"] = got["id"]
	for _, key := range []string{"created_at", "updated_at"} {
		bounded(got[key], started, finished)
		want[key] = got[key]
	}
	// Expiry is computed by the service clock; database-written times use database bounds.
	bounded(got["expires_at"], applicationStarted.Add(invitations.DefaultTTL).Truncate(time.Microsecond), applicationFinished.Add(invitations.DefaultTTL))
	want["expires_at"] = got["expires_at"]
	email, note := "fixture-guest@silo.example.test", "welcome"
	if supersedes {
		email = "fixture-invitee@silo.example.test"
		note = ""
	}
	if strings.HasPrefix(id, "adm_inv_resend.") {
		note = "fixture pending"
	}
	want["email"], _ = json.Marshal(email)
	want["note"], _ = json.Marshal(note)
	equal(want, got)
}
