package routeinventory

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureConfig(name string) Config {
	return Config{
		Root:       filepath.Join("testdata", "fixtures", name),
		ModulePath: "example.test/fixture",
		Listeners: []ListenerSpec{{
			ID:          "fixture",
			Description: "analyzer fixture",
			Dir:         "listener",
			Func:        "NewRouter",
		}},
		AuditDirs: []string{"listener"},
		ScanRoots: []string{"."},
	}
}

// servemuxFixtureConfig declares both halves of the root-listener shape: a chi
// listener and the http.ServeMux in front of it.
func servemuxFixtureConfig(name string) Config {
	return Config{
		Root:       filepath.Join("testdata", "fixtures", name),
		ModulePath: "example.test/fixture",
		Listeners: []ListenerSpec{
			{
				ID:          "api",
				Description: "analyzer fixture api listener",
				Dir:         "api",
				Func:        "NewRouter",
			},
			{
				ID:          "root",
				Kind:        ListenerKindServeMux,
				Description: "analyzer fixture root listener",
				Dir:         "root",
				Func:        "newRootHandler",
				Delegates:   map[string]string{"apiRouter": "api"},
			},
		},
		AuditDirs: []string{"api", "root"},
		ScanRoots: []string{"."},
	}
}

// rootOnlyFixtureConfig is the ServeMux half on its own, for fixtures that have
// no API listener beside it.
func rootOnlyFixtureConfig(name string) Config {
	cfg := servemuxFixtureConfig(name)
	cfg.Listeners = cfg.Listeners[1:]
	cfg.Listeners[0].Delegates = nil
	cfg.AuditDirs = []string{"root"}
	return cfg
}

func analyzeFixture(t *testing.T, name string) *Inventory {
	t.Helper()
	inv, err := Analyze(fixtureConfig(name))
	if err != nil {
		t.Fatalf("analyze %s: %v", name, err)
	}
	return inv
}

func TestAnalyzeEnumeratesConditionalAndHelperRoutes(t *testing.T) {
	inv := analyzeFixture(t, "basic")

	// Both branches of the admin `if` are present: a runtime walk of one
	// wiring could only ever show one of them.
	want := map[string]struct {
		conditions []string
		middleware []string
		origin     string
	}{
		"GET /api/v1/health": {
			conditions: nil,
			middleware: []string{"baseMiddleware"},
			origin:     "explicit",
		},
		"POST /api/v1/admin/things|enableAdmin": {
			conditions: []string{"enableAdmin"},
			middleware: []string{"baseMiddleware", "requireAdmin"},
			origin:     "explicit",
		},
		"POST /api/v1/admin/things|!(enableAdmin)": {
			conditions: []string{"!(enableAdmin)"},
			middleware: []string{"baseMiddleware"},
			origin:     "explicit",
		},
		"GET /api/v1/extras": {
			conditions: nil,
			middleware: []string{"baseMiddleware"},
			origin:     "explicit",
		},
	}

	got := map[string]Route{}
	for _, route := range inv.Routes {
		key := route.Method + " " + route.Path
		if len(route.Conditions) > 0 {
			key += "|" + strings.Join(route.Conditions, "&&")
		}
		got[key] = route
	}
	for key, expected := range want {
		route, ok := got[key]
		if !ok {
			t.Fatalf("missing route %q; inventory has %d routes", key, len(inv.Routes))
		}
		if route.MethodOrigin != expected.origin {
			t.Errorf("%s: method_origin = %q, want %q", key, route.MethodOrigin, expected.origin)
		}
		middleware := inv.MiddlewareFor(route)
		if strings.Join(middleware, ",") != strings.Join(expected.middleware, ",") {
			t.Errorf("%s: middleware = %v, want %v", key, middleware, expected.middleware)
		}
	}

	// Handle/HandleFunc registers every method; the inventory enumerates them
	// instead of hiding nine operations behind a wildcard row.
	wildcard := map[string]bool{}
	for _, route := range inv.Routes {
		if route.Path == "/api/v1/wildcard" {
			if route.MethodOrigin != "handle_all" {
				t.Errorf("wildcard route has method_origin %q", route.MethodOrigin)
			}
			wildcard[route.Method] = true
		}
	}
	if len(wildcard) != len(handleAllMethods) {
		t.Errorf("wildcard expanded to %d methods, want %d", len(wildcard), len(handleAllMethods))
	}
}

