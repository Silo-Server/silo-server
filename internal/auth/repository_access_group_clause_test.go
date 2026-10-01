package auth

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestAccessGroupSetClause pins the exact SQL/argument shape produced for
// access_group_id, independent of a live database. It's a regression guard
// for the expr/$? removal: the CASE guards, the NULL-on-promotion write, and
// the default-group CTE all have to keep generating the same effective SQL
// the old generic-expr mechanism did.
func TestAccessGroupSetClause(t *testing.T) {
	admin := models.RoleAdmin
	user := "user"

	t.Run("promoting to admin clears the group directly", func(t *testing.T) {
		setClause, predicate, cte, args, nextArgIndex := accessGroupSetClause(
			models.UpdateUserInput{Role: &admin}, 3,
		)
		if setClause != "access_group_id = $3" {
			t.Fatalf("setClause = %q", setClause)
		}
		if predicate != "access_group_id IS DISTINCT FROM $3" {
			t.Fatalf("predicate = %q", predicate)
		}
		if cte != "" {
			t.Fatalf("cte = %q, want none", cte)
		}
		if len(args) != 1 || args[0] != (*int64)(nil) {
			t.Fatalf("args = %#v, want [nil]", args)
		}
		if nextArgIndex != 4 {
			t.Fatalf("nextArgIndex = %d, want 4", nextArgIndex)
		}
	})

	t.Run("demoting an admin without a group falls back to the default via a CTE, binding no placeholder", func(t *testing.T) {
		setClause, predicate, cte, args, nextArgIndex := accessGroupSetClause(
			models.UpdateUserInput{Role: &user}, 3,
		)
		wantExpr := "(CASE WHEN role = '" + models.RoleAdmin +
			"' THEN (SELECT id FROM default_group) ELSE access_group_id END)"
		if setClause != "access_group_id = "+wantExpr {
			t.Fatalf("setClause = %q", setClause)
		}
		if predicate != "access_group_id IS DISTINCT FROM "+wantExpr {
			t.Fatalf("predicate = %q", predicate)
		}
		if cte != "default_group AS (SELECT id FROM access_groups WHERE is_default)" {
			t.Fatalf("cte = %q", cte)
		}
		// The same alias appears in both setClause and predicate above, so
		// the default-group subselect is only ever written once in the CTE
		// text itself: Postgres materializes a multiply-referenced CTE once
		// instead of re-running it per appearance.
		if len(args) != 0 {
			t.Fatalf("args = %#v, want none (no placeholder consumed)", args)
		}
		if nextArgIndex != 3 {
			t.Fatalf("nextArgIndex = %d, want unchanged 3", nextArgIndex)
		}
	})

	t.Run("setting a group alone is guarded against a concurrent admin promotion", func(t *testing.T) {
		groupID := int64(42)
		setClause, predicate, cte, args, nextArgIndex := accessGroupSetClause(
			models.UpdateUserInput{AccessGroupID: models.SetValue(groupID)}, 5,
		)
		wantExpr := "(CASE WHEN role = '" + models.RoleAdmin + "' THEN NULL ELSE $5::bigint END)"
		if setClause != "access_group_id = "+wantExpr {
			t.Fatalf("setClause = %q", setClause)
		}
		if predicate != "access_group_id IS DISTINCT FROM "+wantExpr {
			t.Fatalf("predicate = %q", predicate)
		}
		if cte != "" {
			t.Fatalf("cte = %q, want none", cte)
		}
		if len(args) != 1 || *(args[0].(*int64)) != groupID {
			t.Fatalf("args = %#v, want [%d]", args, groupID)
		}
		if nextArgIndex != 6 {
			t.Fatalf("nextArgIndex = %d, want 6", nextArgIndex)
		}
	})

	t.Run("explicit null binds directly with no CASE", func(t *testing.T) {
		setClause, predicate, cte, args, nextArgIndex := accessGroupSetClause(
			models.UpdateUserInput{AccessGroupID: models.ClearValue[int64]()}, 2,
		)
		if setClause != "access_group_id = $2" {
			t.Fatalf("setClause = %q", setClause)
		}
		if predicate != "access_group_id IS DISTINCT FROM $2" {
			t.Fatalf("predicate = %q", predicate)
		}
		if cte != "" {
			t.Fatalf("cte = %q, want none", cte)
		}
		if len(args) != 1 || args[0] != (*int64)(nil) {
			t.Fatalf("args = %#v, want [nil]", args)
		}
		if nextArgIndex != 3 {
			t.Fatalf("nextArgIndex = %d, want 3", nextArgIndex)
		}
	})

	t.Run("untouched leaves the column alone", func(t *testing.T) {
		setClause, predicate, cte, args, nextArgIndex := accessGroupSetClause(
			models.UpdateUserInput{}, 7,
		)
		if setClause != "" || predicate != "" || cte != "" || args != nil {
			t.Fatalf("got (%q, %q, %q, %#v), want all empty", setClause, predicate, cte, args)
		}
		if nextArgIndex != 7 {
			t.Fatalf("nextArgIndex = %d, want unchanged 7", nextArgIndex)
		}
	})
}
