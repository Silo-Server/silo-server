// Package contractledger validates the pre-1.0-native-to-v2 migration ledger
// (contracts/api/v2/migration.json) against its JSON Schema and reconciles it
// with the legacy native route inventory (contracts/api/v2/route-inventory.json).
//
// The gate is one entry per inventory row and one inventory row per entry.
// Both artifacts are embedded, so a drift between them fails the build's test
// run rather than surfacing later as a missing migration decision.
package contractledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
)

const (
	inventoryPath = "route-inventory.json"
	ledgerPath    = "migration.json"
	schemaPath    = "migration.schema.json"
)

// Key identifies one registered route variant. The inventory registers a few
// method+path pairs more than once under different middleware or conditions,
// so RegistrationIndex disambiguates those in registration order.
type Key struct {
	Listener          string
	Method            string
	Path              string
	RegistrationIndex int
}

func (k Key) String() string {
	return fmt.Sprintf("%s %s %s #%d", k.Listener, k.Method, k.Path, k.RegistrationIndex)
}

// Entry is the subset of a ledger entry the gate reasons about. The schema
// governs the full shape; this struct only carries what reconciliation needs.
type Entry struct {
	Listener          string `json:"listener"`
	Method            string `json:"method"`
	Path              string `json:"path"`
	RegistrationIndex int    `json:"registration_index"`
	Handler           string `json:"handler"`
	Disposition       string `json:"disposition"`
	ReviewState       string `json:"review_state"`
	Tier              int    `json:"tier"`
}

func (e Entry) key() Key {
	return Key{Listener: e.Listener, Method: e.Method, Path: e.Path, RegistrationIndex: e.RegistrationIndex}
}

// Ledger is the parsed migration ledger.
type Ledger struct {
	SchemaVersion int `json:"schema_version"`
	Totals        struct {
		Entries int `json:"entries"`
	} `json:"totals"`
	Entries []Entry `json:"entries"`
}

type inventoryRoute struct {
	Listener string `json:"listener"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Handler  string `json:"handler"`
}

type inventory struct {
	Routes []inventoryRoute `json:"routes"`
}

// Load parses and schema-validates the embedded ledger.
func Load() (*Ledger, error) {
	return load(apiv2.FS)
}

// Verify runs the full gate against the embedded artifacts: schema validity,
// then bidirectional reconciliation with the route inventory. It returns every
// discrepancy at once so a maintainer can fix the ledger in one pass.
func Verify() error {
	return verify(apiv2.FS)
}

func load(fsys fs.FS) (*Ledger, error) {
	schemaBytes, err := fs.ReadFile(fsys, schemaPath)
	if err != nil {
		return nil, fmt.Errorf("contractledger: read schema: %w", err)
	}
	ledgerBytes, err := fs.ReadFile(fsys, ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("contractledger: read ledger: %w", err)
	}

	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("contractledger: parse schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(schemaPath, schemaDoc); err != nil {
		return nil, fmt.Errorf("contractledger: add schema resource: %w", err)
	}
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("contractledger: compile schema: %w", err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(ledgerBytes))
	if err != nil {
		return nil, fmt.Errorf("contractledger: parse ledger: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("contractledger: ledger violates %s: %w", schemaPath, err)
	}

	var ledger Ledger
	dec := json.NewDecoder(bytes.NewReader(ledgerBytes))
	if err := dec.Decode(&ledger); err != nil {
		return nil, fmt.Errorf("contractledger: decode ledger: %w", err)
	}
	if ledger.Totals.Entries != len(ledger.Entries) {
		return nil, fmt.Errorf("contractledger: totals.entries is %d but %d entries are present", ledger.Totals.Entries, len(ledger.Entries))
	}
	return &ledger, nil
}

func verify(fsys fs.FS) error {
	ledger, err := load(fsys)
	if err != nil {
		return err
	}
	inventoryBytes, err := fs.ReadFile(fsys, inventoryPath)
	if err != nil {
		return fmt.Errorf("contractledger: read inventory: %w", err)
	}
	var inv inventory
	if err := json.Unmarshal(inventoryBytes, &inv); err != nil {
		return fmt.Errorf("contractledger: decode inventory: %w", err)
	}

	// Registration index is positional within (listener, method, path) in
	// inventory order, matching how the ledger was generated.
	seen := map[Key]int{}
	invByKey := make(map[Key]inventoryRoute, len(inv.Routes))
	invOrder := make([]Key, 0, len(inv.Routes))
	for _, r := range inv.Routes {
		base := Key{Listener: r.Listener, Method: r.Method, Path: r.Path}
		k := base
		k.RegistrationIndex = seen[base]
		seen[base]++
		invByKey[k] = r
		invOrder = append(invOrder, k)
	}

	var problems []string
	ledgerByKey := make(map[Key]Entry, len(ledger.Entries))
	for i, e := range ledger.Entries {
		k := e.key()
		if _, dup := ledgerByKey[k]; dup {
			problems = append(problems, fmt.Sprintf("duplicate ledger entry for %s", k))
			continue
		}
		ledgerByKey[k] = e
		if i < len(invOrder) && invOrder[i] != k {
			problems = append(problems, fmt.Sprintf("ledger entry %d is %s; inventory row %d is %s (ledger must follow inventory order)", i, k, i, invOrder[i]))
		}
	}
	for _, k := range invOrder {
		e, ok := ledgerByKey[k]
		if !ok {
			problems = append(problems, fmt.Sprintf("inventory row has no ledger entry: %s", k))
			continue
		}
		if r := invByKey[k]; e.Handler != r.Handler {
			problems = append(problems, fmt.Sprintf("handler drift for %s: ledger %q, inventory %q", k, e.Handler, r.Handler))
		}
	}
	for k := range ledgerByKey {
		if _, ok := invByKey[k]; !ok {
			problems = append(problems, fmt.Sprintf("ledger entry has no inventory row: %s", k))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New("contractledger: ledger and route inventory disagree:\n  " + strings.Join(problems, "\n  "))
}
