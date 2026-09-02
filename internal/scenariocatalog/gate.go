package scenariocatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
	"github.com/Silo-Server/silo-server/internal/contractledger"
)

// LedgerEntry is the slice of a migration-ledger entry the gate and the
// executor reason about. contractledger owns the schema; this package only
// reads the committed bytes.
type LedgerEntry struct {
	Listener          string   `json:"listener"`
	Method            string   `json:"method"`
	Path              string   `json:"path"`
	RegistrationIndex int      `json:"registration_index"`
	RouteGroup        string   `json:"route_group"`
	ReleaseFlow       string   `json:"release_flow"`
	AuthClass         string   `json:"auth_class"`
	Conditions        []string `json:"conditions"`
	Tier              int      `json:"tier"`
}

// Key returns the ledger key.
func (e LedgerEntry) Key() contractledger.Key {
	return contractledger.Key{Listener: e.Listener, Method: e.Method, Path: e.Path, RegistrationIndex: e.RegistrationIndex}
}

// RateLimited reports whether the row is the rate-limited registration
// variant (the router registers it only when a rate-limit middleware exists).
func (e LedgerEntry) RateLimited() bool {
	for _, c := range e.Conditions {
		if c == "deps.RateLimitMW != nil" {
			return true
		}
	}
	return false
}

// LoadLedger decodes the embedded migration ledger.
func LoadLedger() (map[contractledger.Key]LedgerEntry, error) {
	raw, err := fs.ReadFile(apiv2.FS, "migration.json")
	if err != nil {
		return nil, fmt.Errorf("scenariocatalog: read ledger: %w", err)
	}
	var doc struct {
		Entries []LedgerEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("scenariocatalog: decode ledger: %w", err)
	}
	out := make(map[contractledger.Key]LedgerEntry, len(doc.Entries))
	for _, e := range doc.Entries {
		out[e.Key()] = e
	}
	return out, nil
}

// Wave describes one migration wave's coverage obligation: which ledger
// route groups it owns, plus any tier-2 rows it deliberately baselines.
type Wave struct {
	Number int
	Name   string
	// Groups are ledger route_group values on the api listener.
	Groups []string
	// ExtraRows are rows outside the groups (or tier 2) the wave still
	// covers, keyed like the ledger.
	ExtraRows []contractledger.Key
}

// Route groups and rows wave 1 covers. The auth group is referenced from
// tests as well, so it is a named constant.
const (
	GroupAuth   = "/api/v1/auth"
	listenerAPI = "api"
)

// Waves is the ratified wave order. A wave is gated once any catalog
// declares it; later waves populate their catalogs before their first
// section PR (maintainer decision on #135).
var Waves = []Wave{
	{
		Number: 1,
		Name:   "identity and discovery",
		Groups: []string{
			GroupAuth,
			"/api/v1/auth/oauth/{install_id}",
			"/api/v1/profiles",
			"/api/v1/profile/sections",
			"/api/v1/invitations/{token}",
			"/api/v1/onboarding",
			"/api/v1/devices",
			"/api/v1/api-keys",
			"/api/v1/admin/invitations",
			"/api/v1/admin/invite-codes",
			"/api/v1/admin/system",
		},
		ExtraRows: []contractledger.Key{
			{Listener: listenerAPI, Method: http.MethodGet, Path: "/api/v1/health"},
			{Listener: listenerAPI, Method: http.MethodGet, Path: "/api/v1/ready"},
		},
	},
}

// Verify runs the full gate against the embedded catalogs and ledger.
func Verify() error {
	catalogs, err := Load()
	if err != nil {
		return err
	}
	if err := contractledger.Verify(); err != nil {
		return err
	}
	byKey, err := LoadLedger()
	if err != nil {
		return err
	}
	return verify(catalogs, byKey)
}

