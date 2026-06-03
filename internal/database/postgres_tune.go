package database

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	postgresTuneProfileOLTP = "oltp"

	postgresTuneStorageHDD  = "hdd"
	postgresTuneStorageSSD  = "ssd"
	postgresTuneStorageSAN  = "san"
	postgresTuneStorageNVMe = "nvme"

	postgresTuneDBSizeLessRAM    = "less_ram"
	postgresTuneDBSizeMidRAM     = "mid_ram"
	postgresTuneDBSizeGreaterRAM = "greater_ram"
	postgresTuneDBSizeAuto       = "auto"

	defaultPostgresTuneMemoryBudgetPercent = 75

	sizeKB = int64(1024)
	sizeMB = 1024 * sizeKB
	sizeGB = 1024 * sizeMB
	sizeTB = 1024 * sizeGB
)

var managedPostgresTuneSettings = []string{
	"max_connections",
	"shared_buffers",
	"effective_cache_size",
	"maintenance_work_mem",
	"checkpoint_completion_target",
	"wal_buffers",
	"default_statistics_target",
	"random_page_cost",
	"effective_io_concurrency",
	"work_mem",
	"huge_pages",
	"jit",
	"wal_compression",
	"autovacuum_max_workers",
	"autovacuum_work_mem",
	"min_wal_size",
	"max_wal_size",
	"max_worker_processes",
	"max_parallel_workers_per_gather",
	"max_parallel_workers",
	"max_parallel_maintenance_workers",
	"io_method",
	"io_workers",
}

// PostgresTuneOptions controls automatic PostgreSQL tuning through ALTER SYSTEM.
type PostgresTuneOptions struct {
	Enabled             bool
	Profile             string
	MemoryBudgetBytes   int64
	DetectedMemoryBytes int64
	MemorySource        string
	MemoryBudgetPercent int
	CPUs                int
	Connections         int
	Storage             string
	DBSize              string
	OSType              string
	IOMethod            string
}

// PostgresTuneSetting is one recommended PostgreSQL parameter.
type PostgresTuneSetting struct {
	Name            string
	Value           string
	RequiresRestart bool
}

// PostgresTuneFailure records a setting that PostgreSQL rejected.
type PostgresTuneFailure struct {
	Name  string
	Value string
	Err   error
}

// PostgresTuneResult summarizes one tuning run.
type PostgresTuneResult struct {
	PostgresMajorVersion int
	DatabaseSizeBytes    int64
	DBSize               string
	Settings             []PostgresTuneSetting
	Reset                []string
	Applied              int
	Failures             []PostgresTuneFailure
	Reloaded             bool
	RestartRequired      []string
}

type postgresTuneMemoryBudget struct {
	BudgetBytes   int64
	DetectedBytes int64
	Source        string
	Percent       int
}

// LoadPostgresTuneOptionsFromEnv reads the opt-in PostgreSQL tuning environment.
func LoadPostgresTuneOptionsFromEnv(appMaxConnections int) (PostgresTuneOptions, error) {
	rawEnabled := firstNonEmptyEnv("POSTGRES_TUNE", "SILO_POSTGRES_TUNE")
	if rawEnabled == "" {
		return PostgresTuneOptions{}, nil
	}

	enabled, profileFromMode, err := parsePostgresTuneMode(rawEnabled)
	if err != nil {
		return PostgresTuneOptions{}, err
	}
	if !enabled {
		return PostgresTuneOptions{}, nil
	}

	profile := firstNonEmptyEnv("POSTGRES_TUNE_PROFILE", "SILO_POSTGRES_TUNE_PROFILE")
	if profile == "" {
		profile = profileFromMode
	}
	if profile == "" {
		profile = postgresTuneProfileOLTP
	}
	profile = strings.ToLower(profile)
	if profile != postgresTuneProfileOLTP {
		return PostgresTuneOptions{}, fmt.Errorf("unsupported POSTGRES_TUNE_PROFILE %q; only %q is supported", profile, postgresTuneProfileOLTP)
	}

	memory, err := loadPostgresTuneMemory()
	if err != nil {
		return PostgresTuneOptions{}, err
	}

	cpus, err := loadPostgresTuneCPUs()
	if err != nil {
		return PostgresTuneOptions{}, err
	}

	connections, err := loadPostgresTuneConnections(appMaxConnections)
	if err != nil {
		return PostgresTuneOptions{}, err
	}

	storage, err := normalizePostgresTuneStorage(firstNonEmptyEnv("POSTGRES_TUNE_STORAGE", "SILO_POSTGRES_TUNE_STORAGE"))
	if err != nil {
		return PostgresTuneOptions{}, err
	}

	dbSize, err := normalizePostgresTuneDBSize(firstNonEmptyEnv("POSTGRES_TUNE_DB_SIZE", "SILO_POSTGRES_TUNE_DB_SIZE"))
	if err != nil {
		return PostgresTuneOptions{}, err
	}

	osType := strings.ToLower(firstNonEmptyEnv("POSTGRES_TUNE_OS", "SILO_POSTGRES_TUNE_OS"))
	if osType == "" {
		osType = "linux"
	}

	ioMethod := strings.ToLower(firstNonEmptyEnv("POSTGRES_TUNE_IO_METHOD", "SILO_POSTGRES_TUNE_IO_METHOD"))

	return PostgresTuneOptions{
		Enabled:             true,
		Profile:             profile,
		MemoryBudgetBytes:   memory.BudgetBytes,
		DetectedMemoryBytes: memory.DetectedBytes,
		MemorySource:        memory.Source,
		MemoryBudgetPercent: memory.Percent,
		CPUs:                cpus,
		Connections:         connections,
		Storage:             storage,
		DBSize:              dbSize,
		OSType:              osType,
		IOMethod:            ioMethod,
	}, nil
}

