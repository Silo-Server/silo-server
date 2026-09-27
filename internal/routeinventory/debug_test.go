package routeinventory

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilingImportAllowanceIsNarrow(t *testing.T) {
	s := &sweeper{a: &Analyzer{cfg: Config{Listeners: []ListenerSpec{debugListenerSpec()}}}}
	for _, tc := range []struct {
		file, alias, path string
		allowed           bool
	}{
		{debugServerHandlerFile, "pprof", "net/http/pprof", true},
		{debugServerHandlerFile, "_", "net/http/pprof", false},
		{debugServerHandlerFile, ".", "net/http/pprof", false},
		{debugServerHandlerFile, "prof", "net/http/pprof", false},
		{debugServerHandlerFile, "", "net/http/pprof", false},
		{debugServerHandlerFile, "pprof", "expvar", false},
		{"internal/debugserver/other.go", "pprof", "net/http/pprof", false},
		{"cmd/silo/main.go", "pprof", "net/http/pprof", false},
	} {
		spec := &ast.ImportSpec{}
		if tc.alias != "" {
			spec.Name = ast.NewIdent(tc.alias)
		}
		if got := s.allowedProfilingImport(tc.file, spec, tc.path); got != tc.allowed {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
	s.a.cfg.Listeners = nil
	if s.allowedProfilingImport(debugServerHandlerFile, &ast.ImportSpec{Name: ast.NewIdent("pprof")}, "net/http/pprof") {
		t.Fatal("import allowed without inventoried listener")
	}
}

func TestAnalyzeOperationalProfiler(t *testing.T) {
	for _, tc := range []struct{ name, wantError string }{
		{"pprof_allowed", ""},
		{"pprof_allowed_default", "serves a nil handler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Root: filepath.Join("testdata", "fixtures", tc.name), Listeners: []ListenerSpec{debugListenerSpec()}, AuditDirs: []string{internalDebugServerDir}}
			inv, err := Analyze(cfg)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("got %v, want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.Routes) != len(handleAllMethods) {
				t.Fatalf("routes: %v", inv.Routes)
			}
			for _, route := range inv.Routes {
				if route.Listener != ListenerDebug || route.Path != "/debug/pprof/heap" {
					t.Fatalf("unexpected route: %+v", route)
				}
			}
		})
	}
}
