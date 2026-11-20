package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// packageShards splits the slowest packages across parallel `go test -exec`
// invocations (each receiving its own cloned database) so no single serial
// package binary dominates the CI tail. Tests are distributed round-robin so
// expensive tests in adjacent manifest sections spread across shards.
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

type testInvocation struct {
	label string
	dir   string // non-empty for single-package shard invocations
	tests []string
	index int // 1-based shard index when split
	total int
	args  []string
}

func goTestExecArgs(execPath string, names, dirs []string) []string {
	args := make([]string, 0, 7+len(dirs))
	args = append(args, "test", "-count=1", "-json", "-exec", execPath, "-run", "^("+strings.Join(names, "|")+")$")
	return append(args, dirs...)
}

// planInvocations partitions pins into:
//   - shardInvs: one `go test -json -exec` invocation per shard of each
//     configured heavy package, ordered longest-first; and
//   - mainInv: one `go test -json -exec` invocation for every unsplit package,
//     ordered by packagePriority then pin count (empty args if all pins were
//     in split packages).
func planInvocations(pins []pin, execPath string) (mainInv testInvocation, shardInvs []testInvocation) {
	var dirs []string
	byDir := make(map[string][]string)
	for _, p := range pins {
		if _, seen := byDir[p.dir]; !seen {
			dirs = append(dirs, p.dir)
		}
		byDir[p.dir] = append(byDir[p.dir], p.test)
	}

	var unsplitDirs, unsplitNames []string
	for _, dir := range dirs {
		tests := byDir[dir]
		n := min(max(packageShards[dir], 1), len(tests))
		if n == 1 {
			unsplitDirs = append(unsplitDirs, dir)
			for _, name := range tests {
				if !slices.Contains(unsplitNames, name) {
					unsplitNames = append(unsplitNames, name)
				}
			}
			continue
		}
		buckets := make([][]string, n)
		for i, name := range tests {
			buckets[i%n] = append(buckets[i%n], name)
		}
		for i, bucket := range buckets {
			shardInvs = append(shardInvs, testInvocation{
				label: fmt.Sprintf("%s [%d/%d]", dir, i+1, n),
				dir:   dir,
				tests: bucket,
				index: i + 1,
				total: n,
				args:  goTestExecArgs(execPath, bucket, []string{dir}),
			})
		}
	}

	slices.SortStableFunc(shardInvs, func(a, b testInvocation) int {
		if c := cmp.Compare(packagePriority[b.dir], packagePriority[a.dir]); c != 0 {
			return c
		}
		if c := cmp.Compare(len(b.tests), len(a.tests)); c != 0 {
			return c
		}
		return cmp.Compare(a.index, b.index)
	})

	if len(unsplitDirs) > 0 {
		slices.SortStableFunc(unsplitDirs, func(a, b string) int {
			if c := cmp.Compare(packagePriority[b], packagePriority[a]); c != 0 {
				return c
			}
			return cmp.Compare(len(byDir[b]), len(byDir[a]))
		})
		mainInv = testInvocation{
			label: "unsplit packages",
			args:  goTestExecArgs(execPath, unsplitNames, unsplitDirs),
		}
	}
	return mainInv, shardInvs
}

// goflagsParallelism reads any -p flag from GOFLAGS so callers setting
// GOFLAGS=-p=1 cap concurrent go test invocations too.
func goflagsParallelism(goflags string, defaultP int) int {
	p := max(defaultP, 1)
	fields := strings.Fields(goflags)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		var val string
		switch {
		case strings.HasPrefix(f, "-p="):
			val = strings.TrimPrefix(f, "-p=")
		case strings.HasPrefix(f, "--p="):
			val = strings.TrimPrefix(f, "--p=")
		case (f == "-p" || f == "--p") && i+1 < len(fields):
			i++
			val = fields[i]
		default:
			continue
		}
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			p = n
		}
	}
	return p
}

