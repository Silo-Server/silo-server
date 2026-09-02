// Package scenariocatalog loads, validates, and gates the tier-1 scenario
// catalogs under contracts/api/v2/scenarios.
//
// Three checks make up the gate:
//
//   - every catalog satisfies scenario-catalog.schema.json;
//   - every row a catalog names exists in the migration ledger with tier 1
//     (or is an explicitly listed tier-2 inclusion), and its listener and
//     route group agree with the ledger;
//   - every tier-1 ledger row in a wave whose catalogs exist has a catalog
//     row with at least one scenario per category, or a reason under
//     not_applicable for each category it does not cover, and at least one
//     status_headers or authorization scenario so the row is executable.
//
// The executor that runs the scenarios against the real router lives in the
// executor subpackage so the gate stays free of handler dependencies.
//
// Summarize reports coverage counts for the gate's log. Its
// offline_candidates figure applies OfflineCandidate, which is everything the
// catalog and ledger can say about running without a database: the scenario
// needs none itself and its row is not the rate-limited registration variant.
// The executor's offline run is authoritative and its count is at most the
// gate's, because the executor also drops rows the offline router never
// registers (handlers that exist only with a user store), which the gate
// cannot see without building the router.
package scenariocatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Silo-Server/silo-server/contracts/api/v2/scenarios"
	"github.com/Silo-Server/silo-server/internal/contractledger"
)

// Category is one of the scenario classes the plan names for tier-1 rows.
type Category string

// Categories every tier-1 row must pin at least one scenario in (see
// hasExecutableAssertion in gate.go).
const (
	CategoryStatusHeaders Category = "status_headers"
	CategoryAuthorization Category = "authorization"
)

// Categories lists every category in schema order.
var Categories = []Category{
	CategoryStatusHeaders,
	"data_meaning",
	"field_presence_nullability",
	CategoryAuthorization,
	"sorting",
	"filtering",
	"pagination",
	"error",
	"raw_protocol",
}

// Catalog is one <listener>/<group>.json file.
type Catalog struct {
	// File is the path inside the embedded FS, for error messages.
	File          string `json:"-"`
	SchemaVersion int    `json:"schema_version"`
	Listener      string `json:"listener"`
	RouteGroup    string `json:"route_group"`
	Wave          int    `json:"wave"`
	Description   string `json:"description"`
	Rows          []Row  `json:"rows"`
}

// Row names one ledger row and its scenarios.
type Row struct {
	Listener          string              `json:"listener"`
	Method            string              `json:"method"`
	Path              string              `json:"path"`
	RegistrationIndex int                 `json:"registration_index"`
	Tier2Inclusion    string              `json:"tier2_inclusion,omitempty"`
	NotApplicable     map[Category]string `json:"not_applicable,omitempty"`
	Notes             string              `json:"notes,omitempty"`
	Scenarios         []Scenario          `json:"scenarios"`
}

// Key returns the ledger key for this row.
func (r Row) Key() contractledger.Key {
	return contractledger.Key{Listener: r.Listener, Method: r.Method, Path: r.Path, RegistrationIndex: r.RegistrationIndex}
}

// Scenario is one executable behavior case.
type Scenario struct {
	ID          string            `json:"id"`
	Category    Category          `json:"category"`
	Description string            `json:"description"`
	Principal   Principal         `json:"principal"`
	Request     Request           `json:"request"`
	Expect      Expect            `json:"expect"`
	Requires    []string          `json:"requires,omitempty"`
	Settings    map[string]string `json:"settings,omitempty"`
	FreshState  bool              `json:"fresh_state,omitempty"`
	// Then lists follow-up exchanges run after the primary request, in the
	// same state, so a scenario can pin the effect of a mutation (the poll
	// after an approval, the GET after a PUT) instead of only its status.
	Then          []Step          `json:"then,omitempty"`
	Notes         string          `json:"notes,omitempty"`
	V2Expectation json.RawMessage `json:"v2_expectation"`

	method string
}

// Step is one follow-up exchange of a scenario's then list. It names its
// own method because it may target a different row than the primary
// request; the principal defaults to the scenario's.
type Step struct {
	Description string     `json:"description,omitempty"`
	Method      string     `json:"method"`
	Principal   *Principal `json:"principal,omitempty"`
	Request     Request    `json:"request"`
	Expect      Expect     `json:"expect"`
}

