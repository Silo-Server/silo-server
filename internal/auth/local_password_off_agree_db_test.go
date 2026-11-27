package auth

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// For generated mixes of accounts (passwords, break-glass, roles, local and
// provider-bound keys, identities at providers that do and don't sign people
// in), turning local passwords off is refused naming exactly the accounts
// listAdminLocalPasswordOffAccounts puts in its blocking group, and allowed
// when that group is empty: the page and the refusal never disagree.
func TestLocalPasswordOffPageAndRefusalAgreeDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, config.ServerPublicURLSettingKey))
	env.setSetting(t, config.ServerPublicURLSettingKey, "")
	directory := env.riskProvider(t, "agree-directory", `["password"]`)
	oidc := env.riskProvider(t, "agree-oidc", `["oauth2"]`) // signs nobody in without a public URL
	// A usable break-glass admin, so the break-glass check passes.
	env.riskAccount(t, "agree-glass", models.RoleAdmin, true, true)
	identities := NewIdentityService(env.pool)
	rng := rand.New(rand.NewPCG(5433, 1))
	blockers := 0
	t.Cleanup(func() {
		if !t.Failed() && blockers == 0 {
			t.Error("no mix had an account that blocks: the mixes test nothing")
		}
	})
	for mix := range 6 {
		t.Run(fmt.Sprintf("mix %d", mix), func(t *testing.T) {
			var seeded []int
			for i := range 8 {
				label := fmt.Sprintf("agree-%d-%d", mix, i)
				role := []string{models.RoleUser, models.RoleAdmin}[rng.IntN(2)]
				pw := rng.IntN(2) == 0
				bg := role == models.RoleAdmin && rng.IntN(3) == 0
				id := env.riskAccount(t, label, role, pw, bg)
				seeded = append(seeded, id)
				var identity *int64
				switch rng.IntN(3) {
				case 1:
					identity = new(env.riskIdentity(t, id, directory, label+"-id"))
				case 2:
					identity = new(env.riskIdentity(t, id, oidc, label+"-id"))
				}
				switch rng.IntN(3) {
				case 1:
					env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
				case 2:
					if identity != nil {
						env.riskKey(t, id, models.DeviceKeyStatusActive, identity)
					}
				}
			}
			var listed []int
			for _, id := range seeded {
				if localPasswordOffGroupOf(t, identities, id) == LocalPasswordOffBlocking {
					listed = append(listed, id)
				}
			}
			err := CheckLocalLoginSettingChange(ctx, env.pool, LocalLoginSettings{LocalPasswordLogin: "true"}, LocalLoginSettings{LocalPasswordLogin: "false"})
			var named []int
			if risk, ok := errors.AsType[*AccountsAtRiskError](err); ok {
				for _, a := range risk.Accounts {
					if slices.Contains(seeded, a.ID) {
						named = append(named, a.ID)
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
			blockers += len(listed)
			slices.Sort(listed)
			slices.Sort(named)
			if !slices.Equal(listed, named) {
				t.Fatalf("blocking group %v, refusal names %v", listed, named)
			}
			// Out of the way of the next mix.
			if _, err := env.pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = ANY($1)`, seeded); err != nil {
				t.Fatal(err)
			}
		})
	}
}
