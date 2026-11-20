// Command dbpins runs the DB-backed tests named in one or more lists, such
// as scripts/ci/db-pins.txt, and fails unless every one of them ran and
// passed.
//
// A DB-backed test skips when SILO_TEST_DATABASE_URL is unset or the database
// has not been migrated, and go test counts a skip as success. A budget that
// skips guards nothing, so this command fails on a listed test that is
// missing, skipped or failing, and on any skipped subtest.
//
// Each package or shard runs against its own copy of the database (see
// clone.go), so the database must have no other sessions; -shared runs every
// package against it directly instead.
//
// Usage (the repository root is the cwd; SILO_TEST_DATABASE_URL names a
// migrated, disposable database):
//
//	go run ./scripts/ci/dbpins -list scripts/ci/db-pins.txt -list scripts/ci/db-contracts.txt
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// listFlag collects every -list.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(path string) error {
	*l = append(*l, path)
	return nil
}

// pin is one listed test: a package directory such as ./internal/catalog and
// a top-level test name.
type pin struct {
	dir, test string
}

var testName = regexp.MustCompile(`^Test[A-Za-z0-9_]*$`)

func main() {
	if template := os.Getenv(cloneTemplateEnv); template != "" {
		os.Exit(runCloned(template, os.Args[1:]))
	}
	os.Exit(runMain())
}

func runMain() int {
	var lists listFlag
	flag.Var(&lists, "list", "file naming the pinned tests; repeat to run several lists in one pass")
	shared := flag.Bool("shared", false, "run every package against SILO_TEST_DATABASE_URL instead of a copy of it")
	flag.Parse()
	if len(lists) == 0 {
		lists = listFlag{"scripts/ci/db-pins.txt"}
	}

	pins, err := readLists(lists)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbpins: %v\n", err)
		return 1
	}
	// GOWORK=off: in a workspace, go list -m prints every module.
	listModule := exec.CommandContext(context.Background(), "go", "list", "-m")
	listModule.Env = append(os.Environ(), "GOWORK=off")
	module, err := listModule.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbpins: go list -m: %v\n", err)
		return 1
	}

	template := os.Getenv("SILO_TEST_DATABASE_URL")
	var (
		res      *results
		exitErr  error
		infraErr error
	)
	switch {
	case template == "":
		fmt.Println("dbpins: SILO_TEST_DATABASE_URL is not set, so every pin will skip")
		res, exitErr, infraErr = runDirect(pins)
	case *shared:
		res, exitErr, infraErr = runDirect(pins)
	default:
		// Keep SIGINT/SIGTERM trapped through dropRunClones so an interrupt
		// cancels queued shard starts and lets active wrappers finish before
		// sweeping any leftover database copies.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		execPath, run, setupErr := setUpClones(ctx, template)
		if run != "" {
			defer func() {
				if dropErr := dropRunClones(template, run); dropErr != nil {
					fmt.Fprintf(os.Stderr, "dbpins: %v\n", dropErr)
				}
			}()
		}
		if setupErr != nil {
			fmt.Fprintf(os.Stderr, "dbpins: %v\n", setupErr)
			return 1
		}
		res, exitErr, infraErr = runSharded(ctx, pins, execPath, template, run)
	}

	if res == nil {
		if infraErr != nil {
			fmt.Fprintf(os.Stderr, "dbpins: %v\n", infraErr)
		} else if exitErr != nil {
			fmt.Fprintf(os.Stderr, "dbpins: %v\n", exitErr)
		}
		return 1
	}

	report, problems := summarizeRun(res, pins, strings.TrimSpace(string(module)), exitErr, infraErr)
	fmt.Print(report)
	if len(problems) > 0 {
		fmt.Printf("\n::error::%d problem(s) running the pins in %s; a missing or skipped test fails like a failing one\n", len(problems), strings.Join(lists, " and "))
		for _, problem := range problems {
			fmt.Println("  " + problem)
		}
		return 1
	}
	fmt.Printf("\ndbpins: all %d pins passed\n", len(pins))
	return 0
}

func summarizeRun(res *results, pins []pin, module string, exitErr, infraErr error) (string, []string) {
	report, problems := res.check(pins, module)
	if infraErr != nil {
		problems = append(problems, "runner: "+infraErr.Error())
	} else if exitErr != nil && len(problems) == 0 {
		// A test binary or go test failed without a failing test to show for it.
		problems = append(problems, "go test: "+exitErr.Error())
	}
	return report, problems
}

func runDirect(pins []pin) (*results, error, error) {
	args := goTestArgs(pins)
	cmd := exec.CommandContext(context.Background(), "go", args...) //nolint:gosec // fixed go executable with separate arguments, no shell
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	res, readErr := readEvents(stdout, func(line string) { fmt.Print(line) })
	if readErr != nil {
		// Keep draining, or a go test still writing blocks on the full pipe
		// and Wait never returns.
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, nil, readErr
	}
	return res, waitErr, nil
}

