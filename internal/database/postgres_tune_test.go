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
	tests := []struct {
		name             string
		memoryBudget     int64
		sharedBuffersMax int64
		cpus             int
		detectedCPUs     int
		want             map[string]string
	}{
		{
			name:         "mid-sized host reaches the default shared_buffers ceiling",
			memoryBudget: 16 * sizeGB,
			cpus:         8,
			want: map[string]string{
				"max_connections":                  "100",
				"shared_buffers":                   "2GB",
				"effective_cache_size":             "12GB",
				"maintenance_work_mem":             "1GB",
				"autovacuum_work_mem":              "256MB",
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
			},
		},
		{
			name:         "small host stays under the ceilings",
			memoryBudget: 2 * sizeGB,
			cpus:         4,
			want: map[string]string{
				"shared_buffers":       "512MB",
				"effective_cache_size": "1536MB",
				"maintenance_work_mem": "128MB",
				"autovacuum_work_mem":  "128MB",
				"wal_buffers":          "16MB",
				"huge_pages":           "off",
			},
		},
		{
			name:         "large host with an explicit CPU count",
			memoryBudget: 48 * sizeGB,
			cpus:         64,
			want: map[string]string{
				"shared_buffers":         "2GB",
				"effective_cache_size":   "36GB",
				"maintenance_work_mem":   "3GB",
				"autovacuum_work_mem":    "256MB",
				"work_mem":               "76725kB",
				"huge_pages":             "try",
				"max_worker_processes":   "64",
				"max_parallel_workers":   "64",
				"autovacuum_max_workers": "5",
				"io_workers":             "16",
			},
		},
		{
			name:             "raised ceiling",
			memoryBudget:     48 * sizeGB,
			sharedBuffersMax: 8 * sizeGB,
			cpus:             8,
			want: map[string]string{
				"shared_buffers":       "8GB",
				"effective_cache_size": "36GB",
			},
		},
		{
			// Workers follow the bounded count; work_mem keeps the detected
			// one, so bounding does not raise it.
			name:         "detected CPU count was bounded",
			memoryBudget: 16 * sizeGB,
			cpus:         16,
			detectedCPUs: 224,
			want: map[string]string{
				"max_worker_processes":   "16",
				"max_parallel_workers":   "16",
				"autovacuum_max_workers": "4",
				"io_workers":             "4",
				"work_mem":               "12945kB",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := PostgresTuneOptions{
				Enabled:               true,
				Profile:               postgresTuneProfileOLTP,
				MemoryBudgetBytes:     tt.memoryBudget,
				SharedBuffersMaxBytes: tt.sharedBuffersMax,
				CPUs:                  tt.cpus,
				DetectedCPUs:          tt.detectedCPUs,
				Connections:           100,
				Storage:               postgresTuneStorageNVMe,
				DBSize:                postgresTuneDBSizeMidRAM,
				OSType:                "linux",
			}
			byName := map[string]string{}
			for _, setting := range RecommendPostgresOLTPSettings(opts, 18) {
				byName[setting.Name] = setting.Value
			}
			for key, value := range tt.want {
				if byName[key] != value {
					t.Errorf("%s = %q, want %q", key, byName[key], value)
				}
			}
			if _, ok := byName["io_method"]; ok {
				t.Error("io_method should only be emitted when explicitly configured")
			}
		})
	}
}

// A container without a CPU limit reports every core of its host.
func TestDetectedPostgresTuneCPUsAreBounded(t *testing.T) {
	for detected, want := range map[int]int{0: 1, 8: 8, 224: 16} {
		if got := detectedPostgresTuneCPUs(detected); got != want {
			t.Errorf("detectedPostgresTuneCPUs(%d) = %d, want %d", detected, got, want)
		}
	}
}