// RecommendPostgresOLTPSettings returns pgtune-style OLTP recommendations.
func RecommendPostgresOLTPSettings(opts PostgresTuneOptions, postgresMajorVersion int) []PostgresTuneSetting {
	memoryBudgetKB := opts.MemoryBudgetBytes / sizeKB
	if memoryBudgetKB <= 0 {
		return nil
	}

	connections := opts.Connections
	if connections <= 0 {
		connections = 100
	}

	settings := make([]PostgresTuneSetting, 0, 24)
	sharedBuffers := memoryBudgetKB / 4
	effectiveCacheSize := (memoryBudgetKB * 3) / 4
	maintenanceWorkMem := memoryBudgetKB / 16
	if maintenanceWorkMem > 8*sizeGB/sizeKB {
		maintenanceWorkMem = 8 * sizeGB / sizeKB
	}

	settings = append(settings,
		PostgresTuneSetting{Name: "max_connections", Value: strconv.Itoa(connections)},
		PostgresTuneSetting{Name: "shared_buffers", Value: formatPostgresTuneKB(sharedBuffers)},
		PostgresTuneSetting{Name: "effective_cache_size", Value: formatPostgresTuneKB(effectiveCacheSize)},
		PostgresTuneSetting{Name: "maintenance_work_mem", Value: formatPostgresTuneKB(maintenanceWorkMem)},
		PostgresTuneSetting{Name: "checkpoint_completion_target", Value: "0.9"},
		PostgresTuneSetting{Name: "wal_buffers", Value: formatPostgresTuneKB(walBuffersForSharedBuffers(sharedBuffers))},
		PostgresTuneSetting{Name: "default_statistics_target", Value: "100"},
		PostgresTuneSetting{Name: "random_page_cost", Value: randomPageCost(opts.Storage, opts.DBSize)},
	)

	if effectiveIO := effectiveIOConcurrency(opts.OSType, opts.Storage); effectiveIO != "" {
		settings = append(settings, PostgresTuneSetting{Name: "effective_io_concurrency", Value: effectiveIO})
	}

	settings = append(settings,
		PostgresTuneSetting{Name: "work_mem", Value: formatPostgresTuneKB(workMem(memoryBudgetKB, sharedBuffers, connections, opts.CPUs, opts.DBSize))},
		PostgresTuneSetting{Name: "huge_pages", Value: hugePages(sharedBuffers, opts.OSType)},
	)

	if postgresMajorVersion >= 12 {
		settings = append(settings, PostgresTuneSetting{Name: "jit", Value: "off"})
	}
	if postgresMajorVersion >= 15 {
		settings = append(settings, PostgresTuneSetting{Name: "wal_compression", Value: "lz4"})
	} else if postgresMajorVersion >= 10 {
		settings = append(settings, PostgresTuneSetting{Name: "wal_compression", Value: "on"})
	}

	if autovacuumWorkers := autovacuumMaxWorkers(opts.CPUs); autovacuumWorkers != "" {
		settings = append(settings, PostgresTuneSetting{Name: "autovacuum_max_workers", Value: autovacuumWorkers})
	}
	if maintenanceWorkMem >= 2*sizeGB/sizeKB {
		settings = append(settings, PostgresTuneSetting{Name: "autovacuum_work_mem", Value: "2GB"})
	}

	settings = append(settings,
		PostgresTuneSetting{Name: "min_wal_size", Value: "2GB"},
		PostgresTuneSetting{Name: "max_wal_size", Value: "8GB"},
	)
	settings = append(settings, parallelWorkerSettings(opts.CPUs, postgresMajorVersion)...)

	if postgresMajorVersion >= 18 {
		if ioWorkers := ioWorkers(opts.CPUs); ioWorkers != "" {
			settings = append(settings, PostgresTuneSetting{Name: "io_workers", Value: ioWorkers})
		}
		if opts.IOMethod != "" && opts.IOMethod != "auto" {
			settings = append(settings, PostgresTuneSetting{Name: "io_method", Value: opts.IOMethod})
		}
	}

	return settings
}

