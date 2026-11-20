package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// packageShards splits the slowest packages across isolated database copies
// so no single serial test binary holds up the run while other CPU slots sit
// idle. Tests are distributed round-robin so expensive tests in adjacent
// manifest sections spread across shards.
var packageShards = map[string]int{
	"./internal/backup":  4,
	"./internal/catalog": 2,
	"./internal/scanner": 2,
}

// packagePriority starts the longest-running packages first so shorter
// packages pack beside them instead of launching ahead of them.
var packagePriority = map[string]int{
	"./internal/backup":          100,
	"./internal/catalog":         90,
	"./internal/scanner":         80,
	"./internal/metadata":        70,
	"./cmd/silo":                 65,
	"./internal/recommendations": 60,
	"./internal/notifications":   55,
	"./internal/api/handlers":    50,
}

type testShard struct {
	dir   string
	tests []string
	index int // 1-based shard index when split; 0 when unsplit
	total int
}

func (s testShard) label() string {
	if s.total <= 1 {
		return s.dir
	}
	return fmt.Sprintf("%s [%d/%d]", s.dir, s.index, s.total)
}

func (s testShard) cloneTag() string {
	if s.total <= 1 {
		return s.dir
	}
	return fmt.Sprintf("%s_s%d", s.dir, s.index)
}

func (s testShard) runPattern() string {
	return "^(" + strings.Join(s.tests, "|") + ")$"
}

type compiledPackage struct {
	dir        string
	importPath string
	binary     string
}

// planShards groups pins by package, splits configured heavy packages
// round-robin, and orders the resulting shards longest-first.
func planShards(pins []pin) []testShard {
	var dirs []string
	byDir := make(map[string][]string)
	for _, p := range pins {
		if _, seen := byDir[p.dir]; !seen {
			dirs = append(dirs, p.dir)
		}
		byDir[p.dir] = append(byDir[p.dir], p.test)
	}

	var shards []testShard
	for _, dir := range dirs {
		tests := byDir[dir]
		n := min(max(packageShards[dir], 1), len(tests))
		if n == 1 {
			shards = append(shards, testShard{dir: dir, tests: tests, total: 1})
			continue
		}
		buckets := make([][]string, n)
		for i, name := range tests {
			buckets[i%n] = append(buckets[i%n], name)
		}
		for i, bucket := range buckets {
			shards = append(shards, testShard{
				dir:   dir,
				tests: bucket,
				index: i + 1,
				total: n,
			})
		}
	}

	slices.SortStableFunc(shards, func(a, b testShard) int {
		if c := cmp.Compare(packagePriority[b.dir], packagePriority[a.dir]); c != 0 {
			return c
		}
		if c := cmp.Compare(len(b.tests), len(a.tests)); c != 0 {
			return c
		}
		return cmp.Compare(a.index, b.index)
	})
	return shards
}

// batchPackagesByBasename partitions directories into collision-free batches
// for `go test -c -o <dir>/`, which names each output binary after the last
// segment of its import path.
func batchPackagesByBasename(pkgs []compiledPackage) [][]compiledPackage {
	var batches [][]compiledPackage
	var seen []map[string]bool
	for _, pkg := range pkgs {
		base := path.Base(pkg.importPath)
		placed := false
		for i := range batches {
			if !seen[i][base] {
				seen[i][base] = true
				batches[i] = append(batches[i], pkg)
				placed = true
				break
			}
		}
		if !placed {
			seen = append(seen, map[string]bool{base: true})
			batches = append(batches, []compiledPackage{pkg})
		}
	}
	return batches
}

func testBinaryExt() string {
	if runtime.GOOS == "windows" {
		return ".test.exe"
	}
	return ".test"
}

func compileTestBinaries(ctx context.Context, shards []testShard, binRoot string) (map[string]compiledPackage, error) {
	var dirs []string
	seenDir := make(map[string]bool)
	for _, s := range shards {
		if !seenDir[s.dir] {
			seenDir[s.dir] = true
			dirs = append(dirs, s.dir)
		}
	}

	listArgs := append([]string{"list", "-f", "{{.Dir}}\t{{.ImportPath}}"}, dirs...)
	listCmd := exec.CommandContext(ctx, "go", listArgs...) //nolint:gosec // fixed go binary with validated repo-relative package dirs
	var listStderr bytes.Buffer
	listCmd.Stderr = &listStderr
	listOut, err := listCmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(listStderr.String()); msg != "" {
			return nil, fmt.Errorf("go list: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("go list: %w", err)
	}
	importByAbsDir := make(map[string]string, len(dirs))
	for _, line := range strings.Split(strings.TrimSpace(string(listOut)), "\n") {
		if line == "" {
			continue
		}
		absDir, importPath, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("unexpected go list line %q", line)
		}
		importByAbsDir[filepath.Clean(absDir)] = strings.TrimSpace(importPath)
	}

	pkgs := make([]compiledPackage, len(dirs))
	for i, dir := range dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", dir, err)
		}
		importPath, ok := importByAbsDir[filepath.Clean(absDir)]
		if !ok {
			return nil, fmt.Errorf("go list did not return package for %s", dir)
		}
		pkgs[i] = compiledPackage{dir: dir, importPath: importPath}
	}

	ext := testBinaryExt()
	compiled := make(map[string]compiledPackage, len(pkgs))
	for batchIdx, batch := range batchPackagesByBasename(pkgs) {
		batchDir := filepath.Join(binRoot, fmt.Sprintf("b%d", batchIdx))
		if err := os.MkdirAll(batchDir, 0o700); err != nil {
			return nil, err
		}
		args := make([]string, 0, len(batch)+4)
		args = append(args, "test", "-c", "-o", batchDir+string(os.PathSeparator))
		for _, pkg := range batch {
			args = append(args, pkg.dir)
		}
		cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // fixed go binary compiling repository packages
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("compile test binaries: %w", err)
		}
		for _, pkg := range batch {
			pkg.binary = filepath.Join(batchDir, path.Base(pkg.importPath)+ext)
			compiled[pkg.dir] = pkg
		}
	}
	return compiled, nil
}

