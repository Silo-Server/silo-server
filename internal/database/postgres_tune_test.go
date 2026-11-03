package database

import (
	"context"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecommendPostgresOLTPSettings(t *testing.T) {
	opts := PostgresTuneOptions{
		Enabled:           true,
		Profile:           postgresTuneProfileOLTP,
		MemoryBudgetBytes: 16 * sizeGB,
		CPUs:              8,
		Connections:       100,
		Storage:           postgresTuneStorageNVMe,
		DBSize:            postgresTuneDBSizeMidRAM,
		OSType:            "linux",
	}

	settings := RecommendPostgresOLTPSettings(opts, 18)
	byName := map[string]string{}
	for _, setting := range settings {
		byName[setting.Name] = setting.Value
	}

	expected := map[string]string{
		"max_connections":                  "100",
		"shared_buffers":                   "4GB",
		"effective_cache_size":             "12GB",
		"maintenance_work_mem":             "1GB",
		"wal_buffers":                      "16MB",
		"min_wal_size":                     "2GB",
		"max_wal_size":                     "8GB",
		"random_page_cost":                 "1.1",
		"effective_io_concurrency":         "1000",
		"work_mem":                         "38836kB",
		"huge_pages":                       "try",
		"jit":                              "off",
		"wal_compression":                  "lz4",
		"max_worker_processes":             "8",
		"max_parallel_workers_per_gather":  "4",
		"max_parallel_workers":             "8",
		"max_parallel_maintenance_workers": "4",
	}

	for key, value := range expected {
		if byName[key] != value {
			t.Fatalf("%s = %q, want %q", key, byName[key], value)
		}
	}

	if _, ok := byName["io_method"]; ok {
		t.Fatal("io_method should only be emitted when explicitly configured")
	}
}

func TestLoadPostgresTuneOptionsFromEnv(t *testing.T) {
	t.Setenv("POSTGRES_TUNE", "auto")
	t.Setenv("POSTGRES_TUNE_MEMORY", "8GB")
	t.Setenv("POSTGRES_TUNE_CPUS", "4")
	t.Setenv("POSTGRES_TUNE_STORAGE", "hdd")
	t.Setenv("POSTGRES_TUNE_DB_SIZE", "greater_ram")

	opts, err := LoadPostgresTuneOptionsFromEnv(20)
	if err != nil {
		t.Fatal(err)
	}

	if !opts.Enabled {
		t.Fatal("expected tuning to be enabled")
	}
	if opts.MemoryBudgetBytes != 8*sizeGB {
		t.Fatalf("MemoryBudgetBytes = %d, want %d", opts.MemoryBudgetBytes, 8*sizeGB)
	}
	if opts.DetectedMemoryBytes != 8*sizeGB {
		t.Fatalf("DetectedMemoryBytes = %d, want %d", opts.DetectedMemoryBytes, 8*sizeGB)
	}
	if opts.MemorySource != "env" {
		t.Fatalf("MemorySource = %q, want env", opts.MemorySource)
	}
	if opts.MemoryBudgetPercent != 100 {
		t.Fatalf("MemoryBudgetPercent = %d, want 100", opts.MemoryBudgetPercent)
	}
	if opts.CPUs != 4 {
		t.Fatalf("CPUs = %d, want 4", opts.CPUs)
	}
	if opts.Connections != 100 {
		t.Fatalf("Connections = %d, want 100", opts.Connections)
	}
	if opts.Storage != postgresTuneStorageHDD {
		t.Fatalf("Storage = %q, want %q", opts.Storage, postgresTuneStorageHDD)
	}
	if opts.DBSize != postgresTuneDBSizeGreaterRAM {
		t.Fatalf("DBSize = %q, want %q", opts.DBSize, postgresTuneDBSizeGreaterRAM)
	}
}

func TestLoadPostgresTuneOptionsUsesApplicationPoolForHighConnectionCounts(t *testing.T) {
	t.Setenv("POSTGRES_TUNE", "oltp")
	t.Setenv("POSTGRES_TUNE_MEMORY", "8GB")
	t.Setenv("POSTGRES_TUNE_CPUS", "4")

	opts, err := LoadPostgresTuneOptionsFromEnv(120)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Connections != 140 {
		t.Fatalf("Connections = %d, want 140", opts.Connections)
	}
}

func TestLoadPostgresTuneOptionsFloorsExplicitConnectionsAtApplicationPoolHeadroom(t *testing.T) {
	t.Setenv("POSTGRES_TUNE", "oltp")
	t.Setenv("POSTGRES_TUNE_MEMORY", "8GB")
	t.Setenv("POSTGRES_TUNE_CPUS", "4")
	t.Setenv("POSTGRES_TUNE_CONNECTIONS", "80")

	opts, err := LoadPostgresTuneOptionsFromEnv(90)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Connections != 110 {
		t.Fatalf("Connections = %d, want 110", opts.Connections)
	}
}

func TestPostgresTuneDisabledByDefault(t *testing.T) {
	opts, err := LoadPostgresTuneOptionsFromEnv(20)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Enabled {
		t.Fatal("expected tuning to be disabled by default")
	}
}

func TestClassifyPostgresTuneDBSize(t *testing.T) {
	tests := []struct {
		name       string
		dbSize     int64
		memBudget  int64
		wantDBSize string
	}{
		{name: "comfortably fits", dbSize: 8 * sizeGB, memBudget: 24 * sizeGB, wantDBSize: postgresTuneDBSizeLessRAM},
		{name: "fits without headroom", dbSize: 18 * sizeGB, memBudget: 24 * sizeGB, wantDBSize: postgresTuneDBSizeMidRAM},
		{name: "larger than budget", dbSize: 35 * sizeGB, memBudget: 24 * sizeGB, wantDBSize: postgresTuneDBSizeGreaterRAM},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPostgresTuneDBSize(tt.dbSize, tt.memBudget); got != tt.wantDBSize {
				t.Fatalf("classifyPostgresTuneDBSize() = %q, want %q", got, tt.wantDBSize)
			}
		})
	}
}

