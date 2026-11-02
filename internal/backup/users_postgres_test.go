package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// libraryScope is a library page's section scope.
const libraryScope = "library"

func dbExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// testAccount inserts an account, deleted when the test ends.
func testAccount(t *testing.T, pool *pgxpool.Pool, username, hash string, group *int64, libraries []int) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO users (username, email, password_hash, role, permissions, library_ids, access_group_id)
		VALUES ($1::text, $1::text || '@example.test', $2, 'user', '{}', $3, $4) RETURNING id`, username, hash, libraries, group).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

func testProfile(t *testing.T, pool *pgxpool.Pool, userID int, id, name string, primary bool, pin string) {
	t.Helper()
	dbExec(t, pool, `INSERT INTO user_profiles (id, user_id, name, is_primary, pin_hash) VALUES ($1, $2, $3, $4, $5)`, id, userID, name, primary, pin)
}

func testGroup(t *testing.T, pool *pgxpool.Pool, name string, maxStreams int) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO access_groups (name, max_streams, download_transcode_allowed, configuration_revision) VALUES ($1, $2, false, 1) RETURNING id`,
		name, maxStreams).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM access_groups WHERE id = $1`, id) })
	return id
}

func testLibrary(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	folder, err := catalog.NewFolderRepository(pool).Create(t.Context(), catalog.CreateFolderInput{Paths: []string{dir}, Type: "movies", Name: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folder.ID) })
	return folder.ID
}

func testMovie(t *testing.T, pool *pgxpool.Pool, id, title, tmdb string, library int) {
	t.Helper()
	dbExec(t, pool, `INSERT INTO media_items (content_id, type, title, year, tmdb_id, status) VALUES ($1, 'movie', $2, 2001, $3, 'matched')`, id, title, tmdb)
	if library > 0 {
		dbExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, id) })
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (f *selectiveFixture) restoreUsers(t *testing.T, backupID, digest string, users []UserSelection) *SelectiveReport {
	t.Helper()
	payload, _ := json.Marshal(RestoreRequest{BackupID: backupID, Digest: digest, Users: users})
	result, err := f.svc.ExecuteRestore(t.Context(), RestoreRun{Payload: payload, Fenced: (&testFence{pool: f.pool}).run})
	if err != nil {
		t.Fatal(err)
	}
	return result.(*SelectiveReport)
}

func reportItem(t *testing.T, r *SelectiveReport, section, id string) ReportItem {
	t.Helper()
	for _, item := range r.Items {
		if item.Section == section && item.ID == id {
			return item
		}
	}
	t.Fatalf("report has no %s item %s: %+v", section, id, r.Items)
	return ReportItem{}
}

func allTypes() []string { return UserDataTypeIDs() }

// userSeed is the state TestUserRestoreDB backs up.
type userSeed struct {
	suffix                          string
	lib1, lib2                      int
	alice, bob, carol, dave, frank  int
	groupFamily, groupWide          int64
	movies                          [3]string
	unmarked                        string
	section                         string
	gone                            string
	collectionID                    string
	bobLocalProgressAt, aliceWatchT time.Time
}

func seedUsers(t *testing.T, f *selectiveFixture) userSeed {
	t.Helper()
	pool, ctx := f.pool, t.Context()
	s := userSeed{suffix: randomHex(t, 4)}
	// The restore creates accounts under the seed's usernames.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE username::text LIKE $1`, "%-"+s.suffix)
	})
	s.lib1, s.lib2 = testLibrary(t, pool, "users-a-"+s.suffix), testLibrary(t, pool, "users-b-"+s.suffix)
	s.movies = [3]string{"bkus-" + s.suffix + "-1", "bkus-" + s.suffix + "-2", "bkus-" + s.suffix + "-3"}
	testMovie(t, pool, s.movies[0], "First "+s.suffix, "", s.lib1)
	testMovie(t, pool, s.movies[1], "Second "+s.suffix, "", s.lib2)
	// In both libraries, so it stays visible to Alice when the second is gone.
	dbExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, s.movies[1], s.lib1)
	testMovie(t, pool, s.movies[2], "Third "+s.suffix, "tm3-"+s.suffix, s.lib1)
	s.unmarked = "bkus-" + s.suffix + "-unmarked"
	testMovie(t, pool, s.unmarked, "Unmarked "+s.suffix, "", s.lib1)
	s.gone = "bkus-" + s.suffix + "-gone"
	testMovie(t, pool, s.gone, "Gone "+s.suffix, "", 0)
	s.groupFamily = testGroup(t, pool, "family-"+s.suffix, 3)
	s.groupWide = testGroup(t, pool, "wide-"+s.suffix, 2)

	s.alice = testAccount(t, pool, "alice-"+s.suffix, "hash-alice", nil, []int{s.lib1, s.lib2})
	testProfile(t, pool, s.alice, "pa-main-"+s.suffix, "Main", true, "")
	testProfile(t, pool, s.alice, "pa-kids-"+s.suffix, "Kids", false, "pin-kids")
	dbExec(t, pool, `UPDATE user_profiles SET library_restrictions_enabled = true, subtitle_language = 'fr', avatar = $3 WHERE user_id = $1 AND id = $2`,
		s.alice, "pa-main-"+s.suffix, "upload:profile-avatars/"+strconv.Itoa(s.alice)+"/pa-main-"+s.suffix+"/original.webp")
	dbExec(t, pool, `UPDATE user_profiles SET library_restrictions_enabled = false WHERE user_id = $1 AND id = $2`, s.alice, "pa-main-"+s.suffix)
	dbExec(t, pool, `UPDATE user_profiles SET library_restrictions_enabled = true WHERE user_id = $1 AND id = $2`, s.alice, "pa-kids-"+s.suffix)
	dbExec(t, pool, `INSERT INTO user_profile_allowed_libraries (user_id, profile_id, library_id) VALUES ($1, $2, $3), ($1, $2, $4)`,
		s.alice, "pa-kids-"+s.suffix, s.lib1, s.lib2)
	avatarDir := "profile-avatars/" + strconv.Itoa(s.alice) + "/pa-main-" + s.suffix + "/"
	for name, data := range map[string]string{"original.webp": "avatar", "w256.webp": "avatar small"} {
		if err := f.store.Put(ctx, avatarDir+name, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	main, kids := "pa-main-"+s.suffix, "pa-kids-"+s.suffix
	s.aliceWatchT = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	dbExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 600, 6000, $4)`,
		s.alice, main, s.movies[0], s.aliceWatchT)
	for _, item := range []string{s.movies[0], s.gone} {
		dbExec(t, pool, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed, source) VALUES (gen_random_uuid()::text, $1, $2, $3, $4, true, 'playback')`,
			s.alice, main, item, s.aliceWatchT)
	}
	dbExec(t, pool, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed, source) VALUES (gen_random_uuid()::text, $1, $2, $3, $4, true, 'playback')`,
		s.alice, kids, s.movies[1], s.aliceWatchT)
	dbExec(t, pool, `INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating) VALUES ($1, $2, $3, 4)`, s.alice, main, s.movies[1])
	dbExec(t, pool, `INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, s.alice, main, s.movies[0])
	dbExec(t, pool, `INSERT INTO user_watchlist (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, s.alice, main, s.movies[1])
	dbExec(t, pool, `INSERT INTO user_not_interested (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, s.alice, main, s.movies[2])
	dbExec(t, pool, `INSERT INTO user_home_item_dismissals (user_id, profile_id, surface, media_item_id) VALUES ($1, $2, 'continue_watching', $3)`, s.alice, main, s.movies[1])
	dbExec(t, pool, `INSERT INTO user_setting_values (user_id, key, scope, profile_id, value) VALUES ($1, 'ui.disabled_library_ids', 'profile', $2, $3::jsonb)`,
		s.alice, main, "["+strconv.Itoa(s.lib2)+"]")
	dbExec(t, pool, `INSERT INTO notification_preferences (user_id, profile_id, enabled) VALUES ($1, $2, false)`, s.alice, main)
	s.collectionID = "pc-" + s.suffix
	poster := "user-collection-images/" + s.collectionID + "/poster/original.ab.jpg"
	dbExec(t, pool, `INSERT INTO user_personal_collections (id, user_id, profile_id, creator_profile_id, name, poster_url, native) VALUES ($1, $2, $3, $3, 'Faves', $4, true)`,
		s.collectionID, s.alice, main, poster)
	dbExec(t, pool, `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, position) VALUES ($1, $2, $3, 0), ($1, $2, $4, 1)`,
		s.alice, s.collectionID, s.movies[0], s.movies[2])
	if err := f.store.Put(ctx, poster, []byte("collection poster")); err != nil {
		t.Fatal(err)
	}
	seedUserAuthored(t, f, &s, main)

	s.bob = testAccount(t, pool, "bob-"+s.suffix, "hash-bob", nil, nil)
	testProfile(t, pool, s.bob, "pb-main-"+s.suffix, "Main", true, "pin-backup")
	testProfile(t, pool, s.bob, "pb-extra-"+s.suffix, "Extra", false, "")
	dbExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 100, 6000, $4)`,
		s.bob, "pb-main-"+s.suffix, s.movies[0], s.aliceWatchT)
	dbExec(t, pool, `INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating) VALUES ($1, $2, $3, 5)`, s.bob, "pb-main-"+s.suffix, s.movies[1])
	dbExec(t, pool, `INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, s.bob, "pb-main-"+s.suffix, s.movies[2])

	s.carol = testAccount(t, pool, "carol-"+s.suffix, "hash-carol", &s.groupFamily, nil)
	s.dave = testAccount(t, pool, "dave-"+s.suffix, "hash-dave", &s.groupWide, nil)
	s.frank = testAccount(t, pool, "frank-"+s.suffix, "hash-frank", nil, []int{})
	return s
}