// ApplyPostgresTuning applies pgtune-style OLTP recommendations with ALTER SYSTEM.
func ApplyPostgresTuning(ctx context.Context, pool *pgxpool.Pool, opts PostgresTuneOptions) (*PostgresTuneResult, error) {
	result := &PostgresTuneResult{}
	if !opts.Enabled {
		return result, nil
	}

	if opts.DBSize == postgresTuneDBSizeAuto {
		databaseSize, err := currentDatabaseSize(ctx, pool)
		if err != nil {
			return result, err
		}
		opts.DBSize = classifyPostgresTuneDBSize(databaseSize, opts.MemoryBudgetBytes)
		result.DatabaseSizeBytes = databaseSize
	}
	result.DBSize = opts.DBSize

	majorVersion, err := postgresMajorVersion(ctx, pool)
	if err != nil {
		return result, err
	}
	result.PostgresMajorVersion = majorVersion

	settings := RecommendPostgresOLTPSettings(opts, majorVersion)
	recommended := make(map[string]struct{}, len(settings))
	for _, setting := range settings {
		recommended[setting.Name] = struct{}{}
	}

	for _, name := range managedPostgresTuneSettings {
		if _, ok := recommended[name]; ok {
			continue
		}
		exists, err := postgresSettingExists(ctx, pool, name)
		if err != nil || !exists {
			continue
		}
		requiresRestart, _ := postgresSettingRequiresRestart(ctx, pool, name)
		if _, err := pool.Exec(ctx, fmt.Sprintf("ALTER SYSTEM RESET %s", name)); err != nil {
			result.Failures = append(result.Failures, PostgresTuneFailure{Name: name, Err: err})
			continue
		}
		result.Reset = append(result.Reset, name)
		result.Applied++
		if requiresRestart {
			result.RestartRequired = append(result.RestartRequired, name)
		}
	}

	for _, setting := range settings {
		requiresRestart, _ := postgresSettingRequiresRestart(ctx, pool, setting.Name)
		setting.RequiresRestart = requiresRestart
		result.Settings = append(result.Settings, setting)

		if _, err := pool.Exec(ctx, fmt.Sprintf("ALTER SYSTEM SET %s = %s", setting.Name, quotePostgresLiteral(setting.Value))); err != nil {
			result.Failures = append(result.Failures, PostgresTuneFailure{Name: setting.Name, Value: setting.Value, Err: err})
			continue
		}

		result.Applied++
		if requiresRestart {
			result.RestartRequired = append(result.RestartRequired, setting.Name)
		}
	}

	if result.Applied == 0 {
		return result, nil
	}

	if err := pool.QueryRow(ctx, "SELECT pg_reload_conf()").Scan(&result.Reloaded); err != nil {
		return result, err
	}
	if !result.Reloaded {
		return result, fmt.Errorf("pg_reload_conf returned false")
	}

	return result, nil
}

func parsePostgresTuneMode(raw string) (bool, string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on", "auto":
		return true, postgresTuneProfileOLTP, nil
	case postgresTuneProfileOLTP:
		return true, postgresTuneProfileOLTP, nil
	case "0", "false", "no", "off", "none", "disabled":
		return false, "", nil
	default:
		return false, "", fmt.Errorf("invalid POSTGRES_TUNE value %q", raw)
	}
}

