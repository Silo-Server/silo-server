package themesongs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryDirectoriesCanonicalRootBoundaries(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('podcast','Theme directory test',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder) }()
	const root = "/theme-directory-fixture"
	const directory = root + "/Season 1"
	const file = directory + "/episode.mp3"
	if _, err := pool.Exec(ctx, `INSERT INTO media_files(media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3)`, folder, file, root); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	for _, tc := range []struct {
		name string
		root string
		file string
		want []string
	}{
		{"clean", root, file, []string{root, directory}},
		{"trailing slash", root + "/", file, []string{root, directory}},
		{"dot component", root + "/.", file, []string{root, directory}},
		{"parent component", root + "/nested/..", file, []string{root, directory}},
		{"filesystem root", "/", file, []string{"/", root, directory}},
		{"empty root", "", file, []string{directory}},
		{"file root", file, file, []string{directory}},
		{"sibling prefix", root + "-other", file, []string{directory}},
		{"file duplicate separator", root, root + "//Season 1/episode.mp3", nil},
		{"file dot component", root, root + "/./Season 1/episode.mp3", nil},
		{"file parent component", root, root + "/nested/../Season 1/episode.mp3", nil},
		{"file root alias", root, "/" + root + "/episode.mp3", nil},
		{"file alias without root", "", root + "/./Season 1/episode.mp3", nil},
		{"file and root aliases", root + "/.", root + "/./Season 1/episode.mp3", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `UPDATE media_files SET canonical_root_path=$1, file_path=$2 WHERE media_folder_id=$3`, tc.root, tc.file, folder); err != nil {
				t.Fatal(err)
			}
			for _, scope := range []string{"", directory} {
				got, err := repo.Directories(ctx, folder, scope)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, tc.want) {
					t.Errorf("root=%q scope=%q: directories=%q, want %q", tc.root, scope, got, tc.want)
				}
			}
		})
	}

	// The final malformed row must not prevent another video's discovery.
	if _, err := pool.Exec(ctx, `INSERT INTO media_files(media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3)`, folder, file, root); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", directory} {
		got, err := repo.Directories(ctx, folder, scope)
		if err != nil || !slices.Equal(got, []string{root, directory}) {
			t.Fatalf("valid row lost alongside malformed row: scope=%q directories=%q err=%v", scope, got, err)
		}
	}
}

func TestRepositoryPruneOrphansIncludesAncestorOfSubtree(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series','Theme ancestor pruning test',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder) }()
	root := filepath.Join(t.TempDir(), "Show")
	scope := filepath.Join(root, "Season 01")
	repo := NewRepository(pool)
	file := File{Song: Song{Title: "Series theme", Container: "mp3", DurationSeconds: 4}, OwnerPath: root, Path: filepath.Join(root, "theme.mp3"), Size: 50, Modified: time.Now().Truncate(time.Microsecond)}
	if err := repo.Replace(ctx, folder, root, []File{file}); err != nil {
		t.Fatal(err)
	}
	// A retained missing video keeps the theme until scanner cleanup removes its row.
	var video int
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path,canonical_root_path,missing_since) VALUES($1,$2,$3,now()) RETURNING id`, folder, filepath.Join(scope, "episode.mkv"), root).Scan(&video); err != nil {
		t.Fatal(err)
	}
	if err := repo.PruneOrphans(ctx, folder, scope, false); err != nil {
		t.Fatal(err)
	}
	cache, err := repo.ScanFiles(ctx, folder, scope, false)
	if err != nil || len(cache[root]) != 1 {
		t.Fatalf("missing video did not retain ancestor theme: cache=%v err=%v", cache, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media_files WHERE id=$1`, video); err != nil {
		t.Fatal(err)
	}
	if err := repo.PruneOrphans(ctx, folder, scope, true); err != nil {
		t.Fatal(err)
	}
	cache, err = repo.ScanFiles(ctx, folder, scope, false)
	if err != nil || len(cache[root]) != 1 {
		t.Fatalf("exact subtree prune changed ancestor theme: cache=%v err=%v", cache, err)
	}
	if err := repo.PruneOrphans(ctx, folder, scope, false); err != nil {
		t.Fatal(err)
	}
	cache, err = repo.ScanFiles(ctx, folder, scope, false)
	if err != nil || len(cache[root]) != 0 {
		t.Fatalf("orphaned ancestor theme survived subtree prune: cache=%v err=%v", cache, err)
	}
}