// seedUserAuthored adds what Alice built herself: a collection group, a
// smart collection that names only the library that will be gone, a home
// layout change of an admin section and a library page change of the gone
// library, a watchlisted title outside the library, and a mark unwatched,
// which stores -infinity as its listing cutoff.
func seedUserAuthored(t *testing.T, f *selectiveFixture, s *userSeed, main string) {
	t.Helper()
	pool, ctx := f.pool, t.Context()
	dbExec(t, pool, `INSERT INTO user_collection_groups (user_id, id, label, title, name, slug) VALUES ($1, $2, 'shelf', 'Shelf', 'Shelf', $3)`,
		s.alice, "cg-"+s.suffix, "shelf-"+s.suffix)
	dbExec(t, pool, `UPDATE user_personal_collections SET group_id = $2 WHERE user_id = $1 AND id = $3`, s.alice, "cg-"+s.suffix, s.collectionID)
	dbExec(t, pool, `INSERT INTO user_personal_collections (id, user_id, profile_id, creator_profile_id, name, collection_type, query_definition, native)
		VALUES ($1, $2, $3, $3, 'Gone smart', 'smart', $4::jsonb, true)`, "pcs-"+s.suffix, s.alice, main, `{"library_ids":[`+strconv.Itoa(s.lib2)+`]}`)
	s.section = "ps-" + s.suffix
	dbExec(t, pool, `INSERT INTO page_sections (id, scope, section_type, title) VALUES ($1, 'home', 'recently_added', $2)`, s.section, "Recent "+s.suffix)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id = $1`, s.section) })
	home, _ := json.Marshal([]userstore.SectionOverride{{ID: "o1", ProfileID: main, Scope: "home", SectionID: s.section, Hidden: true}})
	library, _ := json.Marshal([]userstore.SectionOverride{{ID: "o2", ProfileID: main, Scope: libraryScope, LibraryID: strconv.Itoa(s.lib2), IsUserAdded: true, UserTitle: "Mine"}})
	dbExec(t, pool, `INSERT INTO user_settings (user_id, key, value) VALUES ($1, 'section_overrides:home:', $2), ($1, $3, $4)`,
		s.alice, string(home), "section_overrides:library:"+strconv.Itoa(s.lib2), string(library))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tmdb := 900000000 + int(time.Now().UnixNano()%1000000)
	if _, err := watchlist.AddSnapshotInTx(ctx, tx, s.alice, main, watchlist.Snapshot{MediaType: "movie", TMDBID: tmdb, Title: "Coming " + s.suffix}, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles WHERE tmdb_id = $1`, tmdb)
	})
	dbExec(t, pool, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed, source) VALUES (gen_random_uuid()::text, $1, $2, $3, $4, true, 'playback')`,
		s.alice, main, s.unmarked, s.aliceWatchT)
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, s.alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.(interface {
		ClearWatchedState(context.Context, string, []string, time.Time) error
	}).ClearWatchedState(ctx, main, []string{s.unmarked}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// loseUsers turns the test database into "another server" that lacks most
// of the seed: the accounts are gone, one library is gone, the wide group
// grants more than it did, and the third movie is in the
// catalog under another content id.
func (f *selectiveFixture) loseUsers(t *testing.T, s *userSeed, backupID string) {
	t.Helper()
	pool := f.pool
	dbExec(t, pool, `DELETE FROM users WHERE id = ANY($1)`, []int{s.alice, s.carol, s.dave, s.frank})
	dbExec(t, pool, `UPDATE access_groups SET max_streams = 0 WHERE id = $1`, s.groupWide)
	dbExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, s.lib2)
	dbExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{s.movies[2], s.gone})
	moved := s.movies[2] + "-here"
	testMovie(t, pool, moved, "Third "+s.suffix, "tm3-"+s.suffix, s.lib1)
	// Bob is still here and changed his password and PIN since; his local
	// progress is newer than the backup's and he rated the second movie.
	s.bobLocalProgressAt = s.aliceWatchT.Add(time.Hour)
	dbExec(t, pool, `UPDATE users SET password_hash = 'hash-bob-now' WHERE id = $1`, s.bob)
	dbExec(t, pool, `UPDATE user_profiles SET pin_hash = 'pin-now' WHERE user_id = $1 AND id = $2`, s.bob, "pb-main-"+s.suffix)
	dbExec(t, pool, `DELETE FROM user_profiles WHERE user_id = $1 AND id = $2`, s.bob, "pb-extra-"+s.suffix)
	dbExec(t, pool, `UPDATE user_watch_progress SET position_seconds = 900, updated_at = $3 WHERE user_id = $1 AND profile_id = $2`,
		s.bob, "pb-main-"+s.suffix, s.bobLocalProgressAt)
	dbExec(t, pool, `UPDATE user_ratings SET rating = 2 WHERE user_id = $1 AND profile_id = $2`, s.bob, "pb-main-"+s.suffix)
	f.moveToAnotherServer(t, backupID)
}

// moveToAnotherServer gives the database another server id and puts the
// backup where that server finds it, as a backup brought over from the
// original server would be.
func (f *selectiveFixture) moveToAnotherServer(t *testing.T, backupID string) {
	t.Helper()
	ctx := t.Context()
	from := folderPrefix(f.serverID, backupID)
	f.serverID = "elsewhere-" + randomHex(t, 4)
	objects, _, err := f.store.List(ctx, from, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects {
		body, _, err := f.store.Get(ctx, obj.Key)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.Put(ctx, folderPrefix(f.serverID, backupID)+strings.TrimPrefix(obj.Key, from), data); err != nil {
			t.Fatal(err)
		}
	}
}

// TestUserRestoreDB backs up accounts with profiles and every data type,
// then restores them on "another server" (the same database under another
// server id, missing most of it):
//
//   - preview: a new ungrouped account is selectable with its unmapped
//     library counted; an existing account is matched by username;
//   - restore: new accounts keep their password hash, overrides, NULL
//     versus empty library lists, and group by name, with unmapped
//     libraries dropped and the restriction flag kept; new profiles keep
//     their PIN, avatar (under the new ids), and access; every data type
//     lands with items remapped, matched by provider id, or reported by
//     title when not in the catalog;
//   - an existing account keeps its password and PIN, gains its missing
//     profile, and merges data by the rules: newer progress here wins, a
//     rating here is kept;
//   - an account in a same-named group that is wider here is left out at
//     restore time; a selection whose target changed since the preview is
//     left out;
//   - running the same restore again changes nothing.
func TestUserRestoreDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	s := seedUsers(t, f)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	f.loseUsers(t, &s, source.ID)
	preview, err := f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.Itoa
	bob := checkUsersPreview(t, preview, s)

	main, kids := "pa-main-"+s.suffix, "pa-kids-"+s.suffix
	selection := []UserSelection{
		{Account: id(s.alice), Target: TargetNew, Profiles: []ProfileSelection{
			{Profile: main, Target: TargetNew, DataTypes: allTypes()},
			{Profile: kids, Target: TargetNew, DataTypes: []string{userdata.WatchHistory}},
		}},
		{Account: id(s.bob), Target: id(s.bob), Profiles: []ProfileSelection{
			{Profile: "pb-main-" + s.suffix, Target: bob.Account.Profiles[0].Target, DataTypes: []string{userdata.WatchHistory, userdata.Ratings, userdata.Favorites}},
			{Profile: "pb-extra-" + s.suffix, Target: TargetNew},
		}},
		{Account: id(s.carol), Target: TargetNew},
		{Account: id(s.dave), Target: TargetNew},
		{Account: id(s.frank), Target: TargetNew},
	}
	report := f.restoreUsers(t, source.ID, preview.Digest, selection)
	aliceItem := reportItem(t, report, SectionUsers, id(s.alice))
	if aliceItem.Outcome != OutcomeRestored || aliceItem.Account == nil || !aliceItem.Account.Created {
		t.Fatalf("alice restored = %+v", aliceItem)
	}
	if got := reportItem(t, report, SectionUsers, id(s.dave)); got.Outcome != OutcomeLeftOut || got.Reason != ReasonPolicyWider {
		t.Fatalf("dave = %+v", got)
	}
	if !slices.Contains(aliceItem.Account.Unmatched, "Gone "+s.suffix+" (2001)") {
		t.Fatalf("unmatched = %v, want the gone movie by title", aliceItem.Account.Unmatched)
	}
	newAlice := aliceItem.Account.LocalID
	checkNewAccount(t, f, s, newAlice, report)
	checkExistingAccount(t, f, s, report)

	// A target that no longer matches the mapping is left out.
	carolNew := reportItem(t, report, SectionUsers, id(s.carol)).Account.LocalID
	stale := f.restoreUsers(t, source.ID, preview.Digest, []UserSelection{{Account: id(s.carol), Target: id(carolNew + 1000000)}})
	if got := reportItem(t, stale, SectionUsers, id(s.carol)); got.Outcome != OutcomeLeftOut || got.Reason != ReasonChanged {
		t.Fatalf("carol with a changed target = %+v", got)
	}

	counts := func() []int {
		return []int{
			countRows(t, f.pool, `SELECT count(*) FROM users WHERE username LIKE $1`, "%-"+s.suffix),
			countRows(t, f.pool, `SELECT count(*) FROM user_profiles WHERE user_id = ANY($1)`, []int{newAlice, s.bob}),
			countRows(t, f.pool, `SELECT count(*) FROM user_watch_history WHERE user_id = ANY($1)`, []int{newAlice, s.bob}),
			countRows(t, f.pool, `SELECT count(*) FROM user_personal_collection_items WHERE user_id = $1`, newAlice),
		}
	}
	before := counts()
	checkChangesNothing(t, f.restoreUsers(t, source.ID, preview.Digest, selection))
	if after := counts(); !slices.Equal(before, after) {
		t.Fatalf("restoring again changed row counts %v -> %v", before, after)
	}
}

// checkUsersPreview checks the preview after the loss and returns Bob's
// item.
func checkUsersPreview(t *testing.T, preview *RestorePreview, s userSeed) PreviewItem {
	t.Helper()
	id := strconv.Itoa
	alice := previewItem(t, preview, SectionUsers, id(s.alice))
	switch {
	case !alice.Selectable || alice.Account.Match != MatchNew || alice.Account.Target != TargetNew || alice.Account.LibrariesLeftOut != 1:
		t.Fatalf("alice = %+v %+v", alice, alice.Account)
	case len(alice.Account.Profiles) != 2 || alice.Account.Profiles[1].LibrariesLeftOut != 1 || !alice.Account.Profiles[1].HasPIN:
		t.Fatalf("alice's profiles = %+v", alice.Account.Profiles)
	}
	bob := previewItem(t, preview, SectionUsers, id(s.bob))
	if !bob.Selectable || bob.Account.Match != MatchLogin || bob.Account.Target != id(s.bob) {
		t.Fatalf("bob = %+v %+v", bob, bob.Account)
	}
	return bob
}

// checkChangesNothing requires a re-run's report to show nothing restored,
// failed, or changed.
func checkChangesNothing(t *testing.T, again *SelectiveReport) {
	t.Helper()
	for _, item := range again.Items {
		if item.Outcome == OutcomeRestored || item.Outcome == OutcomeFailed {
			t.Fatalf("restoring again: %s %s (%+v)", item.ID, item.Outcome, item.Account)
		}
		if item.Account == nil {
			continue
		}
		for _, p := range item.Account.Profiles {
			for _, d := range p.Data {
				if d.Changed() {
					t.Fatalf("restoring again changed %s of %s: %+v", d.Type, p.Label, d.Result)
				}
			}
		}
	}
}

func checkNewAccount(t *testing.T, f *selectiveFixture, s userSeed, newAlice int, report *SelectiveReport) {
	t.Helper()
	pool, ctx := f.pool, t.Context()
	var hash string
	var libraries []int
	var group *int64
	var owner bool
	if err := pool.QueryRow(ctx, `SELECT password_hash, library_ids, access_group_id, is_owner FROM users WHERE id = $1`, newAlice).Scan(&hash, &libraries, &group, &owner); err != nil {
		t.Fatal(err)
	}
	if newAlice == s.alice || hash != "hash-alice" || !slices.Equal(libraries, []int{s.lib1}) || group != nil || owner {
		t.Fatalf("restored alice: id %d hash %q libraries %v group %v owner %v", newAlice, hash, libraries, group, owner)
	}
	frank := reportItem(t, report, SectionUsers, strconv.Itoa(s.frank)).Account.LocalID
	var frankLibraries []int
	if err := pool.QueryRow(ctx, `SELECT library_ids FROM users WHERE id = $1`, frank).Scan(&frankLibraries); err != nil || frankLibraries == nil || len(frankLibraries) != 0 {
		t.Fatalf("frank's empty library list = %v (nil %v), %v; want empty, not NULL", frankLibraries, frankLibraries == nil, err)
	}
	carol := reportItem(t, report, SectionUsers, strconv.Itoa(s.carol)).Account.LocalID
	var carolGroup *int64
	if err := pool.QueryRow(ctx, `SELECT access_group_id FROM users WHERE id = $1`, carol).Scan(&carolGroup); err != nil || carolGroup == nil || *carolGroup != s.groupFamily {
		t.Fatalf("carol's group = %v, %v; want %d", carolGroup, err, s.groupFamily)
	}

	var mainID, mainAvatar, kidsPIN string
	var mainPrimary, kidsRestricted bool
	if err := pool.QueryRow(ctx, `SELECT id, avatar, is_primary FROM user_profiles WHERE user_id = $1 AND name = 'Main'`, newAlice).Scan(&mainID, &mainAvatar, &mainPrimary); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT pin_hash, library_restrictions_enabled FROM user_profiles WHERE user_id = $1 AND name = 'Kids'`, newAlice).Scan(&kidsPIN, &kidsRestricted); err != nil {
		t.Fatal(err)
	}
	kidsLibraries := countRows(t, pool, `SELECT count(*) FROM user_profile_allowed_libraries l JOIN user_profiles p ON p.user_id = l.user_id AND p.id = l.profile_id
		WHERE l.user_id = $1 AND p.name = 'Kids' AND l.library_id = $2`, newAlice, s.lib1)
	avatarKey := "profile-avatars/" + strconv.Itoa(newAlice) + "/" + mainID + "/original.webp"
	switch {
	case mainID == "pa-main-"+s.suffix || !mainPrimary || mainAvatar != uploadedAvatarPrefix+avatarKey:
		t.Fatalf("restored main profile %s primary %v avatar %q", mainID, mainPrimary, mainAvatar)
	case kidsPIN != "pin-kids" || !kidsRestricted || kidsLibraries != 1:
		t.Fatalf("restored kids profile pin %q restricted %v libraries %d", kidsPIN, kidsRestricted, kidsLibraries)
	}
	for _, key := range []string{avatarKey, "profile-avatars/" + strconv.Itoa(newAlice) + "/" + mainID + "/w256.webp"} {
		if _, err := f.store.Stat(ctx, key); err != nil {
			t.Fatalf("restored avatar file %s: %v", key, err)
		}
	}
	moved := s.movies[2] + "-here"
	checks := []struct {
		name string
		sql  string
		args []any
		want int
	}{
		{"progress", `SELECT count(*) FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3 AND position_seconds = 600`, []any{newAlice, mainID, s.movies[0]}, 1},
		{"history (the gone movie unmatched)", `SELECT count(*) FROM user_watch_history WHERE user_id = $1 AND profile_id = $2`, []any{newAlice, mainID}, 2},
		{"rating", `SELECT count(*) FROM user_ratings WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3 AND rating = 4`, []any{newAlice, mainID, s.movies[1]}, 1},
		{"favorite", `SELECT count(*) FROM user_favorites WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, []any{newAlice, mainID, s.movies[0]}, 1},
		{"watchlist", `SELECT count(*) FROM user_watchlist WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, []any{newAlice, mainID, s.movies[1]}, 1},
		{"not interested by provider id", `SELECT count(*) FROM user_not_interested WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, []any{newAlice, mainID, moved}, 1},
		{"dismissal", `SELECT count(*) FROM user_home_item_dismissals WHERE user_id = $1 AND profile_id = $2`, []any{newAlice, mainID}, 1},
		{"collection items", `SELECT count(*) FROM user_personal_collection_items i JOIN user_personal_collections c ON c.user_id = i.user_id AND c.id = i.collection_id
			WHERE c.user_id = $1 AND c.name = 'Faves' AND i.media_item_id = ANY($2)`, []any{newAlice, []string{s.movies[0], moved}}, 2},
		{"hidden libraries remapped", `SELECT count(*) FROM user_setting_values WHERE user_id = $1 AND profile_id = $2 AND key = 'ui.disabled_library_ids' AND value = '[]'::jsonb`, []any{newAlice, mainID}, 1},
		{"profile preferences", `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND id = $2 AND subtitle_language = 'fr'`, []any{newAlice, mainID}, 1},
		{"notification preferences", `SELECT count(*) FROM notification_preferences WHERE user_id = $1 AND profile_id = $2 AND NOT enabled`, []any{newAlice, mainID}, 1},
		{"unmarked hide with -infinity", `SELECT count(*) FROM user_history_hidden_items WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3 AND removed_before = '-infinity'`, []any{newAlice, mainID, s.unmarked}, 1},
		{"collection group", `SELECT count(*) FROM user_personal_collections c JOIN user_collection_groups g ON g.user_id = c.user_id AND g.id = c.group_id
			WHERE c.user_id = $1 AND c.name = 'Faves' AND g.slug = $2`, []any{newAlice, "shelf-" + s.suffix}, 1},
		{"smart collection of the gone library left out", `SELECT count(*) FROM user_personal_collections WHERE user_id = $1 AND name = 'Gone smart'`, []any{newAlice}, 0},
		{"home layout", `SELECT count(*) FROM user_settings WHERE user_id = $1 AND key = 'section_overrides:home:' AND value::jsonb @> $2::jsonb`,
			[]any{newAlice, `[{"SectionID":"` + s.section + `","Hidden":true,"ProfileID":"` + mainID + `"}]`}, 1},
		{"library layout of the gone library left out", `SELECT count(*) FROM user_settings WHERE user_id = $1 AND key LIKE 'section_overrides:library:%'`, []any{newAlice}, 0},
		{"watchlist title", `SELECT count(*) FROM user_watchlist_titles e JOIN watchlist_titles w ON w.id = e.title_id WHERE e.user_id = $1 AND e.profile_id = $2 AND w.title = $3`,
			[]any{newAlice, mainID, "Coming " + s.suffix}, 1},
		{"kids history only", `SELECT count(*) FROM user_watch_history h JOIN user_profiles p ON p.user_id = h.user_id AND p.id = h.profile_id WHERE h.user_id = $1 AND p.name = 'Kids'`, []any{newAlice}, 1},
	}
	for _, c := range checks {
		if got := countRows(t, pool, c.sql, c.args...); got != c.want {
			t.Fatalf("%s: %d rows, want %d", c.name, got, c.want)
		}
	}
	var poster, collectionID string
	if err := pool.QueryRow(ctx, `SELECT id, poster_url FROM user_personal_collections WHERE user_id = $1 AND name = 'Faves'`, newAlice).Scan(&collectionID, &poster); err != nil {
		t.Fatal(err)
	}
	if collectionID == s.collectionID || poster != "user-collection-images/"+collectionID+"/poster/original.ab.jpg" {
		t.Fatalf("restored collection %s poster %q", collectionID, poster)
	}
	if _, err := f.store.Stat(ctx, poster); err != nil {
		t.Fatalf("restored collection poster: %v", err)
	}
}

func checkExistingAccount(t *testing.T, f *selectiveFixture, s userSeed, report *SelectiveReport) {
	t.Helper()
	pool, ctx := f.pool, t.Context()
	item := reportItem(t, report, SectionUsers, strconv.Itoa(s.bob))
	if item.Outcome != OutcomeRestored || item.Account.Created || len(item.Account.Profiles) != 2 || !item.Account.Profiles[1].Created {
		t.Fatalf("bob = %+v %+v", item, item.Account)
	}
	var hash, pin string
	if err := pool.QueryRow(ctx, `SELECT u.password_hash, p.pin_hash FROM users u JOIN user_profiles p ON p.user_id = u.id AND p.id = $2 WHERE u.id = $1`,
		s.bob, "pb-main-"+s.suffix).Scan(&hash, &pin); err != nil {
		t.Fatal(err)
	}
	var position float64
	var rating int
	if err := pool.QueryRow(ctx, `SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2`, s.bob, "pb-main-"+s.suffix).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT rating FROM user_ratings WHERE user_id = $1 AND profile_id = $2`, s.bob, "pb-main-"+s.suffix).Scan(&rating); err != nil {
		t.Fatal(err)
	}
	switch {
	case hash != "hash-bob-now" || pin != "pin-now":
		t.Fatalf("existing account changed: hash %q pin %q", hash, pin)
	case position != 900 || rating != 2:
		t.Fatalf("merge replaced newer local data: progress %v rating %d", position, rating)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM user_favorites WHERE user_id = $1 AND media_item_id = $2`, s.bob, s.movies[2]+"-here"); n != 1 {
		t.Fatalf("bob's favorite matched by provider id: %d rows", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND name = 'Extra' AND id <> $2`, s.bob, "pb-extra-"+s.suffix); n != 1 {
		t.Fatalf("bob's missing profile restored: %d", n)
	}
}