func loadPostgresTuneMemory() (postgresTuneMemoryBudget, error) {
	raw := firstNonEmptyEnv("POSTGRES_TUNE_MEMORY", "SILO_POSTGRES_TUNE_MEMORY")
	if raw == "" || strings.EqualFold(raw, "auto") {
		mem, source, err := detectTotalMemoryBytes()
		if err != nil {
			return postgresTuneMemoryBudget{}, fmt.Errorf("POSTGRES_TUNE_MEMORY=auto: %w", err)
		}
		budgetPercent, err := loadPostgresTuneMemoryBudgetPercent()
		if err != nil {
			return postgresTuneMemoryBudget{}, err
		}
		budget := (mem * int64(budgetPercent)) / 100
		return postgresTuneMemoryBudget{
			BudgetBytes:   budget,
			DetectedBytes: mem,
			Source:        source,
			Percent:       budgetPercent,
		}, nil
	}

	mem, err := parsePostgresTuneByteSize(raw)
	if err != nil {
		return postgresTuneMemoryBudget{}, fmt.Errorf("invalid POSTGRES_TUNE_MEMORY: %w", err)
	}
	return postgresTuneMemoryBudget{
		BudgetBytes:   mem,
		DetectedBytes: mem,
		Source:        "env",
		Percent:       100,
	}, nil
}

func loadPostgresTuneMemoryBudgetPercent() (int, error) {
	raw := firstNonEmptyEnv("POSTGRES_TUNE_MEMORY_BUDGET_PERCENT", "SILO_POSTGRES_TUNE_MEMORY_BUDGET_PERCENT")
	if raw == "" {
		return defaultPostgresTuneMemoryBudgetPercent, nil
	}
	percent, err := strconv.Atoi(raw)
	if err != nil || percent < 1 || percent > 100 {
		return 0, fmt.Errorf("invalid POSTGRES_TUNE_MEMORY_BUDGET_PERCENT %q", raw)
	}
	return percent, nil
}

func loadPostgresTuneCPUs() (int, error) {
	raw := firstNonEmptyEnv("POSTGRES_TUNE_CPUS", "SILO_POSTGRES_TUNE_CPUS")
	if raw == "" || strings.EqualFold(raw, "auto") {
		cpus := runtime.NumCPU()
		if cpus < 1 {
			cpus = 1
		}
		return cpus, nil
	}

	cpus, err := strconv.Atoi(raw)
	if err != nil || cpus < 1 {
		return 0, fmt.Errorf("invalid POSTGRES_TUNE_CPUS %q", raw)
	}
	return cpus, nil
}

func loadPostgresTuneConnections(appMaxConnections int) (int, error) {
	minConnections := 1
	if appMaxConnections > 0 {
		minConnections = appMaxConnections + 20
	}

	raw := firstNonEmptyEnv("POSTGRES_TUNE_CONNECTIONS", "SILO_POSTGRES_TUNE_CONNECTIONS")
	if raw != "" {
		connections, err := strconv.Atoi(raw)
		if err != nil || connections < 1 {
			return 0, fmt.Errorf("invalid POSTGRES_TUNE_CONNECTIONS %q", raw)
		}
		if connections < minConnections {
			connections = minConnections
		}
		return connections, nil
	}

	connections := 100
	if minConnections > connections {
		connections = minConnections
	}
	return connections, nil
}

func normalizePostgresTuneStorage(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "ssd":
		return postgresTuneStorageSSD, nil
	case "hdd", "hard_drive_hdd":
		return postgresTuneStorageHDD, nil
	case "san":
		return postgresTuneStorageSAN, nil
	case "nvme", "optane":
		return postgresTuneStorageNVMe, nil
	default:
		return "", fmt.Errorf("invalid POSTGRES_TUNE_STORAGE %q", raw)
	}
}

func normalizePostgresTuneDBSize(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return postgresTuneDBSizeAuto, nil
	case "mid", "medium", "mid_ram":
		return postgresTuneDBSizeMidRAM, nil
	case "less", "less_ram", "fits_ram", "fits-in-ram":
		return postgresTuneDBSizeLessRAM, nil
	case "greater", "greater_ram", "larger_than_ram":
		return postgresTuneDBSizeGreaterRAM, nil
	default:
		return "", fmt.Errorf("invalid POSTGRES_TUNE_DB_SIZE %q", raw)
	}
}