func runSharded(ctx context.Context, pins []pin, template string) (*results, string, error) {
	run, err := preflightClones(ctx, template)
	if err != nil {
		return nil, run, err
	}

	shards := planShards(pins)
	binRoot, err := os.MkdirTemp("", "dbpins-bins-*")
	if err != nil {
		return nil, run, err
	}
	defer func() { _ = os.RemoveAll(binRoot) }()

	compiled, err := compileTestBinaries(ctx, shards, binRoot)
	if err != nil {
		return nil, run, err
	}

	res := &results{tests: make(map[testKey]*testResult, len(pins))}
	var (
		cloneMu sync.Mutex
		resMu   sync.Mutex
		outMu   sync.Mutex
		errMu   sync.Mutex
		runErrs []error
	)

	work := make(chan testShard, len(shards))
	for _, s := range shards {
		work <- s
	}
	close(work)

	workers := min(max(runtime.GOMAXPROCS(0), 1), len(shards))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case s, ok := <-work:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}
					pkg := compiled[s.dir]
					if err := runOneShard(ctx, template, run, s, pkg, &cloneMu, &resMu, &outMu, res); err != nil {
						errMu.Lock()
						runErrs = append(runErrs, err)
						errMu.Unlock()
					}
				}
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		runErrs = append(runErrs, err)
	}
	return res, run, errors.Join(runErrs...)
}

func preflightClones(ctx context.Context, template string) (string, error) {
	run, err := newRunToken()
	if err != nil {
		return "", err
	}
	name, _, err := cloneDatabase(ctx, template, run, "preflight")
	if err != nil {
		return "", err
	}
	if err := dropDatabase(ctx, template, name); err != nil {
		return run, err
	}
	return run, nil
}

func runOneShard(
	ctx context.Context,
	template, run string,
	s testShard,
	pkg compiledPackage,
	cloneMu, resMu, outMu *sync.Mutex,
	merged *results,
) error {
	if _, err := os.Stat(pkg.binary); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s: stat test binary: %w", s.label(), err)
	}

	cloneMu.Lock()
	clone, cloneURL, err := cloneDatabase(ctx, template, run, s.cloneTag())
	cloneMu.Unlock()
	if err != nil {
		return fmt.Errorf("%s: %w", s.label(), err)
	}
	defer func() {
		if dropErr := dropDatabase(context.Background(), template, clone); dropErr != nil {
			outMu.Lock()
			fmt.Fprintf(os.Stderr, "dbpins: %v\n", dropErr)
			outMu.Unlock()
		}
	}()

	start := time.Now()
	cmd := exec.CommandContext( //nolint:gosec // fixed go tool test2json running a freshly compiled test binary
		ctx,
		"go", "tool", "test2json", "-p", pkg.importPath, "-t",
		pkg.binary,
		"-test.v=test2json",
		"-test.count=1",
		"-test.timeout=10m",
		"-test.run="+s.runPattern(),
	)
	var stderr bytes.Buffer
	cmd.Dir = s.dir
	cmd.Env = cloneEnv(os.Environ(), template, cloneURL)
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: %w", s.label(), err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", s.label(), err)
	}

	var passthrough strings.Builder
	shardRes, readErr := readEvents(stdout, func(line string) { passthrough.WriteString(line) })
	if readErr != nil {
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	resMu.Lock()
	merged.merge(shardRes)
	resMu.Unlock()

	outMu.Lock()
	if passthrough.Len() > 0 {
		fmt.Print(passthrough.String())
	}
	if stderr.Len() > 0 {
		_, _ = os.Stderr.Write(stderr.Bytes())
	}
	status := "ok  "
	if waitErr != nil {
		status = "FAIL"
	}
	fmt.Printf("%s\t%s\t%.3fs\n", status, shardSummaryName(pkg.importPath, s), elapsed.Seconds())
	outMu.Unlock()

	if readErr != nil {
		return fmt.Errorf("%s: %w", s.label(), readErr)
	}
	if waitErr != nil {
		return fmt.Errorf("%s: %w", s.label(), waitErr)
	}
	return nil
}

func shardSummaryName(importPath string, s testShard) string {
	if s.total <= 1 {
		return importPath
	}
	return fmt.Sprintf("%s [shard %d/%d]", importPath, s.index, s.total)
}

func (res *results) merge(other *results) {
	if other == nil {
		return
	}
	for _, key := range other.order {
		if _, exists := res.tests[key]; !exists {
			res.order = append(res.order, key)
		}
		res.tests[key] = other.tests[key]
	}
}