// matchCase is one backup account the planner matches.
type matchCase struct {
	name      string
	doc       UserDocument
	match     string
	local     int
	reason    string
	differing string
}

// matchAccount builds an ungrouped account entry with its effective policy.
func matchAccount(username, email string) AccountEntry {
	a := AccountEntry{ID: 900000 + len(username), Username: username, Email: email, Role: "user", Permissions: []string{}}
	a.Effective = effectivePolicyOf(access.ApplyGroupPolicy(a.user(), nil))
	return a
}

// groupedDoc is an account in the backup's group name with a stream cap.
func groupedDoc(name, sfx string, streams int) UserDocument {
	a := matchAccount("g-"+name+"-"+sfx, "")
	g := GroupPolicy{MaxStreams: streams, DownloadAllowed: true, TranscodeAllowed: true, AudioTranscodeAllowed: true, MaxProfiles: 5, RequestsAllowed: true}
	a.Group = &GroupEntry{Name: name, Policy: g}
	gp := g.access()
	a.Effective = effectivePolicyOf(access.ApplyGroupPolicy(a.user(), &gp))
	return UserDocument{Account: a}
}

// sharedSignIn gives one account's email the other's username: two accounts
// with one sign-in, as accounts from before sign-ins were unique can be. The
// sign-in trigger refuses that today, so it is bypassed for this one write.
func sharedSignIn(t *testing.T, pool *pgxpool.Pool, account int, email string) {
	t.Helper()
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, sql := range []string{`SET session_replication_role = replica`, `UPDATE users SET email = '` + email + `' WHERE id = ` + strconv.Itoa(account), `RESET session_replication_role`} {
		if _, err := conn.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}

func checkMatch(t *testing.T, p accountPlan, c matchCase) {
	t.Helper()
	acct := p.item.Account
	if c.reason != "" {
		if p.item.Selectable || p.item.Reason != c.reason {
			t.Fatalf("plan = %+v, want unselectable %s", p.item, c.reason)
		}
		if c.differing != "" && (len(acct.PolicyDifferences) == 0 || acct.PolicyDifferences[0].Field != c.differing) {
			t.Fatalf("differences = %+v, want %s", acct.PolicyDifferences, c.differing)
		}
		return
	}
	if !p.item.Selectable || acct.Match != c.match || p.localID != c.local {
		t.Fatalf("plan = %+v %+v local %d, want %s %d", p.item, acct, p.localID, c.match, c.local)
	}
	if c.doc.Account.Role == "admin" && !acct.Admin {
		t.Fatal("an administrator account is not marked")
	}
}

// TestUserMatchingDB checks the users planner against accounts and groups
// in the database. Every automatic match needs exactly one candidate, and a
// new account is selectable only when this server's policy for it grants
// nothing the backup's did not.
func TestUserMatchingDB(t *testing.T) {
	pool, _ := testDB(t)
	sfx := randomHex(t, 4)
	lib := testLibrary(t, pool, "match-"+sfx)
	alice := testAccount(t, pool, "Alice-"+sfx, "h", nil, nil)
	bob := testAccount(t, pool, "bob-"+sfx, "h", nil, nil)
	carol := testAccount(t, pool, "carol-"+sfx, "h", nil, nil)
	testAccount(t, pool, "dan-"+sfx, "h", nil, nil)
	sharedSignIn(t, pool, carol, "DAN-"+sfx)
	testProfile(t, pool, alice, "a1-"+sfx, "Kids", true, "")
	testProfile(t, pool, alice, "a2-"+sfx, "kids", false, "")
	testProfile(t, pool, alice, "a3-"+sfx, "Main", false, "")
	testGroup(t, pool, "family-"+sfx, 2)
	testGroup(t, pool, "open-"+sfx, 0)

	planner := &userPlanner{q: pool, sourceID: "other-" + sfx, libMap: map[int]int{7: lib}, userStore: true}
	plan := func(doc UserDocument) accountPlan {
		t.Helper()
		p, err := planner.plan(t.Context(), &doc)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	capped := matchAccount("capped-"+sfx, "")
	capped.Effective.MaxStreams = 2 // the backup's group capped it; no group here does
	admin := matchAccount("root-"+sfx, "")
	admin.Role = "admin"
	admin.Effective = effectivePolicyOf(access.ApplyGroupPolicy(admin.user(), nil))
	for _, c := range []matchCase{
		{name: "username case-insensitively", doc: UserDocument{Account: matchAccount("alice-"+sfx, "")}, match: MatchLogin, local: alice},
		{name: "email alone", doc: UserDocument{Account: matchAccount("someone-"+sfx, "BOB-"+sfx+"@example.test")}, match: MatchLogin, local: bob},
		{name: "username and email disagree", doc: UserDocument{Account: matchAccount("alice-"+sfx, "bob-"+sfx+"@example.test")}, reason: ReasonAmbiguousAccount},
		{name: "one sign-in, two accounts", doc: UserDocument{Account: matchAccount("dan-"+sfx, "")}, reason: ReasonAmbiguousAccount},
		{name: "nobody", doc: UserDocument{Account: matchAccount("nobody-"+sfx, "nobody-"+sfx+"@example.test")}, match: MatchNew},
		{name: "ungrouped, wider here", doc: UserDocument{Account: capped}, reason: ReasonPolicyWider, differing: "max_streams"},
		{name: "same-named group, same policy", doc: groupedDoc("family-"+sfx, sfx, 2), match: MatchNew},
		{name: "same-named group, wider", doc: groupedDoc("open-"+sfx, sfx, 2), reason: ReasonPolicyWider, differing: "max_streams"},
		{name: "group missing here", doc: groupedDoc("missing-"+sfx, sfx, 2), reason: ReasonGroupNotOnThisServer},
		{name: "administrator", doc: UserDocument{Account: admin}, match: MatchNew},
	} {
		t.Run(c.name, func(t *testing.T) { checkMatch(t, plan(c.doc), c) })
	}

	t.Run("library lists keep null and empty apart and never widen", func(t *testing.T) {
		for _, c := range []struct {
			in      []int
			want    []int
			dropped int
		}{{nil, nil, 0}, {[]int{}, []int{}, 0}, {[]int{7, 8}, []int{lib}, 1}, {[]int{8}, []int{}, 1}} {
			got, dropped := mapLibraries(c.in, planner.libMap)
			if (got == nil) != (c.want == nil) || !slices.Equal(got, c.want) || dropped != c.dropped {
				t.Fatalf("mapLibraries(%v) = %v (nil %v), %d; want %v (nil %v), %d", c.in, got, got == nil, dropped, c.want, c.want == nil, c.dropped)
			}
		}
	})

	t.Run("profiles match by name within the account, one candidate each", func(t *testing.T) {
		p := plan(UserDocument{Account: matchAccount("alice-"+sfx, ""), Profiles: []ProfileEntry{
			{ID: "p1", Name: "KIDS"}, {ID: "p2", Name: "main"}, {ID: "p3", Name: "Main"}, {ID: "p4", Name: "New"},
		}})
		got := map[string]PreviewProfile{}
		for _, pp := range p.item.Account.Profiles {
			got[pp.ID] = pp
		}
		switch {
		case got["p1"].Selectable || got["p1"].Reason != ReasonAmbiguousProfile:
			t.Fatalf("two profiles here named kids: %+v", got["p1"])
		case got["p2"].Selectable || got["p3"].Selectable || got["p2"].Reason != ReasonAmbiguousProfile:
			t.Fatalf("two backup profiles named main: %+v %+v", got["p2"], got["p3"])
		case !got["p4"].Selectable || got["p4"].Match != MatchNew:
			t.Fatalf("a new profile: %+v", got["p4"])
		}
	})

}

// TestUserTablesCoveredDB: every table with a user_id or profile_id column
// is claimed by a data type or the account and profile fields, or excluded
// with a reason, and nothing is listed twice or names a table that is gone.
// A new per-user table fails here until someone decides whether restores
// carry it.
func TestUserTablesCoveredDB(t *testing.T) {
	pool, _ := testDB(t)
	tables, err := collectStringsArgs(t.Context(), pool, `
		SELECT DISTINCT c.table_name FROM information_schema.columns c
		JOIN pg_class r ON r.relname = c.table_name AND r.relkind IN ('r', 'p')
		JOIN pg_namespace n ON n.oid = r.relnamespace AND n.nspname = c.table_schema
		WHERE c.table_schema = 'public' AND c.column_name IN ('user_id', 'profile_id') AND NOT r.relispartition
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	owner := map[string]string{}
	claim := func(table, by string) {
		if prev, ok := owner[table]; ok {
			t.Errorf("%s is claimed by %s and %s", table, prev, by)
		}
		owner[table] = by
	}
	for _, dt := range userDataTypes {
		for _, table := range dt.Tables() {
			claim(table, "data type "+dt.ID())
		}
	}
	for _, table := range accountFieldTables {
		claim(table, "account and profile fields")
	}
	for table, reason := range excludedUserTables {
		if reason == "" {
			t.Errorf("%s is excluded without a reason", table)
		}
		claim(table, "exclusions")
	}
	present := map[string]bool{"users": true} // the accounts themselves, keyed by id
	for _, table := range tables {
		present[table] = true
		if _, ok := owner[table]; !ok {
			t.Errorf("%s has a user_id or profile_id column but no data type claims it and it is not excluded", table)
		}
	}
	for table, by := range owner {
		if !present[table] {
			t.Errorf("%s (%s) is not a per-user table of this schema", table, by)
		}
	}
}

// TestProfileTransferDB exports one profile and imports it into others of
// the same account, as the signed-in profile does with its own file:
//
//   - the import merges into the target profile only, by the restore's
//     rules, and importing again changes nothing;
//   - a restricted profile gains nothing it cannot see: items outside its
//     libraries and preferences of those libraries are left out.
func TestProfileTransferDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx := t.Context()
	sfx := randomHex(t, 4)
	lib1, lib2 := testLibrary(t, pool, "pt-a-"+sfx), testLibrary(t, pool, "pt-b-"+sfx)
	m1, m2, m3 := "bkpt-"+sfx+"-1", "bkpt-"+sfx+"-2", "bkpt-"+sfx+"-3"
	testMovie(t, pool, m1, "One "+sfx, "", lib1)
	testMovie(t, pool, m2, "Two "+sfx, "", lib2)
	// Rated above the child profile's ceiling, in a library it may see.
	testMovie(t, pool, m3, "Three "+sfx, "", lib1)
	dbExec(t, pool, `UPDATE media_items SET content_rating = 'G', content_rating_age = 0 WHERE content_id = ANY($1)`, []string{m1, m2})
	dbExec(t, pool, `UPDATE media_items SET content_rating = 'R', content_rating_age = 17 WHERE content_id = $1`, m3)
	user := testAccount(t, pool, "pt-"+sfx, "h", nil, nil)
	other := testAccount(t, pool, "pt-other-"+sfx, "h", nil, nil)
	for _, p := range []string{"src", "dst", "kid"} {
		testProfile(t, pool, user, p+"-"+sfx, p, p == "src", "")
	}
	testProfile(t, pool, other, "o-"+sfx, "o", true, "")
	src, dst, kid := "src-"+sfx, "dst-"+sfx, "kid-"+sfx
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, item := range []string{m1, m2, m3} {
		dbExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 60, 600, $4)`, user, src, item, at)
		dbExec(t, pool, `INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, user, src, item)
	}
	dbExec(t, pool, `INSERT INTO user_library_playback_preferences (user_id, profile_id, library_id, audio_language) VALUES ($1, $2, $3, 'de'), ($1, $2, $4, 'fr')`, user, src, lib1, lib2)

	transfer := NewProfileTransfer(pool, func(context.Context) (string, error) { return "pt-server-" + sfx, nil })
	data, err := transfer.Export(ctx, ProfileImportScope{UserID: user, ProfileID: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.Export(ctx, ProfileImportScope{UserID: other, ProfileID: src}); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("exporting another account's profile = %v, want not found", err)
	}
	for _, field := range []string{`"password_hash"`, `"pin_hash"`, `"allowed_library_ids"`, `"username"`} {
		if strings.Contains(string(data), field) {
			t.Fatalf("the export carries %s", field)
		}
	}

	res, err := transfer.Import(ctx, ProfileImportScope{UserID: user, ProfileID: dst}, data)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SameServer || countRows(t, pool, `SELECT count(*) FROM user_favorites WHERE user_id = $1 AND profile_id = $2`, user, dst) != 3 ||
		countRows(t, pool, `SELECT count(*) FROM user_library_playback_preferences WHERE user_id = $1 AND profile_id = $2`, user, dst) != 2 {
		t.Fatalf("import into dst = %+v", res)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM user_favorites WHERE user_id = $1 AND profile_id <> ALL($2)`, user, []string{src, dst}); n != 0 {
		t.Fatalf("the import wrote to %d rows of other profiles", n)
	}
	again, err := transfer.Import(ctx, ProfileImportScope{UserID: user, ProfileID: dst}, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range again.Data {
		if d.Changed() {
			t.Fatalf("importing again changed %s: %+v", d.Type, d.Result)
		}
	}

	res, err = transfer.Import(ctx, ProfileImportScope{UserID: user, ProfileID: kid, AllowedLibraryIDs: []int{lib1},
		Maturity: access.MaturityLimits{MaxContentRating: "PG"}}, data)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case countRows(t, pool, `SELECT count(*) FROM user_favorites WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, user, kid, m2) != 0,
		countRows(t, pool, `SELECT count(*) FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, user, kid, m2) != 0:
		t.Fatal("a restricted profile gained an item outside its libraries")
	case countRows(t, pool, `SELECT count(*) FROM user_favorites WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, user, kid, m3) != 0:
		t.Fatal("a child profile gained an item above its content-rating ceiling")
	case countRows(t, pool, `SELECT count(*) FROM user_favorites WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, user, kid, m1) != 1:
		t.Fatal("a restricted profile lost an item it can see")
	case countRows(t, pool, `SELECT count(*) FROM user_library_playback_preferences WHERE user_id = $1 AND profile_id = $2 AND library_id = $3`, user, kid, lib2) != 0:
		t.Fatal("a restricted profile gained a preference for a library it cannot see")
	case len(res.Hidden) != 2 || res.Hidden[0] != "Three "+sfx+" (2001)" || res.Hidden[1] != "Two "+sfx+" (2001)":
		t.Fatalf("hidden = %v", res.Hidden)
	}
	if _, err := transfer.Import(ctx, ProfileImportScope{UserID: other, ProfileID: dst}, data); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("importing into another account's profile = %v, want not found", err)
	}
	checkRestrictedExport(t, transfer, ProfileImportScope{UserID: user, ProfileID: src, AllowedLibraryIDs: []int{lib1},
		Maturity: access.MaturityLimits{MaxContentRating: "PG"}}, m1, []string{m2, "Two " + sfx, m3, "Three " + sfx})
}

// checkRestrictedExport: a profile exports its data as it may see it now,
// so titles outside its libraries or above its ceiling, and their names,
// are not in the file.
func checkRestrictedExport(t *testing.T, transfer *ProfileTransfer, scope ProfileImportScope, visible string, hidden []string) {
	t.Helper()
	data, err := transfer.Export(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), visible) {
		t.Fatalf("a restricted export lost %s, which the profile can see", visible)
	}
	for _, s := range hidden {
		if strings.Contains(string(data), s) {
			t.Fatalf("a restricted export carries %q, which the profile cannot see", s)
		}
	}
}

