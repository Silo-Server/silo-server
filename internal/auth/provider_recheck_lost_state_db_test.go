package auth

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// An omitted refresh_state retains the stored token. Repeating a refusal of
// that same possibly-spent token must not turn uncertainty into revocation.
func TestProviderRecheckRepeatedUncertainRefusalKeepsStateDB(t *testing.T) {
	for _, status := range []pluginv1.CheckAccountStatus{
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED,
	} {
		t.Run(status.String(), func(t *testing.T) {
			env := newRecheckEnv(t, "lost_repeated", "spent-token")
			ctx := t.Context()
			sessionID, refresh := env.session(t, true)
			key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "uncertain", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET check_outcome_unknown = TRUE WHERE id = $1`, env.identityID); err != nil {
				t.Fatal(err)
			}
			env.checker.respond = answer(status, "")
			for range 2 {
				env.makeDue(t)
				pair, err := env.svc.Refresh(ctx, refresh)
				if err != nil {
					t.Fatalf("uncertain refusal revoked the session: %v", err)
				}
				refresh = pair.RefreshToken
				if got := env.identityState(t).LastCheckStatus; got != CheckStatusUnsupported {
					t.Fatalf("status = %q, want unsupported", got)
				}
				if !env.checkOutcomeUnknown(t) || env.storedToken(t) != "spent-token" {
					t.Fatal("unchanged state lost its uncertainty")
				}
				if env.sessionRow(t, sessionID).RevokedAt != nil {
					t.Fatal("uncertain refusal revoked a login session")
				}
				if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err != nil {
					t.Fatalf("uncertain refusal deleted an API key: %v", err)
				}
			}

			// A new interactive sign-in supplies current state and settles the
			// uncertainty, as does explicit state returned by a check.
			identity := env.identity("lost_repeated")
			identity.RefreshState = refreshState("fresh-token")
			if _, _, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: env.installationID, AutoProvision: true, Identity: identity}); err != nil {
				t.Fatal(err)
			}
			if env.checkOutcomeUnknown(t) || env.storedToken(t) != "fresh-token" {
				t.Fatal("interactive sign-in did not settle the lost state")
			}
		})
	}
}
