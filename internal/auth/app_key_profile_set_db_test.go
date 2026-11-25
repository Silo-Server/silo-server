package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestVerifyProfileSetDB: a signed profile set changes only the account its
// person key belongs to, only once, and never an administrator's account.
// It catches a set signed for one account applied to another, a replayed
// set undoing a later change (a PIN set back), the admin bypass, and an
// edits_seen that is not covered by the signature.
func TestVerifyProfileSetDB(t *testing.T) {
	env := newKeyEnv(t, "keyset")
	ctx := t.Context()
	user := env.localAccount(t, "keyset-user", models.RoleUser)
	admin := env.localAccount(t, "keyset-admin", models.RoleAdmin)
	person, _, _ := env.enroll(t, user)
	adminPerson, _, _ := env.enroll(t, admin)
	set := map[string]any{"version": 3, "items": []any{map[string]any{"pin": map[string]any{"set": "1234"}}}}
	request := func(signer testKey, accountID int, fields map[string]any) KeyRequest {
		all := map[string]any{"account_id": strconv.Itoa(accountID), "set": set}
		for k, v := range fields {
			all[k] = v
		}
		return env.request(t, message{purpose: appkeys.PurposeProfileSet, signer: signer, fields: all})
	}
	spent := func(t *testing.T, req KeyRequest) bool {
		t.Helper()
		_, err := env.signIn.VerifyProfileSet(ctx, req)
		return errors.Is(err, ErrKeyChallengeInvalid)
	}

	t.Run("valid", func(t *testing.T) {
		req := request(person, user.ID, map[string]any{"edits_seen": "token-1"})
		got, err := env.signIn.VerifyProfileSet(ctx, req)
		if err != nil || got.UserID != user.ID || got.EditsSeen != "token-1" || !strings.Contains(string(got.Set), `"1234"`) {
			t.Fatalf("authority = %+v, %v", got, err)
		}
		if !spent(t, req) {
			t.Fatal("replaying an accepted set was not refused as challenge_invalid")
		}
	})
	t.Run("another account", func(t *testing.T) {
		req := request(person, admin.ID, nil)
		if _, err := env.signIn.VerifyProfileSet(ctx, req); !errors.Is(err, ErrKeySignatureInvalid) {
			t.Fatalf("err = %v, want signature_invalid", err)
		}
		if !spent(t, req) {
			t.Fatal("the challenge was not spent")
		}
	})
	t.Run("admin account", func(t *testing.T) {
		req := request(adminPerson, admin.ID, nil)
		if _, err := env.signIn.VerifyProfileSet(ctx, req); !errors.Is(err, ErrProfileSetAdminAccount) {
			t.Fatalf("err = %v, want the admin refusal", err)
		}
		if !spent(t, req) {
			t.Fatal("the challenge was not spent")
		}
	})
	t.Run("edits_seen over 64 characters", func(t *testing.T) {
		_, err := env.signIn.VerifyProfileSet(ctx, request(person, user.ID, map[string]any{"edits_seen": strings.Repeat("e", 65)}))
		if appkeys.CodeOf(err) != appkeys.CodeFieldInvalid {
			t.Fatalf("err = %v, want a field refusal", err)
		}
	})
	t.Run("edits_seen changed after signing", func(t *testing.T) {
		req := request(person, user.ID, map[string]any{"edits_seen": "token-1"})
		raw, _ := base64.RawURLEncoding.DecodeString(req.Envelope.Payload)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		payload["edits_seen"] = "token-2"
		raw, _ = json.Marshal(payload)
		req.Envelope.Payload = base64.RawURLEncoding.EncodeToString(raw)
		if _, err := env.signIn.VerifyProfileSet(ctx, req); appkeys.CodeOf(err) != appkeys.CodeSignatureInvalid {
			t.Fatalf("err = %v, want signature_invalid", err)
		}
	})
	t.Run("revoked person key", func(t *testing.T) {
		req := request(person, user.ID, nil)
		if _, err := env.pool.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = NOW() WHERE user_id = $1`, user.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := env.signIn.VerifyProfileSet(ctx, req); !errors.Is(err, ErrKeySignatureInvalid) {
			t.Fatalf("err = %v, want signature_invalid", err)
		}
		if _, err := env.pool.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = NULL WHERE user_id = $1`, user.ID); err != nil {
			t.Fatal(err)
		}
		if spent(t, req) {
			t.Fatal("a revoked key spent the challenge")
		}
	})
}
