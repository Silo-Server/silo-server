package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testModule = "example.com/silo"

func TestParseList(t *testing.T) {
	pins, err := parseList("# comment\n\n./internal/a TestOne  # why\n./internal/b/ TestTwo\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []pin{{"./internal/a", "TestOne"}, {"./internal/b", "TestTwo"}}
	if !reflect.DeepEqual(pins, want) {
		t.Fatalf("pins = %v, want %v", pins, want)
	}
	for name, list := range map[string]string{
		"empty":          "# nothing\n",
		"one field":      "./internal/a\n",
		"three fields":   "./internal/a TestOne TestTwo\n",
		"not a dir":      "internal/a TestOne\n",
		"pattern":        "./internal/a TestOne.*\n",
		"subtest":        "./internal/a TestOne/sub\n",
		"not a test":     "./internal/a BenchmarkOne\n",
		"listed twice":   "./internal/a TestOne\n./internal/a/ TestOne\n",
		"no test prefix": "./internal/a One\n",
	} {
		if _, err := parseList(list); err == nil {
			t.Errorf("%s: parseList(%q) succeeded", name, list)
		}
	}
}

func TestGoTestArgsAnchorsEveryName(t *testing.T) {
	pins := []pin{{"./internal/b", "TestOne"}, {"./internal/a", "TestOne"}, {"./internal/a", "TestTwo"}}
	got := goTestArgs(pins, false)
	want := []string{"test", "-count=1", "-json", "-run", "^(TestOne|TestTwo)$", "./internal/a", "./internal/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if got := goTestArgs(pins, true); !reflect.DeepEqual(got[len(got)-2:], []string{"./internal/b", "./internal/a"}) {
		t.Fatalf("shared database package order changed: %q", got)
	}
}

func TestGoTestArgsPrioritizesSlowPackagesFirst(t *testing.T) {
	pins := []pin{
		{"./internal/small", "TestS1"},
		{"./internal/small", "TestS2"},
		{"./internal/small", "TestS3"},
		{"./cmd/silo", "TestCmd"},
		{"./internal/catalog", "TestC1"},
		{"./internal/catalog", "TestC2"},
		{"./internal/backup", "TestB1"},
	}
	got := goTestArgs(pins, false)
	wantDirs := []string{"./internal/backup", "./internal/catalog", "./cmd/silo", "./internal/small"}
	if dirs := got[len(got)-4:]; !reflect.DeepEqual(dirs, wantDirs) {
		t.Fatalf("dirs = %q, want %q", dirs, wantDirs)
	}
	wantShared := []string{"./internal/small", "./cmd/silo", "./internal/catalog", "./internal/backup"}
	if dirs := goTestArgs(pins, true); !reflect.DeepEqual(dirs[len(dirs)-4:], wantShared) {
		t.Fatalf("shared dirs = %q, want %q", dirs[len(dirs)-4:], wantShared)
	}
}

// The committed lists must parse and name each test once between them, so a
// malformed edit fails make test-go rather than only the CI job that has a
// database.
func TestCommittedListsParse(t *testing.T) {
	if _, err := readLists([]string{"../db-pins.txt", "../db-contracts.txt"}); err != nil {
		t.Fatal(err)
	}
}

func event(action, pkg, test, output string) string {
	line, _ := json.Marshal(testEvent{Action: action, Package: testModule + "/" + pkg, Test: test, Output: output})
	return string(line) + "\n"
}

// Build errors and ok/FAIL lines are package-level events; lines that are not
// events at all pass through unchanged.
func TestReadEventsPassesPackageOutputThrough(t *testing.T) {
	stream := `{"Action":"build-output","ImportPath":"example.com/silo/internal/a","Output":"a.go:1: syntax error\n"}` + "\n" +
		"not an event\n" +
		event("output", "internal/a", "", "FAIL\texample.com/silo/internal/a [build failed]\n")
	var passthrough strings.Builder
	if _, err := readEvents(strings.NewReader(stream), func(line string) { passthrough.WriteString(line) }); err != nil {
		t.Fatal(err)
	}
	want := "a.go:1: syntax error\nnot an event\nFAIL\texample.com/silo/internal/a [build failed]\n"
	if passthrough.String() != want {
		t.Fatalf("passthrough = %q, want %q", passthrough.String(), want)
	}
}

func TestCheck(t *testing.T) {
	pins := []pin{{"./internal/a", "TestOne"}, {"./internal/b", "TestTwo"}}
	passing := event("run", "internal/a", "TestOne", "") +
		event("pass", "internal/a", "TestOne", "") +
		event("run", "internal/b", "TestTwo", "") +
		event("run", "internal/b", "TestTwo/sub", "") +
		event("pass", "internal/b", "TestTwo/sub", "") +
		event("pass", "internal/b", "TestTwo", "")
	for _, tc := range []struct {
		name, stream string
		want         []string
		// reportHas is text the report must show, such as a skip reason.
		reportHas string
	}{
		{name: "all pass", stream: passing, reportHas: "PASS       ./internal/b TestTwo"},
		{
			name: "no database",
			stream: event("run", "internal/a", "TestOne", "") +
				event("output", "internal/a", "TestOne", "    a_test.go:9: SILO_TEST_DATABASE_URL is not set\n") +
				event("skip", "internal/a", "TestOne", "") +
				event("run", "internal/b", "TestTwo", "") +
				event("skip", "internal/b", "TestTwo", ""),
			want:      []string{"SKIP ./internal/a TestOne", "SKIP ./internal/b TestTwo"},
			reportHas: "SILO_TEST_DATABASE_URL is not set",
		},
		{
			name:   "renamed test",
			stream: event("run", "internal/a", "TestOne", "") + event("pass", "internal/a", "TestOne", ""),
			want:   []string{"MISSING ./internal/b TestTwo"},
		},
		{
			name: "same name in another package",
			stream: passing + event("run", "internal/b", "TestOne", "") +
				event("pass", "internal/b", "TestOne", "") +
				event("run", "internal/a", "TestTwo", "") +
				event("skip", "internal/a", "TestTwo", ""),
			want: []string{"SKIP " + testModule + "/internal/a TestTwo"},
		},
		{
			name: "skipped subtest",
			stream: strings.Replace(passing, event("pass", "internal/b", "TestTwo/sub", ""),
				event("skip", "internal/b", "TestTwo/sub", ""), 1),
			want: []string{"SKIP " + testModule + "/internal/b TestTwo/sub"},
		},
		{
			name: "failed subtest",
			stream: event("run", "internal/a", "TestOne", "") +
				event("pass", "internal/a", "TestOne", "") +
				event("run", "internal/b", "TestTwo", "") +
				event("run", "internal/b", "TestTwo/sub", "") +
				event("output", "internal/b", "TestTwo/sub", "    b_test.go:12: statements = 101, want 9\n") +
				event("fail", "internal/b", "TestTwo/sub", "") +
				event("fail", "internal/b", "TestTwo", ""),
			want: []string{"FAIL ./internal/b TestTwo", "FAIL " + testModule + "/internal/b TestTwo/sub"},
		},
		{
			name: "package stopped mid-test",
			stream: event("run", "internal/a", "TestOne", "") +
				event("pass", "internal/a", "TestOne", "") +
				event("run", "internal/b", "TestTwo", ""),
			want: []string{"UNFINISHED ./internal/b TestTwo"},
		},
		{
			name: "package failed outside tests (e.g. clone or TestMain failure)",
			stream: event("run", "internal/a", "TestOne", "") +
				event("pass", "internal/a", "TestOne", "") +
				event("output", "internal/b", "", "dbpins: copy database failed\n") +
				event("fail", "internal/b", "", ""),
			want: []string{
				"MISSING ./internal/b TestTwo",
				"FAIL " + testModule + "/internal/b (package failed outside tests)",
			},
		},
		{
			name:   "package failed in TestMain teardown after tests passed",
			stream: passing + event("fail", "internal/b", "", ""),
			want: []string{
				"FAIL " + testModule + "/internal/b (package failed outside tests)",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var passthrough strings.Builder
			res, err := readEvents(strings.NewReader(tc.stream), func(line string) { passthrough.WriteString(line) })
			if err != nil {
				t.Fatal(err)
			}
			report, got := res.check(pins, testModule)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("problems = %q, want %q\npassthrough:\n%s\nreport:\n%s", got, tc.want, passthrough.String(), report)
			}
			if !strings.Contains(report, tc.reportHas) {
				t.Fatalf("report does not show %q:\n%s", tc.reportHas, report)
			}
		})
	}
}