// TestUserRestoreSameServerDB restores a deleted profile and lost progress
// on the server that took the backup: the account and its remaining profile
// match by id, the deleted profile comes back under a new id with its
// progress, and running the restore again maps the backup profile to that
// new profile instead of creating it twice.
func TestUserRestoreSameServerDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	sfx := randomHex(t, 4)
	movie, mature := "bkss-"+sfx, "bkss-"+sfx+"-r"
	testMovie(t, f.pool, movie, "Same "+sfx, "", 0)
	testMovie(t, f.pool, mature, "Mature "+sfx, "", 0)
	dbExec(t, f.pool, `UPDATE media_items SET content_rating = 'G', content_rating_age = 0 WHERE content_id = $1`, movie)
	dbExec(t, f.pool, `UPDATE media_items SET content_rating = 'R', content_rating_age = 17 WHERE content_id = $1`, mature)
	account := testAccount(t, f.pool, "same-"+sfx, "h", nil, nil)
	keep, gone := "keep-"+sfx, "gone-"+sfx
	testProfile(t, f.pool, account, keep, "Keep", true, "")
	testProfile(t, f.pool, account, gone, "Gone", false, "pin")
	// The child profile's ceiling hides the R-rated movie, though it once
	// watched it: a restore gives it no data about a title it may not see.
	dbExec(t, f.pool, `UPDATE user_profiles SET is_child = true, max_content_rating = 'PG' WHERE user_id = $1 AND id = $2`, account, gone)
	for _, p := range []string{keep, gone} {
		for _, m := range []string{movie, mature} {
			dbExec(t, f.pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 30, 300, now())`, account, p, m)
		}
	}
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	dbExec(t, f.pool, `DELETE FROM user_profiles WHERE user_id = $1 AND id = $2`, account, gone)
	dbExec(t, f.pool, `DELETE FROM user_watch_progress WHERE user_id = $1`, account)
	preview, err := f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := previewItem(t, preview, SectionUsers, strconv.Itoa(account))
	if item.Account.Match != MatchAccountID || len(item.Account.Profiles) != 2 ||
		item.Account.Profiles[0].Match != MatchProfileID || item.Account.Profiles[1].Match != MatchNew {
		t.Fatalf("same-server preview = %+v %+v", item.Account, item.Account.Profiles)
	}
	selection := []UserSelection{{Account: strconv.Itoa(account), Target: strconv.Itoa(account), Profiles: []ProfileSelection{
		{Profile: keep, Target: keep, DataTypes: []string{userdata.WatchHistory}},
		{Profile: gone, Target: TargetNew, DataTypes: []string{userdata.WatchHistory}},
	}}}
	report := f.restoreUsers(t, source.ID, preview.Digest, selection)
	restored := reportItem(t, report, SectionUsers, strconv.Itoa(account))
	if restored.Outcome != OutcomeRestored || restored.Account.Created || !restored.Account.Profiles[1].Created {
		t.Fatalf("same-server restore = %+v %+v", restored, restored.Account)
	}
	newGone := restored.Account.Profiles[1].LocalID
	if newGone == gone || countRows(t, f.pool, `SELECT count(*) FROM user_profiles WHERE user_id = $1 AND id = $2 AND pin_hash = 'pin'`, account, newGone) != 1 ||
		countRows(t, f.pool, `SELECT count(*) FROM user_watch_progress WHERE user_id = $1`, account) != 3 ||
		countRows(t, f.pool, `SELECT count(*) FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, account, newGone, mature) != 0 {
		t.Fatalf("restored profile %s: its progress is missing, or it gained the R-rated movie", newGone)
	}
	if !slices.Contains(restored.Account.Hidden, "Mature "+sfx+" (2001)") {
		t.Fatalf("hidden = %v, want the R-rated movie", restored.Account.Hidden)
	}
	checkChangesNothing(t, f.restoreUsers(t, source.ID, preview.Digest, selection))
	if n := countRows(t, f.pool, `SELECT count(*) FROM user_profiles WHERE user_id = $1`, account); n != 2 {
		t.Fatalf("restoring again left %d profiles, want 2", n)
	}
}