func TestAnalyzeResolvesHandlerIdentityAndKinds(t *testing.T) {
	inv := analyzeFixture(t, "basic")
	for _, route := range inv.Routes {
		if route.Method != "POST" || route.Path != "/api/v1/admin/things" {
			continue
		}
		if !route.HandlerResolved {
			t.Fatalf("handler not resolved: %+v", route)
		}
		if !strings.HasSuffix(route.Handler, "listener.Handlers).Create") {
			t.Errorf("handler = %q, want the Create method identity", route.Handler)
		}
		if route.RequestKind != KindJSON {
			t.Errorf("request_kind = %q, want %q", route.RequestKind, KindJSON)
		}
		if route.ResponseMediaKind != KindJSON {
			t.Errorf("response_media_kind = %q, want %q", route.ResponseMediaKind, KindJSON)
		}
		if route.SuccessStatuses != nil || route.ErrorCodes != nil {
			t.Errorf("statuses must be explicitly absent, got %v / %v", route.SuccessStatuses, route.ErrorCodes)
		}
		return
	}
	t.Fatal("POST /api/v1/admin/things not found")
}

// TestAnalyzeEnumeratesNewMuxListener proves chi.NewMux is the same
// constructor as chi.NewRouter as far as the inventory is concerned. Matching
// only the NewRouter spelling would drop every route on a NewMux listener.
func TestAnalyzeEnumeratesNewMuxListener(t *testing.T) {
	inv := analyzeFixture(t, "newmux_listener")
	for _, route := range inv.Routes {
		if route.Method == "GET" && route.Path == "/api/v1/from-mux" {
			return
		}
	}
	t.Fatalf("GET /api/v1/from-mux missing; inventory has %d routes", len(inv.Routes))
}

// TestAnalyzeEnumeratesServeMuxListener covers the process root listener: an
// http.ServeMux is a listener like any other, and the registrations it makes
// directly — /metrics above all — need rows of their own.
func TestAnalyzeEnumeratesServeMuxListener(t *testing.T) {
	inv, err := Analyze(servemuxFixtureConfig("servemux"))
	if err != nil {
		t.Fatalf("analyze servemux fixture: %v", err)
	}
	metrics := 0
	health := 0
	sawDelegation := false
	for _, route := range inv.Routes {
		if route.Listener != "root" {
			continue
		}
		switch route.Path {
		case "/metrics":
			metrics++
			if route.Namespace != NamespaceOperational {
				t.Errorf("/metrics namespace = %q, want %q", route.Namespace, NamespaceOperational)
			}
		case "/api/":
			sawDelegation = true
			if route.DelegatesTo != "api" {
				t.Errorf("/api/ delegates_to = %q, want api", route.DelegatesTo)
			}
			if route.AuthClass != authDelegated {
				t.Errorf("/api/ auth_class = %q, want %q", route.AuthClass, authDelegated)
			}
		case "/health":
			health++
		}
		// A method-less ServeMux pattern answers every method, so every row it
		// produces has to say it came from that and not from a chosen verb.
		if route.MethodOrigin != "handle_all" {
			t.Errorf("%s %s: method_origin = %q, want handle_all", route.Method, route.Path, route.MethodOrigin)
		}
	}
	if metrics != len(handleAllMethods) {
		t.Errorf("/metrics produced %d rows, want %d", metrics, len(handleAllMethods))
	}
	if health != len(handleAllMethods) {
		t.Errorf("/health produced %d rows, want %d", health, len(handleAllMethods))
	}
	if !sawDelegation {
		t.Error("no /api/ delegation row")
	}
}