// readLists parses each list and merges them; a test may be listed once
// across all of them.
func readLists(paths []string) ([]pin, error) {
	var pins []pin
	listedIn := map[pin]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // paths come from the -list flags of the make target or the developer
		if err != nil {
			return nil, err
		}
		listed, err := parseList(string(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, p := range listed {
			if other, ok := listedIn[p]; ok {
				return nil, fmt.Errorf("%s %s is listed in both %s and %s", p.dir, p.test, other, path)
			}
			listedIn[p] = path
			pins = append(pins, p)
		}
	}
	return pins, nil
}

// parseList reads one "<package dir> <TestName>" pair per line, ignoring blank
// lines and # comments.
func parseList(data string) ([]pin, error) {
	var pins []pin
	for i, line := range strings.Split(data, "\n") {
		text, _, _ := strings.Cut(line, "#")
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "./") || !testName.MatchString(fields[1]) {
			return nil, fmt.Errorf("line %d: want \"./<package dir> Test<Name>\", got %q", i+1, line)
		}
		p := pin{dir: strings.TrimSuffix(fields[0], "/"), test: fields[1]}
		if slices.Contains(pins, p) {
			return nil, fmt.Errorf("line %d: %s %s is listed twice", i+1, p.dir, p.test)
		}
		pins = append(pins, p)
	}
	if len(pins) == 0 {
		return nil, errors.New("lists no tests")
	}
	return pins, nil
}

// goTestArgs runs exactly the pinned tests in manifest order, uncached: the
// test cache cannot see the database the results depend on.
func goTestArgs(pins []pin) []string {
	var names, dirs []string
	for _, p := range pins {
		if !slices.Contains(names, p.test) {
			names = append(names, p.test)
		}
		if !slices.Contains(dirs, p.dir) {
			dirs = append(dirs, p.dir)
		}
	}
	return append([]string{"test", "-count=1", "-json", "-run", "^(" + strings.Join(names, "|") + ")$"}, dirs...)
}

// testEvent is the part of a go test -json event this command reads.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
	Elapsed float64
}

type testKey struct{ pkg, test string }

type testResult struct {
	action  string // pass, fail or skip; empty until the test ends
	elapsed float64
	output  []string
}

func (r *testResult) passed() bool {
	return r != nil && r.action == "pass"
}

func (r *testResult) status() string {
	switch {
	case r == nil:
		return "MISSING"
	case r.action == "":
		return "UNFINISHED"
	}
	return strings.ToUpper(r.action)
}

type results struct {
	tests map[testKey]*testResult
	order []testKey
}

// readEvents records the outcome and output of every test in a go test -json
// stream. Package-level output (build errors, ok and FAIL lines) and lines
// that are not events go straight to passthrough.
func readEvents(r io.Reader, passthrough func(string)) (*results, error) {
	res := &results{tests: map[testKey]*testResult{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev testEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil || ev.Action == "" {
			passthrough(scanner.Text() + "\n")
			continue
		}
		if ev.Test == "" {
			passthrough(ev.Output)
			continue
		}
		key := testKey{ev.Package, ev.Test}
		result := res.tests[key]
		if result == nil {
			result = &testResult{}
			res.tests[key] = result
			res.order = append(res.order, key)
		}
		switch ev.Action {
		case "output":
			result.output = append(result.output, ev.Output)
		case "pass", "fail", "skip":
			result.action = ev.Action
			result.elapsed = ev.Elapsed
		}
	}
	return res, scanner.Err()
}

// check reports the status of every pin and the output of every test that
// did not pass, and returns the problems: each pin that did not pass, and each
// other test (a subtest, or another test the name pattern matched) that
// skipped or failed. A skipped subtest leaves its parent passing, which is why
// those count on their own.
func (res *results) check(pins []pin, module string) (string, []string) {
	var report strings.Builder
	var problems []string
	pinned := map[testKey]bool{}
	report.WriteString("\n")
	for _, p := range pins {
		key := testKey{module + "/" + strings.TrimPrefix(p.dir, "./"), p.test}
		pinned[key] = true
		result := res.tests[key]
		if result.passed() {
			fmt.Fprintf(&report, "PASS       %s %s (%.2fs)\n", p.dir, p.test, result.elapsed)
			continue
		}
		problems = append(problems, fmt.Sprintf("%s %s %s", result.status(), p.dir, p.test))
		fmt.Fprintf(&report, "%-10s %s %s\n", result.status(), p.dir, p.test)
	}
	for _, key := range res.order {
		result := res.tests[key]
		if result.passed() {
			continue
		}
		if !pinned[key] {
			problems = append(problems, fmt.Sprintf("%s %s %s", result.status(), key.pkg, key.test))
		}
		fmt.Fprintf(&report, "\n--- %s %s %s\n%s", result.status(), key.pkg, key.test, strings.Join(result.output, ""))
	}
	return report.String(), problems
}