// Method returns the HTTP method of the row the scenario belongs to.
func (s Scenario) Method() string { return s.method }

// NeedsDatabase reports whether the scenario can only run against a live
// Postgres: anything that sends a credential, applies settings, or says so.
func (s Scenario) NeedsDatabase() bool {
	if s.Principal.Class != "public" || len(s.Settings) > 0 || len(s.Then) > 0 {
		return true
	}
	for _, req := range s.Requires {
		if req == "database" {
			return true
		}
	}
	return false
}

// Requires reports whether the scenario lists the given requirement.
func (s Scenario) HasRequirement(name string) bool {
	for _, req := range s.Requires {
		if req == name {
			return true
		}
	}
	return false
}

// Principal says who sends the request.
type Principal struct {
	Class    string   `json:"class"`
	User     string   `json:"user,omitempty"`
	Profile  string   `json:"profile,omitempty"`
	Verified bool     `json:"verified,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}

// Request is the synthetic request shape.
type Request struct {
	Path        string             `json:"path"`
	Query       map[string]string  `json:"query,omitempty"`
	Headers     map[string]*string `json:"headers,omitempty"`
	Body        json.RawMessage    `json:"body,omitempty"`
	BodyRef     string             `json:"body_ref,omitempty"`
	RawBody     *string            `json:"raw_body,omitempty"`
	ContentType string             `json:"content_type,omitempty"`
	Multipart   *Multipart         `json:"multipart,omitempty"`
	Repeat      int                `json:"repeat,omitempty"`
}

// Multipart describes a synthetic multipart body.
type Multipart struct {
	Fields map[string]string `json:"fields,omitempty"`
	Files  []MultipartFile   `json:"files,omitempty"`
}

// MultipartFile is one synthetic file part.
type MultipartFile struct {
	Field       string `json:"field"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     string `json:"content"`
}

// Expect is the expected outcome.
type Expect struct {
	Status   int               `json:"status"`
	Headers  []HeaderAssertion `json:"headers,omitempty"`
	Body     []BodyAssertion   `json:"body,omitempty"`
	BodyKind string            `json:"body_kind,omitempty"`
}

// HeaderAssertion is one response-header predicate.
type HeaderAssertion struct {
	Name  string `json:"name"`
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
}

// BodyAssertion is one JSON-pointer predicate.
type BodyAssertion struct {
	Pointer string          `json:"pointer"`
	Op      string          `json:"op"`
	Value   json.RawMessage `json:"value,omitempty"`
	Why     string          `json:"why,omitempty"`
}

// Load parses and schema-validates every embedded catalog.
func Load() ([]*Catalog, error) {
	return load(scenarios.FS)
}

func load(fsys fs.FS) ([]*Catalog, error) {
	schemaBytes, err := fs.ReadFile(fsys, scenarios.SchemaPath)
	if err != nil {
		return nil, fmt.Errorf("scenariocatalog: read schema: %w", err)
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("scenariocatalog: parse schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(scenarios.SchemaPath, schemaDoc); err != nil {
		return nil, fmt.Errorf("scenariocatalog: add schema resource: %w", err)
	}
	schema, err := compiler.Compile(scenarios.SchemaPath)
	if err != nil {
		return nil, fmt.Errorf("scenariocatalog: compile schema: %w", err)
	}

	var files []string
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".json" || p == scenarios.SchemaPath || strings.HasPrefix(p, "fixtures/") {
			return nil
		}
		if !strings.Contains(p, "/") {
			return fmt.Errorf("scenariocatalog: %s is not under a listener directory", p)
		}
		files = append(files, p)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)

	var catalogs []*Catalog
	var problems []string
	for _, file := range files {
		raw, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, fmt.Errorf("scenariocatalog: read %s: %w", file, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: parse: %v", file, err))
			continue
		}
		if err := schema.Validate(doc); err != nil {
			problems = append(problems, fmt.Sprintf("%s: violates %s: %v", file, scenarios.SchemaPath, err))
			continue
		}
		var c Catalog
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			problems = append(problems, fmt.Sprintf("%s: decode: %v", file, err))
			continue
		}
		c.File = file
		for i := range c.Rows {
			for j := range c.Rows[i].Scenarios {
				c.Rows[i].Scenarios[j].method = c.Rows[i].Method
			}
		}
		if dir := path.Dir(file); dir != c.Listener {
			problems = append(problems, fmt.Sprintf("%s: directory %q does not match listener %q", file, dir, c.Listener))
		}
		if want := groupSlug(c.RouteGroup) + ".json"; path.Base(file) != want {
			problems = append(problems, fmt.Sprintf("%s: file name must be %s for route group %s", file, want, c.RouteGroup))
		}
		problems = append(problems, c.check()...)
		catalogs = append(catalogs, &c)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, errors.New("scenariocatalog: invalid catalogs:\n  " + strings.Join(problems, "\n  "))
	}
	return catalogs, nil
}

