package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFolderRepositoryRealtimeMonitoring(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewFolderRepository(pool)

	suffix := time.Now().UnixNano()
	create := func(name string, realtime *bool) *models.MediaFolder {
		t.Helper()
		folder, err := repo.Create(ctx, CreateFolderInput{
			Paths:              []string{fmt.Sprintf("/media/rtmon-%s-%d", name, suffix)},
			Type:               "movies",
			Name:               fmt.Sprintf("rtmon-%s-%d", name, suffix),
			RealtimeMonitoring: realtime,
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), folder.ID) })
		return folder
	}
	get := func(id int) *models.MediaFolder {
		t.Helper()
		folder, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("GetByID(%d): %v", id, err)
		}
		return folder
	}
	off := false
	on := true

	// Omitted on create means on, like the column default.
	defaulted := create("default", nil)
	if !defaulted.RealtimeMonitoring {
		t.Fatal("create without the field: RealtimeMonitoring = false, want true")
	}
	if !get(defaulted.ID).RealtimeMonitoring {
		t.Fatal("GetByID after default create: RealtimeMonitoring = false, want true")
	}

	disabled := create("off", &off)
	if disabled.RealtimeMonitoring {
		t.Fatal("create with false: RealtimeMonitoring = true, want false")
	}

	// An update that never sets the field (the frozen /api/v1 path) leaves
	// the column unchanged.
	newName := fmt.Sprintf("rtmon-renamed-%d", suffix)
	if err := repo.Update(ctx, disabled.ID, UpdateFolderInput{Name: &newName}); err != nil {
		t.Fatalf("update name: %v", err)
	}
	if got := get(disabled.ID); got.RealtimeMonitoring || got.Name != newName {
		t.Fatalf("after name-only update: name=%q realtime=%v, want %q false", got.Name, got.RealtimeMonitoring, newName)
	}

	if err := repo.Update(ctx, disabled.ID, UpdateFolderInput{RealtimeMonitoring: &on}); err != nil {
		t.Fatalf("update realtime on: %v", err)
	}
	if !get(disabled.ID).RealtimeMonitoring {
		t.Fatal("after update to true: RealtimeMonitoring = false")
	}
	if err := repo.Update(ctx, defaulted.ID, UpdateFolderInput{RealtimeMonitoring: &off}); err != nil {
		t.Fatalf("update realtime off: %v", err)
	}

	// Every folder loader reads the column.
	want := map[int]bool{defaulted.ID: false, disabled.ID: true}
	checkLoaded := func(label string, folders []*models.MediaFolder) {
		t.Helper()
		seen := 0
		for _, f := range folders {
			if w, ok := want[f.ID]; ok {
				seen++
				if f.RealtimeMonitoring != w {
					t.Fatalf("%s: folder %d RealtimeMonitoring = %v, want %v", label, f.ID, f.RealtimeMonitoring, w)
				}
			}
		}
		if seen != len(want) {
			t.Fatalf("%s: saw %d of %d test folders", label, seen, len(want))
		}
	}
	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	checkLoaded("List", all)
	enabled, err := repo.GetEnabled(ctx)
	if err != nil {
		t.Fatalf("GetEnabled: %v", err)
	}
	checkLoaded("GetEnabled", enabled)
	byIDs, err := repo.ListByIDs(ctx, []int{defaulted.ID, disabled.ID})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	checkLoaded("ListByIDs", byIDs)
}

func TestFolderDeleteCascadesLibraryMonitorStatus(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewFolderRepository(pool)

	suffix := time.Now().UnixNano()
	folder, err := repo.Create(ctx, CreateFolderInput{
		Paths: []string{fmt.Sprintf("/media/rtmon-status-%d", suffix)},
		Type:  "movies",
		Name:  fmt.Sprintf("rtmon-status-%d", suffix),
	})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	nodeID := fmt.Sprintf("rtmon-node-%d", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_monitor_status WHERE node_id = $1`, nodeID)
		_ = repo.Delete(context.Background(), folder.ID)
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO library_monitor_status (node_id, library_id, state) VALUES ($1, $2, 'monitoring')`,
		nodeID, folder.ID); err != nil {
		t.Fatalf("seed status row: %v", err)
	}
	var backend, detail string
	var directories int
	if err := pool.QueryRow(ctx,
		`SELECT backend, detail, directories FROM library_monitor_status WHERE node_id = $1 AND library_id = $2`,
		nodeID, folder.ID).Scan(&backend, &detail, &directories); err != nil {
		t.Fatalf("read status row: %v", err)
	}
	if backend != "" || detail != "" || directories != 0 {
		t.Fatalf("status defaults = (%q, %q, %d), want empty, empty, 0", backend, detail, directories)
	}

	if err := repo.Delete(ctx, folder.ID); err != nil {
		t.Fatalf("delete folder: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM library_monitor_status WHERE node_id = $1`, nodeID).Scan(&remaining); err != nil {
		t.Fatalf("count status rows: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("status rows after folder delete = %d, want 0", remaining)
	}
}

func TestFolderRepositoryTrickplay(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewFolderRepository(pool)
	suffix := time.Now().UnixNano()
	create := func(name string, enabled bool) *models.MediaFolder {
		t.Helper()
		folder, err := repo.Create(ctx, CreateFolderInput{
			Paths:            []string{fmt.Sprintf("/media/trickplay-%s-%d", name, suffix)},
			Type:             "movies",
			Name:             fmt.Sprintf("trickplay-%s-%d", name, suffix),
			TrickplayEnabled: enabled,
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), folder.ID) })
		return folder
	}
	off, on := create("off", false), create("on", true)
	if off.TrickplayEnabled || !on.TrickplayEnabled {
		t.Fatalf("created off=%v on=%v", off.TrickplayEnabled, on.TrickplayEnabled)
	}
	enable, disable := true, false
	newName := fmt.Sprintf("trickplay-renamed-%d", suffix)
	if err := repo.Update(ctx, on.ID, UpdateFolderInput{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, off.ID, UpdateFolderInput{TrickplayEnabled: &enable}); err != nil {
		t.Fatal(err)
	}
	folders, err := repo.ListByIDs(ctx, []int{off.ID, on.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if !f.TrickplayEnabled {
			t.Fatalf("folder %d lost its trickplay setting", f.ID)
		}
	}
	if err := repo.Update(ctx, on.ID, UpdateFolderInput{TrickplayEnabled: &disable}); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetByID(ctx, on.ID); got.TrickplayEnabled {
		t.Fatal("update to false did not apply")
	}
}