func TestRepositoryInheritanceRematchingAndAccess(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series','Theme test',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder) }()
	series := "theme-series-" + uuid.NewString()
	season := series + "-season"
	episode := series + "-episode"
	newSeries := series + "-new"
	for _, id := range []string{series, newSeries} {
		exec(`INSERT INTO media_items(content_id,type,title) VALUES($1,'series','Theme test')`, id)
		defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, id) }()
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, folder)
	}
	exec(`INSERT INTO seasons(content_id,series_id,season_number) VALUES($1,$2,1)`, season, series)
	exec(`INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number) VALUES($1,$2,$3,1,1)`, episode, series, season)
	root := filepath.Join(t.TempDir(), "Show_100%_音楽")
	seasonDir := filepath.Join(root, "Season 1")
	exec(`INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3,$4,$5)`, series, episode, folder, filepath.Join(seasonDir, "Disc 1", "episode.mkv"), root)
	repo := NewRepository(pool)
	dirs, err := repo.Directories(ctx, folder, seasonDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, dir := range dirs {
		if dir == seasonDir {
			found = true
		}
	}
	if !found {
		t.Fatal("nested season directory was not discovered", dirs)
	}
	file := File{Song: Song{Title: "Series theme", Container: "mp3", DurationSeconds: 4}, OwnerPath: root, Path: filepath.Join(root, "theme.mp3"), Size: 50, Modified: time.Now().Truncate(time.Microsecond)}
	if err := repo.Replace(ctx, folder, root, []File{file}); err != nil {
		t.Fatal(err)
	}
	check := func(id string, inherit bool, owner string, count int) []File {
		t.Helper()
		got, files, err := repo.Resolve(ctx, id, inherit, catalog.AccessFilter{})
		if err != nil || got != owner || len(files) != count {
			t.Fatalf("%s inherit=%v: owner=%s count=%d err=%v", id, inherit, got, len(files), err)
		}
		return files
	}
	files := check(episode, true, series, 1)
	themeID := files[0].ID
	// Exercise the CTE's parameterized library predicates and quality ceiling.
	if _, scoped, err := repo.Resolve(ctx, series, false, catalog.AccessFilter{AllowedLibraryIDs: []int{folder}, DisabledLibraryIDs: []int{-1}, MaxPlaybackQuality: "1080p"}); err != nil || len(scoped) != 1 {
		t.Fatal("parameterized theme access query failed", scoped, err)
	}
	exec(`UPDATE media_files SET resolution='2160P' WHERE media_folder_id=$1`, folder)
	exec(`INSERT INTO media_files(content_id,media_folder_id,file_path,canonical_root_path,resolution) VALUES($1,$2,$3,$4,'1080P')`, series, folder, filepath.Join(root+" low", "episode.mkv"), root+" low")
	if _, scoped, err := repo.Resolve(ctx, series, false, catalog.AccessFilter{AllowedLibraryIDs: []int{folder}, MaxPlaybackQuality: "1080p"}); err != nil || len(scoped) != 0 {
		t.Fatal("visible series leaked a theme from a quality-restricted directory", scoped, err)
	}
	exec(`DELETE FROM media_files WHERE media_folder_id=$1 AND resolution='1080P'`, folder)
	exec(`UPDATE media_files SET resolution=NULL WHERE media_folder_id=$1`, folder)
	// Bytewise prefix bounds must exclude similarly named sibling directories.
	exec(`INSERT INTO media_files(content_id,media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3,$4)`, newSeries, folder, filepath.Join(root+" sequel", "episode.mkv"), root+" sequel")
	check(series, false, series, 1)
	exec(`DELETE FROM media_files WHERE content_id=$1 AND media_folder_id=$2`, newSeries, folder)
	check(season, true, series, 1)
	check(season, false, season, 0)
	check(episode, false, episode, 0)
	check(series+"-S01", true, series, 1)
	check(series+"-S01", false, series+"-S01", 0)
	if _, _, err := repo.Resolve(ctx, series+"-S99", true, catalog.AccessFilter{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("nonexistent synthetic season was accepted", err)
	}
	if _, _, err := repo.Resolve(ctx, series+"-S01", true, catalog.AccessFilter{AllowedLibraryIDs: []int{}}); !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatal("restricted synthetic season was accepted", err)
	}
	if _, err := repo.Find(ctx, themeID, catalog.AccessFilter{AllowedLibraryIDs: []int{}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("restricted theme was found", err)
	}
	seasonFile := file
	seasonFile.OwnerPath = seasonDir
	seasonFile.Path = filepath.Join(seasonDir, "theme.mp3")
	if err := repo.Replace(ctx, folder, seasonDir, []File{seasonFile}); err != nil {
		t.Fatal(err)
	}
	check(episode, true, season, 1)
	check(season, false, season, 1)
	// The catalog can expose this season using episodes alone.
	exec(`UPDATE episodes SET season_id=NULL WHERE content_id=$1`, episode)
	exec(`DELETE FROM seasons WHERE content_id=$1`, season)
	files = check(series+"-S01", false, series+"-S01", 1)
	check(episode, true, series+"-S01", 1)
	if found, err := repo.Find(ctx, files[0].ID, catalog.AccessFilter{}); err != nil || found.ID != files[0].ID || found.OwnerType != "season" {
		t.Fatal("synthetic season theme cannot be played", err)
	}
	if err := repo.Replace(ctx, folder, seasonDir, nil); err != nil {
		t.Fatal(err)
	}
	check(episode, true, series, 1)
	// Flat episodes now use file-valued canonical roots. A unique series still
	// owns the containing directory, including after metadata matching.
	flatPath := filepath.Join(root, "Example.Show.S01E01.mkv")
	_, assignments := naming.InferRootAssignments([]string{flatPath}, "series", folder, nil, root)
	flatRoot := assignments[flatPath].RootPath
	if flatRoot != flatPath {
		t.Fatalf("flat fixture did not exercise a file-valued root: %q", flatRoot)
	}
	exec(`UPDATE media_files SET file_path=$1,canonical_root_path=$2 WHERE media_folder_id=$3`, flatPath, flatRoot, folder)
	check(series, false, series, 1)
	check(episode, true, series, 1)
	check(series+"-S01", true, series, 1)
	if set, err := NewService(repo, "test secret").Discover(ctx, episode, true, catalog.AccessFilter{}); err != nil || set.OwnerID != series || len(set.Items) != 1 {
		t.Fatal("native flat episode theme discovery failed", set, err)
	}
	if selected, err := NewService(repo, "test secret").Select(ctx, series, themeID, catalog.AccessFilter{}); err != nil || selected.ID != themeID {
		t.Fatal("native flat series theme selection failed", selected, err)
	}
	if found, err := repo.Find(ctx, themeID, catalog.AccessFilter{}); err != nil || found.OwnerType != "series" {
		t.Fatal("compatibility flat series theme lookup failed", found, err)
	}
	for _, contentID := range []any{nil, newSeries} {
		exec(`INSERT INTO media_files(content_id,media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3,$3)`, contentID, folder, filepath.Join(root, "Other.Show.S01E01.mkv"))
		check(series, false, series, 0)
		check(episode, true, episode, 0)
		if _, err := repo.Find(ctx, themeID, catalog.AccessFilter{}); !errors.Is(err, ErrNotFound) {
			t.Fatal("flat directory ambiguity leaked a theme", err)
		}
		exec(`DELETE FROM media_files WHERE media_folder_id=$1 AND file_path=$2`, folder, filepath.Join(root, "Other.Show.S01E01.mkv"))
	}
	exec(`UPDATE media_files SET file_path=$1,canonical_root_path=$2 WHERE media_folder_id=$3`, filepath.Join(seasonDir, "Disc 1", "episode.mkv"), root, folder)
	// A metadata rematch changes the owner without rescanning theme audio.
	exec(`UPDATE media_files SET content_id=$1 WHERE media_folder_id=$2`, newSeries, folder)
	check(series, false, series, 0)
	files = check(newSeries, false, newSeries, 1)
	if files[0].ID != themeID {
		t.Fatal("theme ID changed during rematch")
	}
	// A shared root has no unambiguous series or movie owner.
	exec(`INSERT INTO media_files(content_id,media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3,$4)`, series, folder, filepath.Join(root, "other.mkv"), root)
	check(newSeries, false, newSeries, 0)
	check(series, false, series, 0)
	if _, err := repo.Find(ctx, themeID, catalog.AccessFilter{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("ambiguous directory resolved an audio ID", err)
	}
	exec(`DELETE FROM media_files WHERE content_id=$1 AND media_folder_id=$2`, series, folder)
	exec(`UPDATE media_items SET type='movie' WHERE content_id=$1`, newSeries)
	exec(`UPDATE media_files SET episode_id=NULL WHERE media_folder_id=$1`, folder)
	check(newSeries, false, newSeries, 1)
	exec(`INSERT INTO media_files(content_id,media_folder_id,file_path,canonical_root_path) VALUES($1,$2,$3,$4)`, series, folder, filepath.Join(root, "other.mkv"), root)
	check(newSeries, false, newSeries, 0)
	exec(`DELETE FROM media_files WHERE content_id=$1 AND media_folder_id=$2`, series, folder)
	exec(`UPDATE media_folders SET enabled=false WHERE id=$1`, folder)
	if _, files, err := repo.Resolve(ctx, newSeries, false, catalog.AccessFilter{}); err == nil && len(files) > 0 {
		t.Fatal("disabled library theme is visible")
	}
	exec(`UPDATE media_files SET missing_since=now() WHERE media_folder_id=$1`, folder)
	if err := repo.PruneOrphans(ctx, folder, "", false); err != nil {
		t.Fatal(err)
	}
	cache, err := repo.ScanFiles(ctx, folder, "", false)
	if err != nil || len(cache) != 1 {
		t.Fatal("missing video retention must preserve its themes", cache, err)
	}
	exec(`DELETE FROM media_files WHERE media_folder_id=$1`, folder)
	if err := repo.Replace(ctx, folder, seasonDir, []File{seasonFile}); err != nil {
		t.Fatal(err)
	}
	if err := repo.PruneOrphans(ctx, folder, root, true); err != nil {
		t.Fatal(err)
	}
	cache, err = repo.ScanFiles(ctx, folder, "", false)
	if err != nil || len(cache) != 1 || len(cache[seasonDir]) != 1 {
		t.Fatal("exact pruning changed a descendant owner", cache, err)
	}
	cache, err = repo.ScanFiles(ctx, folder, root, true)
	if err != nil || len(cache) != 0 {
		t.Fatal("exact cache read included a descendant owner", cache, err)
	}
	if err := repo.PruneOrphans(ctx, folder, "", false); err != nil {
		t.Fatal(err)
	}
	cache, err = repo.ScanFiles(ctx, folder, "", false)
	if err != nil || len(cache) != 0 {
		t.Fatal("orphan themes were retained", cache, err)
	}
}

func TestRepositoryIsActiveTheme(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies','Theme input approval test',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder) }()
	root := filepath.Join(t.TempDir(), "Movie")
	path := filepath.Join(root, "theme.ogg")
	repo := NewRepository(pool)
	if err := repo.Replace(ctx, folder, root, []File{{Song: Song{Title: "Theme", Container: "ogg", DurationSeconds: 4}, OwnerPath: root, Path: path, Size: 50, Modified: time.Now().Truncate(time.Microsecond)}}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM item_theme_songs WHERE media_folder_id=$1`, folder).Scan(&id); err != nil {
		t.Fatal(err)
	}
	check := func(id int64, path string) bool {
		t.Helper()
		active, err := repo.IsActiveTheme(ctx, id, path)
		if err != nil {
			t.Fatal(err)
		}
		return active
	}
	if !check(id, path) {
		t.Fatal("discovered theme was not approved")
	}
	if check(id, filepath.Join(root, "other.ogg")) || check(id+1_000_000, path) || check(0, path) {
		t.Fatal("theme approval accepted a mismatched id or path")
	}
	if _, err := pool.Exec(ctx, `UPDATE media_folders SET enabled=false WHERE id=$1`, folder); err != nil {
		t.Fatal(err)
	}
	if check(id, path) {
		t.Fatal("theme in a disabled library was approved")
	}
}
