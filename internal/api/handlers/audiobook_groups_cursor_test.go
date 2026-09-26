package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userdb"
)

func TestAudiobookGroupsCursorRejectsSQLiteStatistics(t *testing.T) {
	pool := userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir(), MaxOpen: 2})
	defer func() { _ = pool.Close() }()
	handler := &CatalogHandler{itemsH: &ItemsHandler{browseRepo: catalog.NewBrowseRepository(nil), storeProvider: userdb.NewSQLiteProvider(pool)}}
	_, err := handler.AudiobookGroups(t.Context(), ItemViewer{Access: catalog.AccessFilter{UserID: 17, ProfileID: "viewer"}}, catalog.AudiobookGroupsQuery{LibraryID: 1, GroupBy: catalog.AudiobookGroupByAuthor, CursorPaging: true})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != "catalog_storage_unsupported" {
		t.Fatalf("SQLite viewer stats must fail before querying PG: %v", err)
	}
}
