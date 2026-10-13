package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

func TestOAuthCompletionSerializesWithAccountDisableDB(t *testing.T) {
	for _, store := range []string{"postgres", "memory"} {
		t.Run(store, func(t *testing.T) {
			rig, node, code, userID := pendingOAuthCompletion(t, store, "disable")
			holder, collected := redeemWithAccountHeld(t, rig, node, code, userID)
			ctx := t.Context()
			if _, err := holder.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, userID); err != nil {
				t.Fatal(err)
			}
			if err := RevokeSignInsInTransaction(ctx, holder, userID); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			got := awaitOAuthCompletion(t, collected)
			if !errors.Is(got.err, ErrOAuthCompletionInvalid) || got.completion.SessionID != "" || got.completion.AccessToken != "" {
				t.Fatalf("disabled account redemption: session = %q, error = %v", got.completion.SessionID, got.err)
			}
			if active := rig.env.activeSessions(t, userID); active != 0 {
				t.Fatalf("redemption created %d sessions after account disable and revocation", active)
			}
		})
	}
}

func TestOAuthCompletionReturnsLockedAccountDB(t *testing.T) {
	for _, store := range []string{"postgres", "memory"} {
		t.Run(store, func(t *testing.T) {
			rig, node, code, userID := pendingOAuthCompletion(t, store, "snapshot")
			holder, collected := redeemWithAccountHeld(t, rig, node, code, userID)
			if _, err := holder.Exec(t.Context(), `UPDATE users SET role = $2, access_group_id = NULL, max_profiles = 9 WHERE id = $1`,
				userID, models.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := awaitOAuthCompletion(t, collected)
			if got.err != nil || got.completion.User == nil {
				t.Fatalf("redeem updated account: %v", got.err)
			}
			if got.completion.User.ID != userID || got.completion.User.Role != models.RoleAdmin || got.completion.User.MaxProfiles != 9 {
				t.Fatalf("redemption returned an account snapshot from before the locked update: id = %d, role = %q, max profiles = %d",
					got.completion.User.ID, got.completion.User.Role, got.completion.User.MaxProfiles)
			}
			claims, err := rig.svc.jwt.ValidateToken(got.completion.AccessToken)
			if err != nil || claims.Role != got.completion.User.Role {
				t.Fatalf("token does not carry the locked account role: %v", err)
			}
		})
	}
}

func TestOAuthCompletionAfterInstallationDisableDB(t *testing.T) {
	for _, store := range []string{"postgres", "memory"} {
		t.Run(store, func(t *testing.T) {
			rig, node, code, userID := pendingOAuthCompletion(t, store, "installation_disabled")
			if _, err := rig.env.pool.Exec(t.Context(), `UPDATE plugin_installations SET enabled = false WHERE id = $1`, rig.env.installationID); err != nil {
				t.Fatal(err)
			}
			if _, err := node.Complete(t.Context(), code, nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
				t.Fatalf("redemption after installation disable: %v", err)
			}
			if active := rig.env.activeSessions(t, userID); active != 0 {
				t.Fatalf("disabled installation accepted %d new sessions", active)
			}
			// A refusal leaves the code redeemable while it has not expired.
			if _, err := rig.env.pool.Exec(t.Context(), `UPDATE plugin_installations SET enabled = true WHERE id = $1`, rig.env.installationID); err != nil {
				t.Fatal(err)
			}
			completion, err := node.Complete(t.Context(), code, nativeVerifier, "")
			if err != nil || completion.SessionID == "" {
				t.Fatalf("redemption after installation re-enabled: %v", err)
			}
		})
	}
}

func TestOAuthCompletionAfterIdentityRemovalDB(t *testing.T) {
	for _, store := range []string{"postgres", "memory"} {
		for _, operation := range []string{"unlink", "uninstall"} {
			t.Run(store+"/"+operation, func(t *testing.T) {
				rig, node, code, userID := pendingOAuthCompletion(t, store, "identity_removed")
				ctx := t.Context()
				if operation == "unlink" {
					identity := rig.env.identityRow(t, rig.plugin.subject)
					if err := NewIdentityService(rig.env.pool).AdminUnlink(ctx, userID, identity.ID, 0, false); err != nil {
						t.Fatal(err)
					}
				} else if _, err := rig.env.pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, rig.env.installationID); err != nil {
					t.Fatal(err)
				}
				if _, err := node.Complete(ctx, code, nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
					t.Fatalf("redemption after %s: %v", operation, err)
				}
				if active := rig.env.activeSessions(t, userID); active != 0 {
					t.Fatalf("redemption after %s opened %d sessions", operation, active)
				}
			})
		}
	}
}

