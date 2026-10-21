package scanner

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestScopedPathRangesMatchLiteralPathsWithGenericPlans(t *testing.T) {
	ctx := t.Context()
	pool := newScannerTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "series", "Literal path ranges")
	config := pool.Config()
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	genericPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect generic-plan pool: %v", err)
	}
	t.Cleanup(genericPool.Close)
	root := fmt.Sprintf("/range-%d/Shows_100%%\\Δ", time.Now().UnixNano())
	paths := []string{
		root,
		root + "/episode.mkv",
		root + "/季節/episode.mkv",
		root + "2/episode.mkv",
		root + "0",
		root + "-/episode.mkv",
		filepath.Dir(root) + "/ShowsX100abcΔ/episode.mkv",
		filepath.Dir(root) + "/Shows_100%Δ/episode.mkv",
	}
	for _, path := range paths {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size)
			VALUES ($1, $2, 1024)
		`, folderID, path); err != nil {
			t.Fatalf("seed literal path: %v", err)
		}
	}
	for _, tc := range []struct {
		name, scope string
		want        []string
	}{
		{name: "literal root and descendants", scope: root, want: paths[:3]},
		{name: "trailing separator", scope: root + "/", want: paths[1:3]},
		{name: "filesystem root", scope: string(filepath.Separator), want: paths},
		{name: "exact file", scope: paths[1], want: paths[1:2]},
		{name: "empty scope", scope: root + "/absent", want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewFileRepository(genericPool)
			stateFiles, err := repo.GetScanStateByFolderAndPathPrefix(t.Context(), folderID, tc.scope)
			if err != nil {
				t.Fatalf("read generic-plan path scope: %v", err)
			}
			files, err := repo.GetByFolderAndPathPrefix(t.Context(), folderID, tc.scope)
			if err != nil {
				t.Fatalf("read generic-plan files by path prefix: %v", err)
			}
			want := slices.Clone(tc.want)
			slices.Sort(want)
			for name, got := range map[string][]string{
				"GetScanStateByFolderAndPathPrefix": scanStatePaths(stateFiles),
				"GetByFolderAndPathPrefix":          mediaFilePaths(files),
			} {
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Fatalf("%s scope %q paths = %q, want %q", name, tc.scope, got, want)
				}
			}
		})
	}
}

func scanStatePaths(files []*scanStateFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.FilePath)
	}
	return paths
}

func mediaFilePaths(files []*models.MediaFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.FilePath)
	}
	return paths
}