func TestSummarizeRunReportsNonExitWaitErrorsAlongsideMissingPins(t *testing.T) {
	res := &results{tests: map[testKey]*testResult{}, failedPackages: map[string]bool{}}
	pins := []pin{{"./internal/backup", "TestA"}}
	_, problems := summarizeRun(res, pins, testModule, errors.New("signal: killed"))
	want := []string{
		"MISSING ./internal/backup TestA",
		"go test: signal: killed",
	}
	if !reflect.DeepEqual(problems, want) {
		t.Fatalf("problems = %q, want %q", problems, want)
	}
}

// cloneEnv drops the wrapper's own variables and repoints a database variable
// only when it names the template, however its host, port and options are
// spelled.
func TestCloneEnv(t *testing.T) {
	const template = "postgres://u:p@localhost:5432/silo_test?sslmode=disable"
	const clone = "postgres://u:p@localhost:5432/dbpins_x?sslmode=disable"
	got := cloneEnv([]string{
		cloneTemplateEnv + "=" + template,
		cloneRunEnv + "=0123abcd",
		"SILO_TEST_DATABASE_URL=" + template,
		"SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL=postgresql://u:p@127.0.0.1/other?dbname=silo_test",
		"OTHER=" + template,
	}, template, clone)
	want := []string{
		"SILO_TEST_DATABASE_URL=" + clone,
		"SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL=" + clone,
		"OTHER=" + template,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %q, want %q", got, want)
	}
	for _, other := range []string{
		"postgres://u:p@localhost:5432/subtitles",
		"postgres://u:p@localhost:5432/silo_test?host=other-server",
		"postgres://u:p@localhost:5432/silo_test?database=subtitles",
	} {
		got := cloneEnv([]string{"SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL=" + other}, template, clone)
		if got[0] != "SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL="+other {
			t.Fatalf("a different database was repointed: %q", got)
		}
	}
}