func detectTotalMemoryBytes() (int64, string, error) {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		if mem, err := readCgroupMemoryLimit(path); err == nil && mem > 0 {
			return mem, path, nil
		}
	}

	if mem, err := readMeminfoTotalBytes("/host/proc/meminfo"); err == nil && mem > 0 {
		return mem, "/host/proc/meminfo", nil
	}

	mem, err := readMeminfoTotalBytes("/proc/meminfo")
	if err != nil {
		return 0, "", err
	}
	if runningInContainer() && mem > 128*sizeGB {
		return 0, "", fmt.Errorf("/proc/meminfo reports %s inside a container without a finite cgroup memory limit or /host/proc/meminfo mount; set POSTGRES_TUNE_MEMORY explicitly or mount /proc/meminfo:/host/proc/meminfo:ro", formatByteSize(mem))
	}
	return mem, "/proc/meminfo", nil
}

func readMeminfoTotalBytes(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemTotal line")
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * sizeKB, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemTotal not found")
}

func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	raw, err := os.ReadFile("/proc/1/cgroup")
	return err == nil && (strings.Contains(string(raw), "docker") || strings.Contains(string(raw), "kubepods"))
}

func readCgroupMemoryLimit(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "max" {
		return 0, fmt.Errorf("no cgroup memory limit")
	}
	mem, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	// Docker may expose a huge sentinel when no concrete memory limit is set.
	if mem <= 0 || mem > 1<<60 {
		return 0, fmt.Errorf("no concrete cgroup memory limit")
	}
	return mem, nil
}

func parsePostgresTuneByteSize(raw string) (int64, error) {
	normalized := strings.ToUpper(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, " ", "")
	if normalized == "" {
		return 0, fmt.Errorf("empty size")
	}

	units := []struct {
		suffix string
		value  int64
	}{
		{"TIB", sizeTB},
		{"TB", sizeTB},
		{"GIB", sizeGB},
		{"GB", sizeGB},
		{"MIB", sizeMB},
		{"MB", sizeMB},
		{"KIB", sizeKB},
		{"KB", sizeKB},
		{"T", sizeTB},
		{"G", sizeGB},
		{"M", sizeMB},
		{"K", sizeKB},
		{"B", 1},
	}

	for _, unit := range units {
		if !strings.HasSuffix(normalized, unit.suffix) {
			continue
		}
		number := strings.TrimSuffix(normalized, unit.suffix)
		if number == "" {
			return 0, fmt.Errorf("missing numeric size in %q", raw)
		}
		parsed, err := strconv.ParseFloat(number, 64)
		if err != nil {
			return 0, err
		}
		if parsed <= 0 {
			return 0, fmt.Errorf("size must be positive")
		}
		return int64(parsed * float64(unit.value)), nil
	}

	return 0, fmt.Errorf("size %q must include a unit such as GB or MB", raw)
}

func formatPostgresTuneKB(kb int64) string {
	if kb%(sizeGB/sizeKB) == 0 {
		return fmt.Sprintf("%dGB", kb/(sizeGB/sizeKB))
	}
	if kb%(sizeMB/sizeKB) == 0 {
		return fmt.Sprintf("%dMB", kb/(sizeMB/sizeKB))
	}
	return fmt.Sprintf("%dkB", kb)
}

