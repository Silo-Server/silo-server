package apiv2

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// oneFileFetcher answers one movie's single file.
type oneFileFetcher struct {
	files map[string][]*models.MediaFile
}

func (f oneFileFetcher) GetByContentID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return f.files[id], nil
}

func (oneFileFetcher) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

// watchWithAccess is the real watch reads with the test's catalog access.
type watchWithAccess struct {
	*handlers.ItemsHandler
	access *allowlistCatalogAccess
}

func (w watchWithAccess) ContextAccessFilter(ctx context.Context, opts handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error) {
	return w.access.ContextAccessFilter(ctx, opts)
}

// TestFilePathsHiddenFromAnotherAppDB: on an account that holds
// metadata_curation, the person's own sessions read the on-disk path and file
// name of an item's files, and a session another app's key opened reads
// neither, through the real session lookup and the real item reads: its
// permissions list says it holds no such permission, and what handlers decide
// from that permission themselves must agree. Regression: CanViewFilePaths
// read the account's permissions and ignored how the session was opened, so
// an app the person allowed read where their files are on disk.
func TestFilePathsHiddenFromAnotherAppDB(t *testing.T) {
	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	movie := "paths-movie-" + sfx
	fileName := "Secret Cut " + sfx + ".mkv"
	folderPath := "/srv/media/private-" + sfx
	files := oneFileFetcher{files: map[string][]*models.MediaFile{}}
	var itemsH *handlers.ItemsHandler
	d := newKeyRoutesDB(t, func(deps *Dependencies) {
		pool := viewerAccessTestPool(t)
		browseRepo, itemRepo := catalogpkg.NewBrowseRepository(pool), catalogpkg.NewItemRepository(pool)
		detail := catalogpkg.NewDetailService(itemRepo, catalogpkg.NewEpisodeRepository(pool), catalogpkg.NewSeasonRepository(pool), catalogpkg.NewPersonRepository(pool), files)
		itemsH = handlers.NewItemsHandler(browseRepo, itemRepo, nil, nil, nil, nil, nil, detail, nil)
		itemsH.UserRepo = auth.NewUserRepository(pool)
		access := &allowlistCatalogAccess{}
		deps.CatalogAccess = access
		deps.CatalogItems = handlers.NewCatalogResourceHandler(itemsH)
		deps.Watch = watchWithAccess{ItemsHandler: itemsH, access: access}
	})
	pool := d.pool
	library := seedLibrary(t, pool, "paths-"+sfx)
	t.Cleanup(func() { mustExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, movie) })
	mustExec(t, pool, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, 'movie', 'Paths', 'matched')`, movie)
	mustExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, movie, library)
	files.files[movie] = []*models.MediaFile{{ID: 1, ContentID: movie, MediaFolderID: library, FilePath: folderPath + "/" + fileName,
		Container: "mkv", Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", FileSize: 1000, Duration: 600, CreatedAt: time.Now()}}
	mustExec(t, pool, `UPDATE users SET permissions = '{metadata_curation}' WHERE id = $1`, d.userID)

	// The person's own device key opens a session too.
	public := make([]byte, 65)
	if _, err := rand.Read(public); err != nil {
		t.Fatal(err)
	}
	ownKey, ownSession := uuid.NewString(), uuid.NewString()
	mustExec(t, pool, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
		VALUES ($1::uuid, $2, $3, $4, 'device', 'Phone', $5)`, ownKey, d.userID, public, "key-"+ownKey, models.AppKeyEnrolledSession)
	mustExec(t, pool, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
		VALUES ($1, $2, 'test', $3, $4, $5::uuid)`, ownSession, d.userID, time.Now().Add(time.Hour), models.SessionOpenedAppKey, ownKey)
	d.claims["tok-own-key"] = &auth.Claims{UserID: d.userID, Role: "user", SessionID: ownSession, TokenType: auth.TokenTypeAccess}

	for _, path := range []string{"/catalog/items/" + movie, "/catalog/items/" + movie + "/versions", "/watch/" + movie} {
		for token, visible := range map[string]bool{"tok-phone": true, "tok-own-key": true, "tok-app": false} {
			rec := do(t, d.handler, http.MethodGet, Prefix+path, "", with(bearer(token), "X-Profile-Id", "p-owner"))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s: status %d: %s", token, path, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			named := strings.Contains(body, fileName) || strings.Contains(body, folderPath)
			if named != visible {
				t.Errorf("%s %s: file path or name shown = %v, want %v: %s", token, path, named, visible, body)
			}
		}
	}
}

// TestFilePathsFollowTheGrantedAppPermissionRule: handlers.CanViewFilePaths
// answers false for every session another app's key opened, because
// metadata_curation counts for such a session only while an operation it
// gates is open to it (permittedPermissions), and none is. The handlers
// package cannot read the decision table, so the two are kept in step here:
// opening a metadata curation operation to such a session lists the
// permission and fails this test until CanViewFilePaths follows.
func TestFilePathsFollowTheGrantedAppPermissionRule(t *testing.T) {
	deps := appKeyDeps(&fakeAppKeys{enabled: true})
	view := deps.Accounts.(fakeAccounts).users[1]
	view.PolicyPermissions = []string{string(auth.PermissionMetadataCuration)}
	deps.Accounts = fakeAccounts{users: map[int]handlers.UserView{1: view}}
	rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/account/me", "", bearer(grantedAppToken))
	var me Account
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &me) != nil {
		t.Fatalf("current user: %d %s", rec.Code, rec.Body.String())
	}
	if slices.Contains(me.Permissions, Permission(auth.PermissionMetadataCuration)) {
		t.Fatal("metadata_curation now counts for another app's session: make handlers.CanViewFilePaths follow the same rule")
	}
}
