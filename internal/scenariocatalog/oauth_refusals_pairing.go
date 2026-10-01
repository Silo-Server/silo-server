package scenariocatalog

import (
	"fmt"
	"net/http"
	"slices"
)

// RequiredOAuthRefusalScenarios fixes the bounded OAuth refusal and
// disabled-account acceptance inventory. Every case is decided before any
// authentication plugin is contacted: install_id parsing, state signature
// verification, request body validation, completion-code lookup, or the
// current-account read of a disabled but still-authenticated session.
// disabledAccountReadScenario is the one non-OAuth case in this inventory: a
// disabled account whose still-valid session reads its own account.
const disabledAccountReadScenario = "me.disabled_account"

// currentAccountOperation is the v2 operation the disabled-account read pairs to.
const currentAccountOperation = "getCurrentUser"

var RequiredOAuthRefusalScenarios = []string{
	"oauth_init.bad_install", "oauth_init.unknown_plugin", "oauth_init.status", "oauth_init.raw",
	"oauth_init.meaning", "oauth_init.next_filter", "oauth_init.shape",
	"oauth_cb.bad_install", "oauth_cb.missing_params", "oauth_cb.bad_state", "oauth_cb.status",
	"oauth_cb.meaning", "oauth_cb.filter", "oauth_cb.shape",
	"oauth_complete.not_mounted", "oauth_complete.missing_code", "oauth_complete.malformed",
	"oauth_complete.raw", "oauth_complete.meaning", "oauth_complete.shape",
	disabledAccountReadScenario,
}

// oauthRefusalStatuses pins each case's original v1 status and its v2 status.
// The v1 side is the frozen oracle; the v2 side records the ratified mapping
// (identical plain-text handshakes, and Problem statuses for completion).
var oauthRefusalStatuses = map[string][2]int{
	"oauth_init.bad_install": {400, 400}, "oauth_init.unknown_plugin": {502, 502}, "oauth_init.status": {502, 502},
	"oauth_init.raw": {502, 502}, "oauth_init.meaning": {400, 400}, "oauth_init.next_filter": {502, 502}, "oauth_init.shape": {502, 502},
	"oauth_cb.bad_install": {400, 400}, "oauth_cb.missing_params": {400, 400}, "oauth_cb.bad_state": {302, 302}, "oauth_cb.status": {302, 302},
	"oauth_cb.meaning": {302, 302}, "oauth_cb.filter": {400, 400}, "oauth_cb.shape": {302, 302},
	"oauth_complete.not_mounted": {401, 401}, "oauth_complete.missing_code": {400, 422}, "oauth_complete.malformed": {400, 400},
	"oauth_complete.raw": {401, 401}, "oauth_complete.meaning": {401, 401}, "oauth_complete.shape": {401, 401},
	disabledAccountReadScenario: {200, 200},
}

// OAuthRefusalAcceptance selects the fixed inventory and refuses any case that
// would need a plugin: only the recorded refusal operations, the pinned
// statuses, no follow-ups, no principal override, and no requirement beyond
// the database.
func OAuthRefusalAcceptance(catalogs []*Catalog) ([]*Catalog, error) {
	var selected []*Catalog
	for _, group := range []struct {
		method string
		paths  []string
		ids    []string
		op     string
	}{
		{http.MethodPost, []string{"/api/v1/auth/oauth/{install_id}/init"}, RequiredOAuthRefusalScenarios[:7], "initOAuthLogin"},
		{http.MethodGet, []string{"/api/v1/auth/oauth/{install_id}/callback"}, RequiredOAuthRefusalScenarios[7:14], "finishOAuthCallback"},
		{http.MethodPost, []string{"/api/v1/auth/oauth/complete"}, RequiredOAuthRefusalScenarios[14:20], "completeOAuthLogin"},
		{http.MethodGet, []string{meImpersonationLegacyRoute}, RequiredOAuthRefusalScenarios[20:], currentAccountOperation},
	} {
		picked, err := requiredAcceptance(catalogs, group.method, group.paths, group.ids)
		if err != nil {
			return nil, err
		}
		for _, c := range picked {
			for _, r := range c.Rows {
				for _, s := range r.Scenarios {
					for _, requirement := range s.Requires {
						if requirement != frozenDatabaseRequirement {
							return nil, fmt.Errorf("%s: unsupported requirement %q", s.ID, requirement)
						}
					}
					pair := s.V2Expectation
					if pair.OperationID != group.op || pair.Principal != nil || len(s.Then) != 0 || len(pair.Then) != 0 || pair.Request.Repeat > 1 || s.Request.Repeat > 1 {
						return nil, fmt.Errorf("%s: unsupported oauth refusal acceptance sequence", s.ID)
					}
					// A success on any handshake step would need a served plugin; this
					// inventory records only refusals and the disabled-account read.
					statuses := oauthRefusalStatuses[s.ID]
					if s.Expect.Status != statuses[0] || pair.Expect.Status != statuses[1] {
						return nil, fmt.Errorf("%s: statuses %d/%d differ from the pinned original %d and ratified v2 %d", s.ID, s.Expect.Status, pair.Expect.Status, statuses[0], statuses[1])
					}
					if s.ID == disabledAccountReadScenario && (s.Principal.Class != decisionAuthenticatedPrincipal || s.Principal.User != "disabled") {
						return nil, fmt.Errorf("%s: must read as the disabled fixture account", s.ID)
					}
				}
			}
		}
		selected = append(selected, picked...)
	}
	// Each group came from one catalog file; keep one entry per file so the
	// executor iterates every selected row exactly once.
	merged := map[string]*Catalog{}
	var order []string
	for _, c := range selected {
		if existing, ok := merged[c.File]; ok {
			existing.Rows = append(existing.Rows, c.Rows...)
			continue
		}
		copied := *c
		merged[c.File] = &copied
		order = append(order, c.File)
	}
	result := make([]*Catalog, 0, len(order))
	for _, file := range order {
		result = append(result, merged[file])
	}
	slices.SortFunc(result, func(a, b *Catalog) int {
		if a.File < b.File {
			return -1
		}
		if a.File > b.File {
			return 1
		}
		return 0
	})
	return result, nil
}