func TestParsePostgresTuneByteSizeRequiresUnit(t *testing.T) {
	if _, err := parsePostgresTuneByteSize("16"); err == nil {
		t.Fatal("expected bare memory value to be rejected")
	}
}

// ALTER SYSTEM writes postgresql.auto.conf for the whole cluster but changes
// nothing until a reload or restart, which this test never does. It restores
// the file's max_worker_processes entry when it finishes.
func TestPostgresSettingsPendingRestartPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const name = "max_worker_processes"
	var running int
	var source string
	if err := pool.QueryRow(ctx, "SELECT setting::int, source FROM pg_settings WHERE name = $1", name).Scan(&running, &source); err != nil {
		t.Fatal(err)
	}
	if source != "default" {
		t.Skipf("%s runs from %s; the test needs its built-in default", name, source)
	}
	var saved *string
	if err := pool.QueryRow(ctx, `SELECT (SELECT setting FROM pg_file_settings
WHERE name = $1 AND sourcefile LIKE '%postgresql.auto.conf' ORDER BY seqno DESC LIMIT 1)`, name).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		statement := "ALTER SYSTEM RESET " + name
		if saved != nil {
			statement = "ALTER SYSTEM SET " + name + " = " + quotePostgresLiteral(*saved)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Error(err)
		}
	})

	names := []string{name, "max_connections"}
	for _, tt := range []struct {
		name, statement string
		want            []string
	}{
		{name: "set to the running value", statement: "ALTER SYSTEM SET " + name + " = " + strconv.Itoa(running)},
		{name: "set to another value", statement: "ALTER SYSTEM SET " + name + " = " + strconv.Itoa(running+1), want: []string{name}},
		{name: "reset while running on the default", statement: "ALTER SYSTEM RESET " + name},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tt.statement); err != nil {
				t.Fatal(err)
			}
			got, err := postgresSettingsPendingRestart(ctx, pool, names)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("pending = %v, want %v", got, tt.want)
			}
		})
	}
}
