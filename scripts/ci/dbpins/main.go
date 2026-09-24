// Command dbpins runs the DB-backed query-budget pins listed in
// scripts/ci/db-pins.txt and fails unless every one of them ran and passed.
//
// A DB-backed test skips when SILO_TEST_DATABASE_URL is unset or the database
// has not been migrated, and go test counts a skip as success. A budget that
// skips guards nothing, so this command fails on a listed test that is
// missing, skipped or failing, and on any skipped subtest.
//
// Usage (the repository root is the cwd; SILO_TEST_DATABASE_URL names a
// migrated, disposable database):
//
//	go run ./scripts/ci/dbpins -list scripts/ci/db-pins.txt
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// pin is one listed test: a package directory such as ./internal/catalog and
// a top-level test name.
type pin struct {
	dir, test string
}

var testName = regexp.MustCompile(`^Test[A-Za-z0-9_]*$`)

func main() {
	list := flag.String("list", "scripts/ci/db-pins.txt", "file naming the pinned tests")
	flag.Parse()

	data, err := os.ReadFile(*list)
	if err != nil {
		fail(err)
	}
	pins, err := parseList(string(data))
	if err != nil {
		fail(fmt.Errorf("%s: %w", *list, err))
	}
	// GOWORK=off: in a workspace, go list -m prints every module.
	listModule := exec.Command("go", "list", "-m")
	listModule.Env = append(os.Environ(), "GOWORK=off")
	module, err := listModule.Output()
	if err != nil {
		fail(fmt.Errorf("go list -m: %w", err))
	}
	if os.Getenv("SILO_TEST_DATABASE_URL") == "" {
		fmt.Println("dbpins: SILO_TEST_DATABASE_URL is not set, so every pin will skip")
	}

	cmd := exec.Command("go", goTestArgs(pins)...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail(err)
	}
	if err := cmd.Start(); err != nil {
		fail(err)
	}
	res, readErr := readEvents(stdout, func(line string) { fmt.Print(line) })
	if readErr != nil {
		// Keep draining, or a go test still writing blocks on the full pipe
		// and Wait never returns.
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		fail(readErr)
	}

	report, problems := res.check(pins, strings.TrimSpace(string(module)))
	fmt.Print(report)
	if waitErr != nil && len(problems) == 0 {
		// go test failed without a failing test to show for it.
		problems = append(problems, "go test: "+waitErr.Error())
	}
	if len(problems) > 0 {
		fmt.Printf("\n::error::%d problem(s) running the pins in %s; a missing or skipped test fails like a failing one\n", len(problems), *list)
		for _, problem := range problems {
			fmt.Println("  " + problem)
		}
		os.Exit(1)
	}
	fmt.Printf("\ndbpins: all %d pins passed\n", len(pins))
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "dbpins: %v\n", err)
	os.Exit(1)
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

// goTestArgs runs exactly the pinned tests, uncached: the test cache cannot
// see the database the results depend on.
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