// A dbname or database query parameter overrides the URL path, so pointing
// a URL at the copy, or at the maintenance database, must drop it.
func TestWithDatabaseDropsTheDatabaseOverride(t *testing.T) {
	for _, override := range []string{"dbname", "database"} {
		template := "postgres://u:p@localhost/ignored?" + override + "=silo_test&sslmode=disable"
		if name, err := databaseName(template); err != nil || name != "silo_test" {
			t.Fatalf("%s: databaseName = %q, %v; want silo_test", override, name, err)
		}
		got, err := withDatabase(template, "postgres")
		if err != nil {
			t.Fatal(err)
		}
		if got != "postgres://u:p@localhost/postgres?sslmode=disable" {
			t.Fatalf("%s: withDatabase = %q", override, got)
		}
	}
}

// Postgres truncates names past 63 bytes, which would make two copies collide.
func TestCloneNameFitsPostgres(t *testing.T) {
	name, err := cloneName("0123abcd", "/home/runner/work/silo-server/silo-server/internal/a_very_long_parent_package_name/another_long_package_name")
	if err != nil {
		t.Fatal(err)
	}
	if len(name) > 63 || !strings.HasPrefix(name, clonePrefix+"0123abcd_") || !strings.Contains(name, "another_long_package_name_") {
		t.Fatalf("clone name %q (%d bytes)", name, len(name))
	}
}

func TestExecArg(t *testing.T) {
	for input, want := range map[string]string{
		"/tmp/dbpins":       "/tmp/dbpins",
		"/tmp/my dir/bin":   "'/tmp/my dir/bin'",
		"/tmp/o'brien/bin":  `"/tmp/o'brien/bin"`,
		`/tmp/a "b"/dbpins`: `'/tmp/a "b"/dbpins'`,
	} {
		got, err := execArg(input)
		if err != nil || got != want {
			t.Fatalf("execArg(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := execArg(`/tmp/a'b"c/dbpins`); err == nil {
		t.Fatal("execArg accepted path with both quote kinds")
	}
}
