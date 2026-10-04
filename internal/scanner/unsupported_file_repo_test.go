package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestGroupUnsupportedFiles(t *testing.T) {
	got := GroupUnsupportedFiles(3, []UnsupportedFile{
		{Path: "/movies/Ronin (1998)/VIDEO_TS/VTS_01_2.VOB", Reason: UnsupportedReasonDVDVOB},
		{Path: "/movies/Ronin (1998)/VIDEO_TS/VIDEO_TS.VOB", Reason: UnsupportedReasonDVDVOB},
		{Path: "/movies/Ronin (1998)/VIDEO_TS/VTS_01_1.VOB", Reason: UnsupportedReasonDVDVOB},
		{Path: "/movies/Mixed/b.rmvb", Reason: UnsupportedReasonRealMedia},
		{Path: "/movies/Mixed/a.iso", Reason: UnsupportedReasonDiscImage},
	})
	want := []models.UnsupportedMediaFileGroup{
		{MediaFolderID: 3, DirectoryPath: "/movies/Mixed", Reason: UnsupportedReasonDiscImage, FileCount: 1, FileNames: []string{"a.iso"}},
		{MediaFolderID: 3, DirectoryPath: "/movies/Mixed", Reason: UnsupportedReasonRealMedia, FileCount: 1, FileNames: []string{"b.rmvb"}},
		{MediaFolderID: 3, DirectoryPath: "/movies/Ronin (1998)/VIDEO_TS", Reason: UnsupportedReasonDVDVOB, FileCount: 3, FileNames: []string{"VIDEO_TS.VOB", "VTS_01_1.VOB", "VTS_01_2.VOB"}},
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Fatalf("groups =\n%+v\nwant\n%+v", got, want)
	}
	if got := GroupUnsupportedFiles(3, nil); len(got) != 0 {
		t.Fatalf("groups of nothing = %+v, want none", got)
	}
}

// A disc folder with more clips than the cap keeps the first names in name
// order and counts every clip.
func TestGroupUnsupportedFilesCapsFileNames(t *testing.T) {
	files := make([]UnsupportedFile, 0, MaxUnsupportedFileNames+20)
	for i := MaxUnsupportedFileNames + 19; i >= 0; i-- {
		files = append(files, UnsupportedFile{Path: fmt.Sprintf("/movies/Thief (1981)/BDMV/STREAM/%05d.m2ts", i), Reason: UnsupportedReasonDiscStream})
	}
	got := GroupUnsupportedFiles(3, files)
	if len(got) != 1 {
		t.Fatalf("groups = %+v, want one", got)
	}
	names := got[0].FileNames
	if got[0].FileCount != MaxUnsupportedFileNames+20 || len(names) != MaxUnsupportedFileNames ||
		names[0] != "00000.m2ts" || names[len(names)-1] != fmt.Sprintf("%05d.m2ts", MaxUnsupportedFileNames-1) {
		t.Fatalf("count %d, %d names from %s to %s", got[0].FileCount, len(names), names[0], names[len(names)-1])
	}
}