func formatByteSize(bytes int64) string {
	switch {
	case bytes%sizeTB == 0:
		return fmt.Sprintf("%dTB", bytes/sizeTB)
	case bytes%sizeGB == 0:
		return fmt.Sprintf("%dGB", bytes/sizeGB)
	case bytes%sizeMB == 0:
		return fmt.Sprintf("%dMB", bytes/sizeMB)
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

func walBuffersForSharedBuffers(sharedBuffersKB int64) int64 {
	walBuffers := (sharedBuffersKB * 3) / 100
	maxWalBuffers := 16 * sizeMB / sizeKB
	if walBuffers > maxWalBuffers {
		walBuffers = maxWalBuffers
	}
	if walBuffers > 14*sizeMB/sizeKB && walBuffers < maxWalBuffers {
		walBuffers = maxWalBuffers
	}
	if walBuffers < 32 {
		walBuffers = 32
	}
	return walBuffers
}

func randomPageCost(storage, dbSize string) string {
	if dbSize == postgresTuneDBSizeLessRAM {
		return "1.1"
	}
	if storage == postgresTuneStorageHDD {
		return "4"
	}
	return "1.1"
}

func effectiveIOConcurrency(osType, storage string) string {
	if osType != "linux" {
		return ""
	}
	switch storage {
	case postgresTuneStorageHDD:
		return "2"
	case postgresTuneStorageSSD:
		return "200"
	case postgresTuneStorageSAN:
		return "300"
	case postgresTuneStorageNVMe:
		return "1000"
	default:
		return ""
	}
}

func workMem(memoryBudgetKB, sharedBuffersKB int64, connections, cpus int, dbSize string) int64 {
	parallelForWorkMem := cpus
	if parallelForWorkMem < 1 {
		parallelForWorkMem = 8
	}

	workMem := (memoryBudgetKB - sharedBuffersKB) / int64((connections+parallelForWorkMem)*3)
	switch dbSize {
	case postgresTuneDBSizeLessRAM:
		workMem = int64(math.Floor(float64(workMem) * 1.3))
	case postgresTuneDBSizeGreaterRAM:
		workMem = int64(math.Floor(float64(workMem) * 0.9))
	}

	minWorkMem := 4 * sizeMB / sizeKB
	if workMem < minWorkMem {
		return minWorkMem
	}
	return workMem
}

func currentDatabaseSize(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var size int64
	if err := pool.QueryRow(ctx, "SELECT pg_database_size(current_database())").Scan(&size); err != nil {
		return 0, err
	}
	return size, nil
}

func classifyPostgresTuneDBSize(databaseSizeBytes, memoryBudgetBytes int64) string {
	if databaseSizeBytes <= 0 || memoryBudgetBytes <= 0 {
		return postgresTuneDBSizeMidRAM
	}
	if databaseSizeBytes <= memoryBudgetBytes/2 {
		return postgresTuneDBSizeLessRAM
	}
	if databaseSizeBytes > memoryBudgetBytes {
		return postgresTuneDBSizeGreaterRAM
	}
	return postgresTuneDBSizeMidRAM
}

func hugePages(sharedBuffersKB int64, osType string) string {
	if osType == "mac" || osType == "darwin" {
		return "off"
	}
	if sharedBuffersKB >= 2*sizeGB/sizeKB {
		return "try"
	}
	return "off"
}

func autovacuumMaxWorkers(cpus int) string {
	if cpus >= 32 {
		return "5"
	}
	if cpus >= 16 {
		return "4"
	}
	return ""
}

func parallelWorkerSettings(cpus, postgresMajorVersion int) []PostgresTuneSetting {
	if cpus < 4 {
		return nil
	}

	workersPerGather := int(math.Ceil(float64(cpus) / 2))
	if workersPerGather > 4 {
		workersPerGather = 4
	}

	settings := []PostgresTuneSetting{
		{Name: "max_worker_processes", Value: strconv.Itoa(cpus)},
		{Name: "max_parallel_workers_per_gather", Value: strconv.Itoa(workersPerGather)},
	}
	if postgresMajorVersion >= 10 {
		settings = append(settings, PostgresTuneSetting{Name: "max_parallel_workers", Value: strconv.Itoa(cpus)})
	}
	if postgresMajorVersion >= 11 {
		parallelMaintenanceWorkers := int(math.Ceil(float64(cpus) / 2))
		if parallelMaintenanceWorkers > 4 {
			parallelMaintenanceWorkers = 4
		}
		settings = append(settings, PostgresTuneSetting{Name: "max_parallel_maintenance_workers", Value: strconv.Itoa(parallelMaintenanceWorkers)})
	}
	return settings
}

func ioWorkers(cpus int) string {
	if cpus < 1 {
		return ""
	}
	workers := cpus / 4
	if workers < 3 {
		workers = 3
	}
	if workers > 32 {
		workers = 32
	}
	if workers <= 3 {
		return ""
	}
	return strconv.Itoa(workers)
}

func postgresMajorVersion(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var raw string
	if err := pool.QueryRow(ctx, "SHOW server_version_num").Scan(&raw); err != nil {
		return 0, err
	}
	versionNum, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	return versionNum / 10000, nil
}

func postgresSettingRequiresRestart(ctx context.Context, pool *pgxpool.Pool, name string) (bool, error) {
	var requiresRestart bool
	err := pool.QueryRow(ctx, "SELECT context = 'postmaster' FROM pg_settings WHERE name = $1", name).Scan(&requiresRestart)
	return requiresRestart, err
}

func postgresSettingExists(ctx context.Context, pool *pgxpool.Pool, name string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_settings WHERE name = $1)", name).Scan(&exists)
	return exists, err
}

func quotePostgresLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}