// The startup settings are written before the database exists, so they must
// not change with its size, and they must leave out io_method, which would
// stop the server from starting if PostgreSQL rejected it.
func TestRecommendPostgresStartupSettings(t *testing.T) {
	opts := PostgresTuneOptions{
		Enabled:           true,
		Profile:           postgresTuneProfileOLTP,
		MemoryBudgetBytes: 16 * sizeGB,
		CPUs:              8,
		Connections:       300,
		Storage:           postgresTuneStorageNVMe,
		OSType:            "linux",
		IOMethod:          "io_uring",
	}
	want := []PostgresTuneSetting{
		{Name: "shared_buffers", Value: "2GB", RequiresRestart: true},
		{Name: "wal_buffers", Value: "16MB", RequiresRestart: true},
		{Name: "huge_pages", Value: "try", RequiresRestart: true},
		{Name: "max_worker_processes", Value: "8", RequiresRestart: true},
	}
	for _, dbSize := range []string{postgresTuneDBSizeAuto, postgresTuneDBSizeLessRAM, postgresTuneDBSizeMidRAM, postgresTuneDBSizeGreaterRAM} {
		opts.DBSize = dbSize
		if got := RecommendPostgresStartupSettings(opts, 18); !slices.Equal(got, want) {
			t.Fatalf("DBSize %s: startup settings = %v, want %v", dbSize, got, want)
		}
	}
}

func TestLoadPostgresTuneOptionsFromEnv(t *testing.T) {
	t.Setenv("POSTGRES_TUNE", "auto")
	t.Setenv("POSTGRES_TUNE_MEMORY", "8GB")
	t.Setenv("POSTGRES_TUNE_SHARED_BUFFERS_MAX", "512MB")
	t.Setenv("POSTGRES_TUNE_CPUS", "64")
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
	if opts.SharedBuffersMaxBytes != 512*sizeMB {
		t.Fatalf("SharedBuffersMaxBytes = %d, want %d", opts.SharedBuffersMaxBytes, 512*sizeMB)
	}
	// An explicit count is the operator's and is not bounded.
	if opts.CPUs != 64 || opts.DetectedCPUs != 0 {
		t.Fatalf("CPUs = %d, DetectedCPUs = %d, want 64 and 0", opts.CPUs, opts.DetectedCPUs)
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

// A ceiling PostgreSQL would refuse must not reach the omnibus startup file,
// where it would stop the server from starting.
func TestLoadPostgresTuneOptionsRejectsSharedBuffersMaxBelowPostgresMinimum(t *testing.T) {
	t.Setenv("POSTGRES_TUNE", "auto")
	t.Setenv("POSTGRES_TUNE_MEMORY", "8GB")
	t.Setenv("POSTGRES_TUNE_SHARED_BUFFERS_MAX", "64kB")

	if _, err := LoadPostgresTuneOptionsFromEnv(20); err == nil {
		t.Fatal("expected a ceiling below 128kB to be rejected")
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

// postgresStartupTuneSetting mirrors which tuned settings PostgreSQL reads
// only at start. A new major version can move a setting either way: a
// reloadable one in the startup file is wasted, and a restart-only one left
// out brings back the restart a new cluster needs.
func TestPostgresStartupSettingsMatchServerPostgres(t *testing.T) {
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
	major, err := postgresMajorVersion(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	// Enough memory and CPUs that every tuned setting is recommended.
	opts := PostgresTuneOptions{
		Enabled:           true,
		MemoryBudgetBytes: 64 * sizeGB,
		CPUs:              32,
		Connections:       100,
		Storage:           postgresTuneStorageSSD,
		DBSize:            postgresTuneDBSizeMidRAM,
		OSType:            "linux",
		IOMethod:          "worker",
	}
	for _, setting := range RecommendPostgresOLTPSettings(opts, major) {
		atStart, err := postgresSettingRequiresRestart(ctx, pool, setting.Name)
		if err != nil {
			t.Fatalf("%s: %v", setting.Name, err)
		}
		startup := postgresStartupTuneSetting(setting.Name, major)
		leftOut := setting.Name == "max_connections" || setting.Name == "io_method"
		if startup && !atStart {
			t.Errorf("%s is reloadable on PostgreSQL %d; drop it from the startup settings", setting.Name, major)
		}
		if atStart && !startup && !leftOut {
			t.Errorf("%s needs a restart on PostgreSQL %d; add it to the startup settings", setting.Name, major)
		}
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