func verify(catalogs []*Catalog, byKey map[contractledger.Key]LedgerEntry) error {

	// Rows a wave must cover: tier-1 rows in its groups plus extra rows.
	required := map[int]map[contractledger.Key]bool{}
	for _, w := range Waves {
		groups := map[string]bool{}
		for _, g := range w.Groups {
			groups[g] = true
		}
		req := map[contractledger.Key]bool{}
		for k, e := range byKey {
			if e.Tier == 1 && e.Listener == listenerAPI && groups[e.RouteGroup] {
				req[k] = true
			}
		}
		for _, k := range w.ExtraRows {
			req[k] = true
		}
		required[w.Number] = req
	}

	var problems []string
	covered := map[contractledger.Key]string{}
	declaredWaves := map[int]bool{}
	for _, c := range catalogs {
		declaredWaves[c.Wave] = true
		wave, ok := waveByNumber(c.Wave)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: wave %d is not defined", c.File, c.Wave))
			continue
		}
		if !contains(wave.Groups, c.RouteGroup) && !hasExtraGroup(wave, c) {
			problems = append(problems, fmt.Sprintf("%s: route group %s is not part of wave %d (%s)", c.File, c.RouteGroup, c.Wave, wave.Name))
		}
		for _, row := range c.Rows {
			k := row.Key()
			if prev, dup := covered[k]; dup {
				problems = append(problems, fmt.Sprintf("%s: row %s is already covered by %s", c.File, k, prev))
				continue
			}
			covered[k] = c.File
			entry, ok := byKey[k]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: row %s is not in the migration ledger", c.File, k))
				continue
			}
			if entry.RouteGroup != c.RouteGroup && !required[c.Wave][k] {
				problems = append(problems, fmt.Sprintf("%s: row %s belongs to ledger route group %s, not %s", c.File, k, entry.RouteGroup, c.RouteGroup))
			}
			if entry.Tier != 1 && row.Tier2Inclusion == "" {
				problems = append(problems, fmt.Sprintf("%s: row %s is tier %d in the ledger; tier-2 rows need tier2_inclusion", c.File, k, entry.Tier))
			}
			if entry.Tier == 1 && row.Tier2Inclusion != "" {
				problems = append(problems, fmt.Sprintf("%s: row %s is tier 1 but carries tier2_inclusion", c.File, k))
			}
			if entry.Tier != 1 && !required[c.Wave][k] {
				problems = append(problems, fmt.Sprintf("%s: tier-2 row %s is not listed as an extra row of wave %d", c.File, k, c.Wave))
			}
		}
	}

	for waveNo := range declaredWaves {
		var missing []string
		for k := range required[waveNo] {
			if _, ok := covered[k]; !ok {
				missing = append(missing, k.String())
			}
		}
		sort.Strings(missing)
		for _, m := range missing {
			problems = append(problems, fmt.Sprintf("wave %d: tier-1 row has no scenario catalog: %s", waveNo, m))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New("scenariocatalog: coverage gate failed:\n  " + strings.Join(problems, "\n  "))
}

func waveByNumber(n int) (Wave, bool) {
	for _, w := range Waves {
		if w.Number == n {
			return w, true
		}
	}
	return Wave{}, false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// hasExtraGroup reports whether the catalog only holds extra rows of the
// wave (the health/ready catalog under route group /api/v1).
func hasExtraGroup(w Wave, c *Catalog) bool {
	if len(c.Rows) == 0 {
		return false
	}
	for _, row := range c.Rows {
		found := false
		for _, k := range w.ExtraRows {
			if k == row.Key() {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Coverage summarizes the catalogs for reports and tests.
type Coverage struct {
	Catalogs    int
	Rows        int
	Scenarios   int
	ByCategory  map[Category]int
	CIRunnable  int
	DBGated     int
	Flagged     int
	NotApplicab int
}

// Summarize counts rows and scenarios across catalogs.
func Summarize(catalogs []*Catalog) Coverage {
	cov := Coverage{ByCategory: map[Category]int{}}
	for _, c := range catalogs {
		cov.Catalogs++
		for _, row := range c.Rows {
			cov.Rows++
			cov.NotApplicab += len(row.NotApplicable)
			for _, s := range row.Scenarios {
				cov.Scenarios++
				cov.ByCategory[s.Category]++
				if s.NeedsDatabase() {
					cov.DBGated++
				} else {
					cov.CIRunnable++
				}
				if s.Notes != "" {
					cov.Flagged++
				}
			}
		}
	}
	return cov
}