// check enforces the structural rules the schema cannot express.
func (c *Catalog) check() []string {
	var problems []string
	rowsSeen := map[contractledger.Key]bool{}
	idsSeen := map[string]bool{}
	for _, row := range c.Rows {
		if row.Listener != c.Listener {
			problems = append(problems, fmt.Sprintf("%s: row %s listener disagrees with catalog listener %s", c.File, row.Key(), c.Listener))
		}
		if rowsSeen[row.Key()] {
			problems = append(problems, fmt.Sprintf("%s: duplicate row %s", c.File, row.Key()))
		}
		rowsSeen[row.Key()] = true
		covered := map[Category]bool{}
		for _, s := range row.Scenarios {
			if idsSeen[s.ID] {
				problems = append(problems, fmt.Sprintf("%s: duplicate scenario id %q", c.File, s.ID))
			}
			idsSeen[s.ID] = true
			covered[s.Category] = true
			if s.Request.Body != nil && (s.Request.BodyRef != "" || s.Request.RawBody != nil || s.Request.Multipart != nil) {
				problems = append(problems, fmt.Sprintf("%s: scenario %s declares more than one body source", c.File, s.ID))
			}
			if s.Principal.Class != "api_key" && len(s.Principal.Scopes) > 0 {
				problems = append(problems, fmt.Sprintf("%s: scenario %s sets scopes on a non-api_key principal", c.File, s.ID))
			}
			for i, step := range s.Then {
				if step.Request.Body != nil && (step.Request.BodyRef != "" || step.Request.RawBody != nil || step.Request.Multipart != nil) {
					problems = append(problems, fmt.Sprintf("%s: scenario %s then[%d] declares more than one body source", c.File, s.ID, i))
				}
				if step.Principal != nil && step.Principal.Class != "api_key" && len(step.Principal.Scopes) > 0 {
					problems = append(problems, fmt.Sprintf("%s: scenario %s then[%d] sets scopes on a non-api_key principal", c.File, s.ID, i))
				}
			}
		}
		for cat, reason := range row.NotApplicable {
			if covered[cat] {
				problems = append(problems, fmt.Sprintf("%s: row %s marks %s not_applicable but also has a %s scenario", c.File, row.Key(), cat, cat))
			}
			if strings.TrimSpace(reason) == "" {
				problems = append(problems, fmt.Sprintf("%s: row %s has an empty not_applicable reason for %s", c.File, row.Key(), cat))
			}
		}
		for _, cat := range Categories {
			if !covered[cat] {
				if _, ok := row.NotApplicable[cat]; !ok {
					problems = append(problems, fmt.Sprintf("%s: row %s has no %s scenario and no not_applicable reason", c.File, row.Key(), cat))
				}
			}
		}
	}
	return problems
}

// groupSlug turns a ledger route_group into the catalog file stem:
// "/api/v1/auth/oauth/{install_id}" -> "api-v1-auth-oauth-install_id".
func groupSlug(group string) string {
	s := strings.Trim(group, "/")
	if s == "" {
		return "root"
	}
	s = strings.NewReplacer("{", "", "}", "", "/", "-", "*", "wildcard").Replace(s)
	return s
}

// GroupSlug is exported for the generator/tests.
func GroupSlug(group string) string { return groupSlug(group) }