func TestUnsupportedFileRepositoryReplace(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Unsupported Files Test")
	otherFolderID := seedDeadRootTestFolder(t, pool, "movies", "Unsupported Files Other")
	repo := NewUnsupportedFileRepository(pool)
	base := fmt.Sprintf("/unsupported-%d", time.Now().UnixNano())
	// A scan start later than every row's last sighting, so no row was
	// refreshed by another scan while this one ran.
	afterEverything := time.Now().Add(time.Hour)

	group := func(folder int, dir, reason string, count int) models.UnsupportedMediaFileGroup {
		return models.UnsupportedMediaFileGroup{MediaFolderID: folder, DirectoryPath: base + dir, Reason: reason, FileCount: count, FileNames: []string{"sample"}}
	}
	rows := func(folder int) []string {
		t.Helper()
		result, err := pool.Query(ctx, `SELECT directory_path, reason, file_count FROM unsupported_media_files WHERE media_folder_id = $1`, folder)
		if err != nil {
			t.Fatalf("query rows: %v", err)
		}
		defer result.Close()
		out := []string{}
		for result.Next() {
			var dir, reason string
			var count int
			if err := result.Scan(&dir, &reason, &count); err != nil {
				t.Fatalf("scan row: %v", err)
			}
			out = append(out, fmt.Sprintf("%s %s %d", strings.TrimPrefix(dir, base), reason, count))
		}
		sort.Strings(out)
		return out
	}
	expect := func(step string, folder int, want ...string) {
		t.Helper()
		if got := rows(folder); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s: rows = %v, want %v", step, got, want)
		}
	}

	// A library scan records every group it found.
	if err := repo.Replace(ctx, folderID, nil, nil, afterEverything, []models.UnsupportedMediaFileGroup{
		group(folderID, "/movies/Ronin (1998)/VIDEO_TS", UnsupportedReasonDVDVOB, 6),
		group(folderID, "/movies/Manhunter (1986)", UnsupportedReasonRealMedia, 1),
		group(folderID, "/movies/Offline/VIDEO_TS", UnsupportedReasonDVDVOB, 2),
		group(folderID, "/tv/Show/Season 01/BDMV/STREAM", UnsupportedReasonDiscStream, 40),
	}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if err := repo.Replace(ctx, otherFolderID, nil, nil, afterEverything, []models.UnsupportedMediaFileGroup{
		group(otherFolderID, "/movies/Manhunter (1986)", UnsupportedReasonRealMedia, 1),
	}); err != nil {
		t.Fatalf("other library replace: %v", err)
	}
	expect("first scan", folderID,
		"/movies/Manhunter (1986) realmedia 1",
		"/movies/Offline/VIDEO_TS dvd_vob 2",
		"/movies/Ronin (1998)/VIDEO_TS dvd_vob 6",
		"/tv/Show/Season 01/BDMV/STREAM disc_stream 40",
	)

	// A library scan that started before another scan last refreshed the
	// rows, and found none of them, leaves them alone.
	if err := repo.Replace(ctx, folderID, nil, nil, time.Now().Add(-time.Hour), nil); err != nil {
		t.Fatalf("overlapping replace: %v", err)
	}
	expect("overlapping scan", folderID,
		"/movies/Manhunter (1986) realmedia 1",
		"/movies/Offline/VIDEO_TS dvd_vob 2",
		"/movies/Ronin (1998)/VIDEO_TS dvd_vob 6",
		"/tv/Show/Season 01/BDMV/STREAM disc_stream 40",
	)

	// A subtree scan replaces only its subtree. A sibling sharing a string
	// prefix with the scope is outside it.
	if err := repo.Replace(ctx, folderID, []string{base + "/movies/Ronin (1998)"}, nil, afterEverything, []models.UnsupportedMediaFileGroup{
		group(folderID, "/movies/Ronin (1998)/VIDEO_TS", UnsupportedReasonDVDVOB, 4),
	}); err != nil {
		t.Fatalf("subtree replace: %v", err)
	}
	if err := repo.Replace(ctx, folderID, []string{base + "/movies/Man"}, nil, afterEverything, nil); err != nil {
		t.Fatalf("prefix sibling replace: %v", err)
	}
	expect("subtree scans", folderID,
		"/movies/Manhunter (1986) realmedia 1",
		"/movies/Offline/VIDEO_TS dvd_vob 2",
		"/movies/Ronin (1998)/VIDEO_TS dvd_vob 4",
		"/tv/Show/Season 01/BDMV/STREAM disc_stream 40",
	)

	// A library scan drops what it no longer finds but keeps rows under a
	// path it could not read, and leaves other libraries alone.
	if err := repo.Replace(ctx, folderID, nil, []string{base + "/movies/Offline/"}, afterEverything, []models.UnsupportedMediaFileGroup{
		group(folderID, "/movies/Ronin (1998)/VIDEO_TS", UnsupportedReasonDVDVOB, 4),
	}); err != nil {
		t.Fatalf("second library replace: %v", err)
	}
	expect("second library scan", folderID,
		"/movies/Offline/VIDEO_TS dvd_vob 2",
		"/movies/Ronin (1998)/VIDEO_TS dvd_vob 4",
	)
	expect("other library", otherFolderID, "/movies/Manhunter (1986) realmedia 1")

	// A refreshed row keeps when it was first found.
	var firstSeen, lastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT first_seen_at, last_seen_at FROM unsupported_media_files WHERE media_folder_id = $1 AND directory_path = $2`,
		folderID, base+"/movies/Ronin (1998)/VIDEO_TS").Scan(&firstSeen, &lastSeen); err != nil {
		t.Fatalf("query seen times: %v", err)
	}
	if !lastSeen.After(firstSeen) {
		t.Errorf("first_seen_at %v, last_seen_at %v: want the refresh to move only last_seen_at", firstSeen, lastSeen)
	}

	// Deleting the library deletes its rows.
	if _, err := pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID); err != nil {
		t.Fatalf("delete library: %v", err)
	}
	expect("deleted library", folderID)
}

func TestUnsupportedFileRepositoryListPage(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	name := fmt.Sprintf("Unsupported List %d", time.Now().UnixNano())
	folderID := seedDeadRootTestFolder(t, pool, "movies", name)
	repo := NewUnsupportedFileRepository(pool)
	base := fmt.Sprintf("/unsupported-list-%d", time.Now().UnixNano())

	if err := repo.Replace(ctx, folderID, nil, nil, time.Now().Add(time.Hour), []models.UnsupportedMediaFileGroup{
		{MediaFolderID: folderID, DirectoryPath: base + "/Ronin (1998)/VIDEO_TS", Reason: UnsupportedReasonDVDVOB, FileCount: 2, FileNames: []string{"VIDEO_TS.VOB", "VTS_01_1.VOB"}},
		{MediaFolderID: folderID, DirectoryPath: base + "/Manhunter (1986)", Reason: UnsupportedReasonRealMedia, FileCount: 1, FileNames: []string{"Manhunter (1986).rmvb"}},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	for _, tc := range []struct {
		search string
		want   []string
	}{
		{search: base, want: []string{"/Manhunter (1986)", "/Ronin (1998)/VIDEO_TS"}},
		{search: base + "/ronin", want: []string{"/Ronin (1998)/VIDEO_TS"}},
		{search: "vts_01", want: []string{"/Ronin (1998)/VIDEO_TS"}},
		{search: name, want: []string{"/Manhunter (1986)", "/Ronin (1998)/VIDEO_TS"}},
	} {
		page, err := repo.ListPage(ctx, tc.search, 10, 0)
		if err != nil {
			t.Fatalf("list %q: %v", tc.search, err)
		}
		got := make([]string, 0, len(page))
		for _, group := range page {
			got = append(got, strings.TrimPrefix(group.DirectoryPath, base))
		}
		total, err := repo.Count(ctx, tc.search)
		if err != nil {
			t.Fatalf("count %q: %v", tc.search, err)
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") || total != len(tc.want) {
			t.Errorf("search %q = %v (total %d), want %v", tc.search, got, total, tc.want)
		}
	}

	page, err := repo.ListPage(ctx, base, 1, 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page) != 1 || page[0].DirectoryPath != base+"/Ronin (1998)/VIDEO_TS" || page[0].FileCount != 2 ||
		strings.Join(page[0].FileNames, "|") != "VIDEO_TS.VOB|VTS_01_1.VOB" {
		t.Fatalf("second page = %+v", page)
	}
}

// A library scan reports the files it skips by type in its result, and an
// unreachable root and an unreadable directory among the paths it protected.
func TestScanFolderReportsUnsupportedFiles(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Unsupported Scan Test")

	base := t.TempDir()
	root := filepath.Join(base, "movies")
	offline := filepath.Join(base, "offline")
	for _, rel := range []string{
		"Heat (1995)/Heat (1995).mkv",
		"Ronin (1998)/VIDEO_TS/VIDEO_TS.VOB",
		"Ronin (1998)/VIDEO_TS/VTS_01_1.VOB",
		"Manhunter (1986)/Manhunter (1986).rmvb",
	} {
		writeTestFile(t, filepath.Join(root, rel), "fake payload")
	}
	locked := filepath.Join(root, "Locked")
	writeTestFile(t, filepath.Join(locked, "Thief (1981).iso"), "fake payload")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if entries, err := os.ReadDir(locked); err == nil && len(entries) > 0 {
		t.Skip("running with permission to read a mode-000 directory")
	}

	folder := &models.MediaFolder{ID: folderID, Paths: []string{root, offline}, Type: "movies", Name: "Unsupported Scan Test", Enabled: true}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, false, 0)
	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	got := make([]string, 0, len(result.UnsupportedFiles))
	for _, file := range result.UnsupportedFiles {
		rel, err := filepath.Rel(root, file.Path)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		got = append(got, rel+" "+file.Reason)
	}
	sort.Strings(got)
	want := []string{
		"Manhunter (1986)/Manhunter (1986).rmvb " + UnsupportedReasonRealMedia,
		"Ronin (1998)/VIDEO_TS/VIDEO_TS.VOB " + UnsupportedReasonDVDVOB,
		"Ronin (1998)/VIDEO_TS/VTS_01_1.VOB " + UnsupportedReasonDVDVOB,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("unsupported files = %v, want %v", got, want)
	}
	if fmt.Sprint(result.ProtectedPaths) != fmt.Sprint([]string{offline, locked}) {
		t.Errorf("protected paths = %v, want [%s %s]", result.ProtectedPaths, offline, locked)
	}
}

// A subtree scan of a root whose mount dropped, leaving an empty mountpoint,
// protects that root, so the list keeps the rows under it.
func TestScanSubtreeProtectsASuspectEmptyRoot(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Unsupported Subtree Test")

	root := filepath.Join(t.TempDir(), "movies")
	writeTestFile(t, filepath.Join(root, "Heat (1995)", "Heat (1995).mkv"), "fake payload")
	writeTestFile(t, filepath.Join(root, "Ronin (1998)", "VIDEO_TS", "VTS_01_1.VOB"), "fake payload")
	folder := &models.MediaFolder{ID: folderID, Paths: []string{root}, Type: "movies", Name: "Unsupported Subtree Test", Enabled: true}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, false, 0)
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			t.Fatalf("empty root: %v", err)
		}
	}
	result, err := scanner.ScanSubtree(ctx, folder, root)
	if err != nil {
		t.Fatalf("subtree scan: %v", err)
	}
	if len(result.UnsupportedFiles) != 0 {
		t.Errorf("unsupported files = %v, want none from an empty root", result.UnsupportedFiles)
	}
	if fmt.Sprint(result.ProtectedPaths) != fmt.Sprint([]string{root}) {
		t.Errorf("protected paths = %v, want [%s]", result.ProtectedPaths, root)
	}
}