// TestOmittedAccountIsReportedDB: an account the restore set cannot carry
// is left out of it, and the omission is a warning the backup keeps: the
// backup is complete with warnings, and the preview names the account and
// why and does not offer it.
func TestOmittedAccountIsReportedDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	sfx := randomHex(t, 4)
	account := testAccount(t, f.pool, "big-"+sfx, "h", nil, nil)
	testProfile(t, f.pool, account, "big-"+sfx, "Main", true, "")
	previous := maxUserDocument
	maxUserDocument = 1 // every account document is "too large"
	t.Cleanup(func() { maxUserDocument = previous })
	source, err := backupUnderLock(t, f.svc, KindManual)
	maxUserDocument = previous
	if err != nil {
		t.Fatal(err)
	}
	isOurs := func(o OmittedItem) bool {
		return o.Section == SectionUsers && o.ID == strconv.Itoa(account) && o.Reason == OmittedTooLarge
	}
	if source.Status != StatusCompleteWithWarnings || !slices.ContainsFunc(source.Omitted, isOurs) {
		t.Fatalf("backup = %s, omitted %+v", source.Status, source.Omitted)
	}
	preview, err := f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(preview.Omitted, isOurs) {
		t.Fatalf("preview omitted = %+v", preview.Omitted)
	}
	for _, s := range preview.Sections {
		for _, item := range s.Items {
			if s.ID == SectionUsers && item.ID == strconv.Itoa(account) {
				t.Fatal("an omitted account is offered")
			}
		}
	}
}

