package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUserLibrarySharedPolicyProjection(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TEMP TABLE media_folders (LIKE public.media_folders INCLUDING DEFAULTS);
 CREATE TEMP TABLE media_folder_paths (LIKE public.media_folder_paths INCLUDING DEFAULTS);
 INSERT INTO media_folders (id,type,name,enabled,sort_order) VALUES (1,'movies','First',true,20),(2,'tv','Second',true,10),(3,'movies','Disabled',false,0);
 INSERT INTO media_folder_paths(id,media_folder_id,path) VALUES (1,1,'private-storage-fixture');`)
	if err != nil {
		t.Fatal(err)
	}
	h := NewLibraryHandler(catalog.NewFolderRepository(pool), nil, nil, nil, nil)
	for _, tc := range []struct {
		name  string
		scope access.Scope
		ids   []int
	}{
		{"unrestricted", access.Scope{}, []int{2, 1}},
		{"restricted", access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{1, 3}}, []int{1}},
		{"empty", access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{}}, []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := access.SetScope(t.Context(), tc.scope)
			views, err := h.ListUserLibraries(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int, 0, len(views))
			for _, v := range views {
				ids = append(ids, v.ID)
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatal(ids, tc.ids)
			}
			rec := httptest.NewRecorder()
			h.HandleListUserLibraries(rec, httptest.NewRequest("GET", "/user/libraries", nil).WithContext(ctx))
			var bridge []UserLibraryView
			if err := json.Unmarshal(rec.Body.Bytes(), &bridge); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || !reflect.DeepEqual(views, bridge) {
				t.Fatal(rec.Code, rec.Body.String())
			}
		})
	}
}