func TestOAuthCompletionSerializesWithInstallationChangesDB(t *testing.T) {
	for _, store := range []string{"postgres", "memory"} {
		for _, operation := range []string{"disable", "uninstall"} {
			t.Run(store+"/"+operation, func(t *testing.T) {
				rig, node, code, userID := pendingOAuthCompletion(t, store, "installation_changed")
				ctx := t.Context()
				holder, err := rig.env.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = holder.Rollback(context.WithoutCancel(ctx)) })
				var lockedInstallationID int
				if err := holder.QueryRow(ctx, `SELECT id FROM plugin_installations WHERE id = $1 FOR UPDATE`,
					rig.env.installationID).Scan(&lockedInstallationID); err != nil {
					t.Fatal(err)
				}
				collected := redeemWithAuthorityHeld(t, rig, node, code, holder)
				query := `UPDATE plugin_installations SET enabled = false WHERE id = $1`
				if operation == "uninstall" {
					query = `DELETE FROM plugin_installations WHERE id = $1`
				}
				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if _, err := holder.Exec(writeCtx, query, rig.env.installationID); err != nil {
					t.Fatalf("installation %s deadlocked against redemption: %v", operation, err)
				}
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				got := awaitOAuthCompletion(t, collected)
				if !errors.Is(got.err, ErrOAuthCompletionInvalid) || got.completion.SessionID != "" {
					t.Fatalf("redemption after concurrent %s: session = %q, error = %v", operation, got.completion.SessionID, got.err)
				}
				if active := rig.env.activeSessions(t, userID); active != 0 {
					t.Fatalf("redemption after concurrent %s created %d sessions", operation, active)
				}
			})
		}
	}
}

// Account deletion cascades to completion rows. Redemption must wait for
// the account before it holds the completion, or the two operations deadlock.
func TestPGOAuthCompletionSerializesWithAccountDeleteDB(t *testing.T) {
	rig, node, code, userID := pendingOAuthCompletion(t, "postgres", "delete")
	holder, collected := redeemWithAccountHeld(t, rig, node, code, userID)
	deleteCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := holder.Exec(deleteCtx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("account deletion blocked on a completion held by redemption: %v", err)
	}
	if err := holder.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := awaitOAuthCompletion(t, collected)
	if !errors.Is(got.err, ErrOAuthCompletionInvalid) || got.completion.SessionID != "" {
		t.Fatalf("deleted account redemption: session = %q, error = %v", got.completion.SessionID, got.err)
	}
	var completions, sessions int
	if err := rig.env.pool.QueryRow(t.Context(), `SELECT count(*) FROM oauth_completions WHERE code_hash = $1`,
		oauthCompletionCodeHash(code)).Scan(&completions); err != nil {
		t.Fatal(err)
	}
	if err := rig.env.pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, userID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if completions != 0 || sessions != 0 {
		t.Fatalf("account deletion left %d completions and %d sessions", completions, sessions)
	}
}

func pendingOAuthCompletion(t *testing.T, store, label string) (*oauthDBRig, *OAuthHandler, string, int) {
	t.Helper()
	rig := newOAuthDBRig(t)
	node := rig.node()
	if store == "memory" {
		node.deps.CompletionStore = NewInMemoryOAuthStore()
	}
	rig.plugin.subject = "https://id.example.test|" + rig.env.name("completion_"+label)
	code := rig.run(t, node, node, OAuthStartRequest{Native: true, CodeChallenge: nativeChallenge, AppState: label}).Query().Get("code")
	if code == "" {
		t.Fatal("callback issued no completion code")
	}
	identity := rig.env.identityRow(t, rig.plugin.subject)
	return rig, node, code, identity.UserID
}

type oauthCompletionResult struct {
	completion OAuthCompletion
	err        error
}

func redeemWithAccountHeld(t *testing.T, rig *oauthDBRig, node *OAuthHandler, code string, userID int) (pgx.Tx, <-chan oauthCompletionResult) {
	t.Helper()
	ctx := t.Context()
	holder, err := rig.env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.WithoutCancel(ctx)) })
	if _, err := lockUser(ctx, holder, userID); err != nil {
		t.Fatal(err)
	}
	return holder, redeemWithAuthorityHeld(t, rig, node, code, holder)
}

func redeemWithAuthorityHeld(t *testing.T, rig *oauthDBRig, node *OAuthHandler, code string, holder pgx.Tx) <-chan oauthCompletionResult {
	t.Helper()
	ctx := t.Context()
	var holderPID int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	collected := make(chan oauthCompletionResult, 1)
	go func() {
		completion, err := node.Complete(ctx, code, nativeVerifier, "")
		collected <- oauthCompletionResult{completion: completion, err: err}
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err := rig.env.pool.QueryRow(waitCtx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
			AND $1 = ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked); err != nil {
			t.Fatalf("waiting for redemption to block on the held authority: %v", err)
		}
		if blocked {
			return collected
		}
	}
}

func awaitOAuthCompletion(t *testing.T, collected <-chan oauthCompletionResult) oauthCompletionResult {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case result := <-collected:
		return result
	case <-waitCtx.Done():
		t.Fatal("completion redemption did not finish after the account transaction")
		return oauthCompletionResult{}
	}
}
