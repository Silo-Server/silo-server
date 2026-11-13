package handlers

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestAccessGroupUpdateLibraryPolicyDB: an omitted library list keeps the
// group's libraries, null lifts the restriction, an empty list hides every
// library, and a populated one restricts to it, both as stored and as the
// members' resolved scope sees it.
func TestAccessGroupUpdateLibraryPolicyDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	f.exec(t, `UPDATE users SET email=username||'@example.invalid', password_hash='' WHERE id=$1`, f.account)
	store := access.NewGroupStore(f.pool)
	group, err := store.Create(t.Context(), access.CreateGroupInput{Name: fmt.Sprintf("Library policy %d", f.account)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Delete(context.WithoutCancel(t.Context()), group.ID); err != nil {
			t.Error(err)
		}
	})
	f.exec(t, `UPDATE users SET access_group_id=$2 WHERE id=$1`, f.account, group.ID)
	h := NewAccessGroupHandler(store)
	resolver := access.NewResolver(auth.NewUserRepository(f.pool), pgstore.NewPostgresProvider(f.pool), nil, store)
	executor := &catalog.QueryExecutor{Pool: f.pool}
	for _, tc := range []struct {
		name      string
		libraries *[]int
		want      []int
		count     int
	}{
		{"omitted", nil, []int{f.library}, 4},
		{"null", new([]int(nil)), nil, 5},
		{"empty", new([]int{}), []int{}, 0},
		{"populated", new([]int{f.hidden}), []int{f.hidden}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Update(t.Context(), group.ID, access.UpdateGroupInput{LibraryIDs: new([]int{f.library})}); err != nil {
				t.Fatal(err)
			}
			updated, err := h.UpdateAdminAccessGroup(t.Context(), group.ID, access.UpdateGroupInput{LibraryIDs: tc.libraries}, access.GroupPrecondition{Any: true})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if !reflect.DeepEqual(updated.LibraryIDs, tc.want) {
				t.Errorf("updated library_ids = %#v, want %#v", updated.LibraryIDs, tc.want)
			}
			var ids []int
			if err := f.pool.QueryRow(t.Context(), `SELECT library_ids FROM access_groups WHERE id=$1`, group.ID).Scan(&ids); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Errorf("stored library_ids = %#v, want %#v", ids, tc.want)
			}
			scope, err := resolver.Resolve(t.Context(), access.ResolveInput{UserID: f.account})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(scope.AllowedLibraryIDs, tc.want) || scope.LibrariesRestricted != (tc.want != nil) {
				t.Errorf("scope library_ids = %#v, restricted = %t; want %#v", scope.AllowedLibraryIDs, scope.LibrariesRestricted, tc.want)
			}
			// Restrict the query to this fixture's two synthetic libraries.
			count, err := executor.Count(t.Context(), catalog.QueryDefinition{LibraryIDs: []int{f.library, f.hidden}}, catalog.AccessFilter{AllowedLibraryIDs: scope.AllowedLibraryIDs})
			if err != nil {
				t.Fatal(err)
			}
			if count != tc.count {
				t.Errorf("visible synthetic titles = %d, want %d", count, tc.count)
			}
		})
	}
}
