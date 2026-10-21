package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

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
		name  string
		body  string
		want  []int
		count int
	}{
		{"omitted", `{}`, []int{f.library}, 4},
		{"null", `{"library_ids":null}`, nil, 5},
		{"empty", `{"library_ids":[]}`, []int{}, 0},
		{"populated", fmt.Sprintf(`{"library_ids":[%d]}`, f.hidden), []int{f.hidden}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Update(t.Context(), group.ID, access.UpdateGroupInput{LibraryIDs: new([]int{f.library})}); err != nil {
				t.Fatal(err)
			}
			id := strconv.FormatInt(group.ID, 10)
			rec := httptest.NewRecorder()
			h.HandleUpdate(rec, accessGroupRequestWithID(http.MethodPut, "/api/v1/admin/access-groups/"+id, strings.NewReader(tc.body), id))
			if rec.Code != http.StatusOK {
				t.Fatalf("update status = %d, body %s", rec.Code, rec.Body.String())
			}
			if got := decodeAccessGroupResponse(t, rec).LibraryIDs; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("response library_ids = %#v, want %#v", got, tc.want)
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