// TestAnalyzeRefusesHiddenRegistration is the structural guarantee: every way
// a route could be registered outside the enumerated walk has to fail the
// generator rather than quietly shrink the inventory.
func TestAnalyzeRefusesHiddenRegistration(t *testing.T) {
	cases := []struct {
		fixture string
		cfg     func(string) Config
		want    string
	}{
		{fixture: "unreachable_helper", want: "never reached from a declared listener entry point"},
		{fixture: "escaping_router", want: "cannot follow"},
		{fixture: "loop_registration", want: "does not model"},
		{fixture: "dynamic_pattern", want: "must be a string literal"},
		{fixture: "stray_router", want: "outside the inventoried listeners"},
		// chi.NewMux is a router constructor too: a stray listener built with
		// it has to fail the same way one built with chi.NewRouter does.
		{fixture: "stray_mux", want: "chi.NewMux() constructed in Handler outside the inventoried listeners"},
		// A chi constructor the walk does not model may still return a router.
		{fixture: "unmodeled_chi", want: "a chi constructor the route inventory does not model"},
		// A second router in the entry point is attached somewhere the walk
		// cannot prove, so its rows would claim the wrong paths. The three
		// variants below are the same defect written in the binding forms a
		// walk that matched only `name := chi.NewRouter()` did not recognize:
		// each one used to be accepted, dropping /inner behind a wildcard.
		{fixture: "second_router", want: "a second chi router is constructed"},
		{fixture: "second_router_var", want: "a second chi router is constructed"},
		{fixture: "second_router_multi", want: "a second chi router is constructed"},
		{fixture: "second_router_paren", want: "a second chi router is constructed"},
		{fixture: "package_scope_router", want: "constructed at package scope"},
		// A listener handler passed to a call the audit cannot see into could
		// have anything registered on it, receiver or not.
		{fixture: "carrier_helper", want: "type-asserts a listener handler after its entry point returned it"},
		// A method-aware ServeMux pattern means more than the row it spells.
		{
			fixture: "servemux_method",
			cfg:     rootOnlyFixtureConfig,
			want:    "method-aware ServeMux pattern",
		},
		// A tracked router handed in as a handler is a mount in disguise.
		{fixture: "router_as_handler", want: "does not model"},
		// A registration made on the listener's handler after the entry point
		// returned it is invisible to the walk of that entry point.
		{fixture: "entrypoint_escape", want: "on a listener handler after its entry point returned it"},
		// The mux of a root listener escapes into a helper.
		{fixture: "servemux_escape", cfg: rootOnlyFixtureConfig, want: "does not model"},
		// A ServeMux built without http.NewServeMux() is a working router the
		// walk never bound: the root listener's own rows would vanish.
		{fixture: "servemux_new", cfg: rootOnlyFixtureConfig, want: "built by literal or new()"},
		{fixture: "servemux_literal", cfg: rootOnlyFixtureConfig, want: "built by literal or new()"},
		{fixture: "second_mux_new", want: "built by literal or new()"},
		{fixture: "chi_mux_literal", want: "built by literal or new()"},
		// A zero http.ServeMux at package scope is live with no constructor.
		{fixture: "package_scope_servemux_var", want: "declared with http.ServeMux at package scope"},
		// A constructor reached through a function value builds a router
		// nothing recognizes as a construction.
		{fixture: "ctor_value_local", want: "is referenced as a function value"},
		{fixture: "ctor_value_package", want: "is used as a function value rather than called"},
		{fixture: "ctor_value_arg", want: "is referenced as a function value"},
		{fixture: "dot_import", want: "dot-imports"},
		// The http.Handler vouch is backed by an audit of the callee body with
		// the parameter seeded as a carrier, so asserting it back is refused.
		{fixture: "handler_param_assert", want: "wire: type-asserts a listener handler"},
		// Every binding form the walk understands is a carrier binding too.
		{fixture: "carrier_var", want: "calls Get on a listener handler after its entry point returned it"},
		{fixture: "carrier_multi", want: "calls Get on a listener handler after its entry point returned it"},
		{fixture: "carrier_struct", want: "in a position the route inventory does not follow"},
		{fixture: "carrier_field_store", want: "stores a listener handler into field h"},
		{fixture: "carrier_slice", want: "escapes into a construct the route inventory does not model"},
		{fixture: "carrier_wrapper_return", want: "returns a listener handler"},
		{fixture: "carrier_method_value", want: "takes Get of a listener handler as a value"},
		// A package-level carrier is one everywhere in its package, whatever
		// the declaration order of the files and functions involved.
		{fixture: "carrier_package_var", want: "lateRegister: type-asserts a listener handler"},
		// Everything that would serve http.DefaultServeMux.
		{fixture: "default_servemux", want: "refers to http.DefaultServeMux"},
		{fixture: "pprof_import", want: "imports net/http/pprof"},
		{fixture: "listen_nil", want: "serves a nil handler"},
		{fixture: "server_no_handler", want: "built without a Handler"},
		// A second name for a router type, and a router-typed field outside
		// the audited packages.
		{fixture: "type_alias_chi", want: "type Router is declared with a chi router type"},
		{fixture: "struct_field_chi", want: "type Holder is declared with a chi router type"},
		// An exclusion covers one function, not the file it lives in.
		{
			fixture: "excluded_construct",
			cfg: func(name string) Config {
				cfg := fixtureConfig(name)
				cfg.Exclusions = []RouterExclusion{{
					File:   "compat/server.go",
					Func:   "NewCompat",
					Reason: "fixture compatibility listener",
				}}
				return cfg
			},
			want: "constructed in NewSneaky outside the inventoried listeners",
		},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			build := tc.cfg
			if build == nil {
				build = fixtureConfig
			}
			inv, err := Analyze(build(tc.fixture))
			if err == nil {
				t.Fatalf("expected a failure, got an inventory with %d routes", len(inv.Routes))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestAnalyzeAcceptsScopedCarriers is the other half of the carrier rules:
// carrier names are scoped per function, so an unrelated local that happens to
// share a name with a listener handler is not refused, and a same-directory
// function that takes the handler as http.Handler and only serves it is
// accepted after its body has been audited.
func TestAnalyzeAcceptsScopedCarriers(t *testing.T) {
	if _, err := Analyze(fixtureConfig("carrier_scoped_ok")); err != nil {
		t.Fatalf("scoped carriers should pass: %v", err)
	}
}

// TestAnalyzeAcceptsRecordedExclusion is the other half of the exclusion rule:
// the construction that was actually recorded must pass.
func TestAnalyzeAcceptsRecordedExclusion(t *testing.T) {
	cfg := fixtureConfig("excluded_construct")
	cfg.Root = filepath.Join("testdata", "fixtures", "excluded_construct")
	cfg.Exclusions = []RouterExclusion{
		{File: "compat/server.go", Func: "NewCompat", Reason: "fixture compatibility listener"},
		{File: "compat/server.go", Func: "NewSneaky", Reason: "fixture second listener, also recorded"},
	}
	inv, err := Analyze(cfg)
	if err != nil {
		t.Fatalf("recorded exclusions should pass: %v", err)
	}
	if len(inv.Exclusions) != 2 {
		t.Fatalf("exclusions = %v, want both recorded in the artifact", inv.Exclusions)
	}
}

func TestAnalyzeIsDeterministic(t *testing.T) {
	first := analyzeFixture(t, "basic")
	second := analyzeFixture(t, "basic")
	firstBytes, err := first.MarshalIndented()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.MarshalIndented()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("two runs over the same source produced different bytes")
	}
}

func TestClassifyAuthReportsUnknownMiddleware(t *testing.T) {
	class, traits := classifyAuth([]string{"middleware.RequestID", "somethingBrandNew"})
	if class != "public" {
		t.Errorf("class = %q, want public", class)
	}
	if !contains(traits, "unclassified_middleware") {
		t.Errorf("traits = %v, want unclassified_middleware", traits)
	}

	class, traits = classifyAuth([]string{"authMiddleware.RequireAuth", "requireActingAdmin", "deps.RateLimitMW.Handler"})
	if class != "acting_admin" {
		t.Errorf("class = %q, want acting_admin", class)
	}
	for _, want := range []string{"acting_admin", "authenticated", "rate_limited"} {
		if !contains(traits, want) {
			t.Errorf("traits = %v, want %q", traits, want)
		}
	}
}

func TestReconcileDetectsSeededDiscrepancy(t *testing.T) {
	inv := &Inventory{Routes: []Route{
		{Listener: ListenerAPI, Method: "GET", Path: "/api/v1/health"},
		{Listener: ListenerAPI, Method: "POST", Path: "/api/v1/auth/login"},
	}}
	observed := []string{"GET /api/v1/health", "POST /api/v1/auth/login"}
	if missing := inv.Reconcile(ListenerAPI, observed); len(missing) != 0 {
		t.Fatalf("clean reconciliation reported %v", missing)
	}

	// A route registered outside the enumerable structure shows up at runtime
	// with no inventory row behind it.
	observed = append(observed, "GET /api/v1/secretly-added")
	missing := inv.Reconcile(ListenerAPI, observed)
	if len(missing) != 1 || missing[0] != "GET /api/v1/secretly-added" {
		t.Fatalf("missing = %v, want the unledgered route", missing)
	}

	// Dropping a committed row is the same failure from the other side.
	inv.Routes = inv.Routes[:1]
	missing = inv.Reconcile(ListenerAPI, []string{"POST /api/v1/auth/login"})
	if len(missing) != 1 {
		t.Fatalf("missing = %v, want the dropped row to be reported", missing)
	}
}

// TestReconcileExactCatchesPhantomRows covers the direction the one-way check
// cannot: for a listener with no conditional rows, a row nothing registers is
// an invention, not a surplus.
func TestReconcileExactCatchesPhantomRows(t *testing.T) {
	inv := &Inventory{Routes: []Route{
		{Listener: ListenerProxy, Method: "GET", Path: "/healthz"},
		{Listener: ListenerProxy, Method: "GET", Path: "/phantom"},
	}}
	if count := inv.ConditionalCount(ListenerProxy); count != 0 {
		t.Fatalf("conditional count = %d, want 0", count)
	}
	unledgered, unobserved := inv.ReconcileExact(ListenerProxy, []string{"GET /healthz", "GET /extra"})
	if len(unledgered) != 1 || unledgered[0] != "GET /extra" {
		t.Errorf("unledgered = %v, want the route with no row", unledgered)
	}
	if len(unobserved) != 1 || unobserved[0] != "GET /phantom" {
		t.Errorf("unobserved = %v, want the row nothing registers", unobserved)
	}

	inv.Routes = inv.Routes[:1]
	if unledgered, unobserved := inv.ReconcileExact(ListenerProxy, []string{"GET /healthz"}); len(unledgered)+len(unobserved) != 0 {
		t.Errorf("clean equality reported %v / %v", unledgered, unobserved)
	}
}

func TestInventoryValidateRejectsDuplicates(t *testing.T) {
	inv := &Inventory{Routes: []Route{
		{Listener: ListenerAPI, Method: "GET", Path: "/x", HandlerExpr: "h"},
		{Listener: ListenerAPI, Method: "GET", Path: "/x", HandlerExpr: "h"},
	}}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected duplicate rows to be rejected")
	}
}

func contains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

// TestObservedServeMuxWalksRoutingTree pins the reflective ServeMux walk to the
// current net/http routing tree: every pattern registered has to come back,
// expanded to the nine methods a method-less pattern answers.
func TestObservedServeMuxWalksRoutingTree(t *testing.T) {
	mux := http.NewServeMux()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	patterns := []string{"/metrics", "/api/", "/", "/items/{id}/detail"}
	// Enough siblings to push the tree's child mapping from its slice form to
	// its map form, so both branches of the walk are exercised.
	for i := 0; i < 12; i++ {
		patterns = append(patterns, fmt.Sprintf("/many/%d", i))
	}
	for _, pattern := range patterns {
		mux.Handle(pattern, noop)
	}
	observed, err := ObservedServeMux(mux)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(patterns) * len(handleAllMethods); len(observed) != want {
		t.Fatalf("observed %d variants, want %d: %v", len(observed), want, observed)
	}
	seen := map[string]bool{}
	for _, entry := range observed {
		seen[entry] = true
	}
	for _, pattern := range patterns {
		for _, method := range handleAllMethods {
			if !seen[method+" "+pattern] {
				t.Errorf("missing %s %s", method, pattern)
			}
		}
	}

	// A method-aware pattern is refused by the walk for the same reason the
	// generator refuses it: one row would understate it.
	mux.Handle("GET /only", noop)
	if _, err := ObservedServeMux(mux); err == nil {
		t.Fatal("method-aware pattern should be refused")
	}
}