// runSharded fans out `go test -json -exec` across the split shards and the
// unsplit packages, merging every JSON event stream into one results table.
// Non-test infrastructure failures (start/pipe/scanner errors, cancellation,
// or a failing invocation that produced no test events) are returned in
// infraErr so callers report them alongside any MISSING pins.
func runSharded(ctx context.Context, pins []pin, execPath, template, run string) (*results, error, error) {
	mainInv, shardInvs := planInvocations(pins, execPath)
	env := append(os.Environ(), cloneTemplateEnv+"="+template, cloneRunEnv+"="+run)
	maxP := goflagsParallelism(os.Getenv("GOFLAGS"), max(runtime.GOMAXPROCS(0), 1))

	merged := &results{tests: make(map[testKey]*testResult, len(pins))}
	var (
		mu        sync.Mutex
		outMu     sync.Mutex
		exitErrs  []error
		infraErrs []error
	)

	record := func(inv testInvocation) {
		if ctx.Err() != nil {
			return
		}
		invRes, exitErr, infraErr := runGoTestInvocation(ctx, inv, env, &outMu)
		mu.Lock()
		merged.merge(invRes)
		if exitErr != nil {
			exitErrs = append(exitErrs, exitErr)
		}
		if infraErr != nil {
			infraErrs = append(infraErrs, infraErr)
		}
		mu.Unlock()
	}

	if maxP <= 1 {
		for _, inv := range shardInvs {
			record(inv)
		}
		if len(mainInv.args) > 0 {
			record(mainInv)
		}
	} else {
		runInvocationsParallel(ctx, mainInv, shardInvs, maxP, record)
	}

	if err := ctx.Err(); err != nil {
		infraErrs = append(infraErrs, err)
	}
	return merged, errors.Join(exitErrs...), errors.Join(infraErrs...)
}

func runInvocationsParallel(ctx context.Context, mainInv testInvocation, shardInvs []testInvocation, maxP int, record func(testInvocation)) {
	var wg sync.WaitGroup
	if len(mainInv.args) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(mainInv)
		}()
	}
	if len(shardInvs) > 0 {
		work := make(chan testInvocation, len(shardInvs))
		for _, inv := range shardInvs {
			work <- inv
		}
		close(work)
		workers := min(maxP, len(shardInvs))
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for inv := range work {
					if ctx.Err() != nil {
						return
					}
					record(inv)
				}
			}()
		}
	}
	wg.Wait()
}

func runGoTestInvocation(ctx context.Context, inv testInvocation, env []string, outMu *sync.Mutex) (*results, error, error) {
	cmd := exec.CommandContext(ctx, "go", inv.args...) //nolint:gosec // fixed go binary with validated repo-relative package dirs and self -exec wrapper
	cmd.Env = env
	cmd.Stderr = &lockedWriter{mu: outMu, w: os.Stderr}
	cmd.Cancel = func() error {
		if sigErr := cmd.Process.Signal(os.Interrupt); sigErr != nil {
			if errors.Is(sigErr, os.ErrProcessDone) {
				return sigErr
			}
			return cmd.Process.Kill()
		}
		return nil
	}
	// Fit forced kill + database clone cleanup inside CI cancel grace windows.
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("%s: stdout pipe: %w", inv.label, err)
		writeStderr(outMu, err)
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("%s: start go test: %w", inv.label, err)
		writeStderr(outMu, err)
		return nil, nil, err
	}

	invRes, readErr := readEvents(stdout, func(line string) {
		outMu.Lock()
		fmt.Print(line)
		outMu.Unlock()
	})
	if readErr != nil {
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()

	if readErr != nil {
		err := fmt.Errorf("%s: read test events: %w", inv.label, readErr)
		writeStderr(outMu, err)
		return invRes, nil, err
	}
	if waitErr == nil {
		return invRes, nil, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || invRes == nil || len(invRes.tests) == 0 {
		// Either go test failed to exit normally, or it failed before running
		// any tests (e.g. build error or cloneDatabase failure in -exec).
		err := fmt.Errorf("%s: %w", inv.label, waitErr)
		writeStderr(outMu, err)
		return invRes, nil, err
	}
	return invRes, fmt.Errorf("%s: %w", inv.label, waitErr), nil
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

func writeStderr(mu *sync.Mutex, err error) {
	mu.Lock()
	fmt.Fprintf(os.Stderr, "dbpins: %v\n", err)
	mu.Unlock()
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