// emailLessAccount inserts an account with a username and no email (NULL),
// with a primary profile that watched movie.
func emailLessAccount(t *testing.T, pool *pgxpool.Pool, username, profile, movie string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, password_hash, role, permissions) VALUES ($1, 'h', 'user', '{}') RETURNING id`,
		username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	testProfile(t, pool, id, profile, "Main", true, "")
	dbExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 30, 300, now())`,
		id, profile, movie)
	return id
}

// TestEmailLessAccountRestoreDB: accounts without an email preview and
// restore. On the same server one previews matched by id; on another server two of
// them, with an email-less account already there, each match nothing (an
// empty email is never a match, so they are neither matched to that account
// nor ambiguous), and both are created with no email.
func TestEmailLessAccountRestoreDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	sfx := randomHex(t, 4)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE username::text LIKE $1`, "%-"+sfx)
	})
	movie := "bkne-" + sfx
	testMovie(t, f.pool, movie, "No email "+sfx, "", 0)
	a := emailLessAccount(t, f.pool, "ne-a-"+sfx, "pne-a-"+sfx, movie)
	b := emailLessAccount(t, f.pool, "ne-b-"+sfx, "pne-b-"+sfx, movie)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}

	// Same server: the account, which lost its progress, matches by id.
	dbExec(t, f.pool, `DELETE FROM user_watch_progress WHERE user_id = $1`, a)
	preview, err := f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatalf("same-server preview with email-less accounts: %v", err)
	}
	item := previewItem(t, preview, SectionUsers, strconv.Itoa(a))
	if !item.Selectable || item.Account.Match != MatchAccountID || item.Account.Email != "" {
		t.Fatalf("same-server preview of an email-less account = %+v %+v", item, item.Account)
	}

	// Another server, which has an email-less account of its own.
	dbExec(t, f.pool, `DELETE FROM users WHERE id = ANY($1)`, []int{a, b})
	emailLessAccount(t, f.pool, "ne-here-"+sfx, "pne-here-"+sfx, movie)
	f.moveToAnotherServer(t, source.ID)
	preview, err = f.svc.Preview(ctx, source.ID)
	if err != nil {
		t.Fatalf("cross-server preview with email-less accounts: %v", err)
	}
	var selection []UserSelection
	for _, id := range []int{a, b} {
		item := previewItem(t, preview, SectionUsers, strconv.Itoa(id))
		if !item.Selectable || item.Account.Match != MatchNew {
			t.Fatalf("cross-server preview of account %d = %+v %+v", id, item, item.Account)
		}
		profile := "pne-a-" + sfx
		if id == b {
			profile = "pne-b-" + sfx
		}
		selection = append(selection, UserSelection{Account: strconv.Itoa(id), Target: TargetNew, Profiles: []ProfileSelection{
			{Profile: profile, Target: TargetNew, DataTypes: []string{userdata.WatchHistory}},
		}})
	}
	report := f.restoreUsers(t, source.ID, preview.Digest, selection)
	for _, id := range []int{a, b} {
		if got := reportItem(t, report, SectionUsers, strconv.Itoa(id)); got.Outcome != OutcomeRestored || got.Account == nil || !got.Account.Created {
			t.Fatalf("cross-server restore of account %d = %+v", id, got)
		}
	}
	if n := countRows(t, f.pool, `SELECT count(*) FROM users WHERE username::text IN ($1, $2) AND email IS NULL`, "ne-a-"+sfx, "ne-b-"+sfx); n != 2 {
		t.Fatalf("%d restored accounts have no email, want 2", n)
	}
}

// TestItemMatchingInATransactionWithOnePoolConnectionDB: an account restore
// and a profile import resolve items inside a transaction that holds a
// connection. An item absent by content id is matched by its provider ids,
// and that match reads through the same transaction: with a pool of one
// connection, a match that asked the pool for another would wait forever.
func TestItemMatchingInATransactionWithOnePoolConnectionDB(t *testing.T) {
	_, dsn := testDB(t)
	// A wait for a second connection shows as this deadline, not a hang.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := randomHex(t, 4)
	local, tmdb := "movie-here-"+suffix, "tmdb-"+suffix
	testMovie(t, pool, local, "Matched by provider id", tmdb, 0)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	source := "movie-elsewhere-" + suffix
	r, err := newItemResolver(ctx, tx, []ItemRef{{ContentID: source, Kind: "movie", Title: "Matched by provider id", Year: 2001, TMDBID: tmdb}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := r.find(ctx, source)
	if err != nil || !ok || got != local {
		t.Fatalf("matching %s by provider id = %q, %v, %v; want %q", source, got, ok, err, local)
	}
}

// TestPluginInstallationMapDB: a play's source installation maps through
// the restore set's plugins section to the one installation here of that
// plugin with the capability; a capability it lacks, two such
// installations, or an installation the set does not name map nothing, and
// the play becomes an import.
func TestPluginInstallationMapDB(t *testing.T) {
	pool, _ := testDB(t)
	ctx := t.Context()
	sfx := randomHex(t, 4)
	install := func(pluginID, capability string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path) VALUES ($1, '1.0.0', $2) RETURNING id`,
			pluginID, "/plugins/"+pluginID+"-"+randomHex(t, 2)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id) })
		dbExec(t, pool, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id) VALUES ($1, 'watch_sync_provider.v1', $2)`, id, capability)
		return id
	}
	unique, dup := "bk.watch."+sfx, "bk.dup."+sfx
	local := install(unique, "floppy")
	install(dup, "x")
	install(dup, "x")
	a := &applier{rs: &RestoreSet{Plugins: []PluginDocument{{PluginID: unique, InstallationID: 10}, {PluginID: dup, InstallationID: 11}}}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lookup := a.pluginInstallation(ctx, tx)
	for _, c := range []struct {
		id         int
		capability string
		want       int
		ok         bool
	}{{10, "floppy", local, true}, {10, "other", 0, false}, {11, "x", 0, false}, {12, "floppy", 0, false}} {
		if got, ok := lookup(c.id, c.capability); got != c.want || ok != c.ok {
			t.Errorf("installation %d %s = %d, %v; want %d, %v", c.id, c.capability, got, ok, c.want, c.ok)
		}
	}
}

// pendingOf reads a preview profile's pending count per data type.
func pendingOf(p PreviewProfile) map[string]int {
	out := map[string]int{}
	for _, d := range p.DataTypes {
		if d.Pending != nil {
			out[d.ID] = *d.Pending
		}
	}
	return out
}

// TestMatchedAccountPendingDB: a preview counts what restoring a matched
// account would change, by the merge rules, without writing:
//
//   - a play, a rating, and a profile preference changed since make that
//     account and profile differ, with one pending change in each type;
//   - a profile the backup has and this server lost makes its account
//     differ;
//   - after the restore, a fresh preview finds the account the same again.
func TestMatchedAccountPendingDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx, pool := t.Context(), f.pool
	sfx := randomHex(t, 4)
	library := testLibrary(t, pool, "pending-"+sfx)
	movies := []string{"bkpe-" + sfx + "-1", "bkpe-" + sfx + "-2"}
	for _, m := range movies {
		testMovie(t, pool, m, "Pending "+m, "", library)
	}
	alice := testAccount(t, pool, "pend-alice-"+sfx, "h", nil, nil)
	main := "pe-main-" + sfx
	testProfile(t, pool, alice, main, "Main", true, "")
	when := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	dbExec(t, pool, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed, source) VALUES (gen_random_uuid()::text, $1, $2, $3, $4, true, 'playback')`, alice, main, movies[0], when)
	dbExec(t, pool, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 60, 600, $4)`, alice, main, movies[1], when)
	dbExec(t, pool, `INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating) VALUES ($1, $2, $3, 4)`, alice, main, movies[0])
	dbExec(t, pool, `INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, alice, main, movies[1])
	dbExec(t, pool, `UPDATE user_profiles SET subtitle_language = 'fr' WHERE user_id = $1 AND id = $2`, alice, main)
	bob := testAccount(t, pool, "pend-bob-"+sfx, "h", nil, nil)
	testProfile(t, pool, bob, "pb-main-"+sfx, "Main", true, "")
	testProfile(t, pool, bob, "pb-kids-"+sfx, "Kids", false, "")

	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	preview := func() *RestorePreview {
		t.Helper()
		p, err := f.svc.Preview(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	same := func(p *RestorePreview, account int) {
		t.Helper()
		item := previewItem(t, p, SectionUsers, strconv.Itoa(account))
		if item.Status != ItemOnThisServer || item.Reason != ReasonSame || item.Selectable {
			t.Fatalf("account %d = %+v, want on this server", account, item)
		}
		for _, pr := range item.Account.Profiles {
			for id, n := range pendingOf(pr) {
				if n != 0 {
					t.Fatalf("profile %s has %d pending %s", pr.ID, n, id)
				}
			}
			if pr.Status != ItemOnThisServer || pr.Reason != ReasonSame || pr.Selectable {
				t.Fatalf("profile = %+v, want on this server", pr)
			}
		}
	}
	dbExec(t, pool, `DELETE FROM user_watch_history WHERE user_id = $1 AND profile_id = $2`, alice, main)
	dbExec(t, pool, `DELETE FROM user_ratings WHERE user_id = $1 AND profile_id = $2`, alice, main)
	dbExec(t, pool, `UPDATE user_profiles SET subtitle_language = 'de' WHERE user_id = $1 AND id = $2`, alice, main)
	dbExec(t, pool, `DELETE FROM user_profiles WHERE user_id = $1 AND id = $2`, bob, "pb-kids-"+sfx)
	p := preview()
	item := previewItem(t, p, SectionUsers, strconv.Itoa(alice))
	if item.Status != ItemDiffers || !item.Selectable {
		t.Fatalf("changed account = %+v", item)
	}
	profile := item.Account.Profiles[0]
	want := map[string]int{userdata.WatchHistory: 1, userdata.Ratings: 1, userdata.Preferences: 1}
	for id, n := range pendingOf(profile) {
		if n != want[id] {
			t.Fatalf("pending %s = %d, want %d (all %v)", id, n, want[id], pendingOf(profile))
		}
	}
	if profile.Status != ItemDiffers || !profile.Selectable {
		t.Fatalf("changed profile = %+v", profile)
	}
	if got := previewItem(t, p, SectionUsers, strconv.Itoa(bob)); got.Status != ItemDiffers || !got.Selectable {
		t.Fatalf("account missing a profile = %+v", got)
	}

	f.restoreUsers(t, source.ID, p.Digest, []UserSelection{{Account: strconv.Itoa(alice), Target: strconv.Itoa(alice),
		Profiles: []ProfileSelection{{Profile: main, Target: main, DataTypes: allTypes()}}}})
	same(preview(), alice)
}

// TestFreshBackupComparesSameDB: every data type of the users seed (plays,
// progress, hides, ratings, favorites, a watchlist, dismissals, settings,
// track and sort preferences, page layouts, personal collections with a
// group and smart rules, notification preferences) compares unchanged
// against a backup taken just before: a preview of it finds each matched
// account on this server with nothing pending.
func TestFreshBackupComparesSameDB(t *testing.T) {
	f := newSelectiveFixture(t)
	s := seedUsers(t, f)
	main := "pa-main-" + s.suffix
	// Shapes real servers hold: a language stored as the three-letter code
	// the settings contract normalizes to two letters, a page layout
	// override of an admin section that is gone, and a hidden-libraries
	// list naming a library deleted since, neither of which a backup can
	// carry. None is a change a restore would make.
	gone := testLibrary(t, f.pool, "users-gone-"+s.suffix)
	dbExec(t, f.pool, `DELETE FROM media_folders WHERE id = $1`, gone)
	dbExec(t, f.pool, `UPDATE user_setting_values SET value = $3::jsonb WHERE user_id = $1 AND profile_id = $2 AND key = 'ui.disabled_library_ids'`,
		s.alice, main, "["+strconv.Itoa(gone)+", "+strconv.Itoa(s.lib2)+"]")
	dbExec(t, f.pool, `INSERT INTO user_setting_values (user_id, key, scope, profile_id, series_id, value) VALUES ($1, 'playback.audio_language', 'profile_series', $2, $3, '"eng"'::jsonb)`,
		s.alice, main, s.movies[2])
	layout, _ := json.Marshal([]userstore.SectionOverride{
		{ID: "o1", ProfileID: main, Scope: "home", SectionID: s.section, Hidden: true},
		{ID: "o3", ProfileID: main, Scope: "home", SectionID: "gone-" + s.suffix, Hidden: true},
	})
	dbExec(t, f.pool, `UPDATE user_settings SET value = $2 WHERE user_id = $1 AND key = 'section_overrides:home:'`, s.alice, string(layout))
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.Preview(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []int{s.alice, s.bob, s.carol, s.frank} {
		item := previewItem(t, preview, SectionUsers, strconv.Itoa(account))
		if item.Status != ItemOnThisServer || item.Reason != ReasonSame {
			var pending []map[string]int
			if item.Account != nil {
				for _, p := range item.Account.Profiles {
					pending = append(pending, pendingOf(p))
				}
			}
			t.Fatalf("account %d = %s %s %q, pending %v", account, item.Status, item.Reason, item.Message, pending)
		}
	}
	// The comparison ran over every data type Alice's main profile carries.
	alice := previewItem(t, preview, SectionUsers, strconv.Itoa(s.alice)).Account.Profiles[0]
	compared := 0
	for _, d := range alice.DataTypes {
		if d.Pending == nil || d.Count == 0 {
			t.Fatalf("alice's %s: count %d, pending %v; want a counted comparison", d.ID, d.Count, d.Pending)
		}
		compared++
	}
	if compared != len(userDataTypes) {
		t.Fatalf("compared %d data types, want %d", compared, len(userDataTypes))
	}
}
