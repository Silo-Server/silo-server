package settingsmigrate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Legacy account-wide keys that the server used to read at request time
// whenever a profile had no canonical row: access scope resolution for hidden
// libraries, and home sections for the next-up mode. The legacy settings
// endpoint stopped accepting both at the cutover, so their stored values are
// frozen.
const (
	legacyDisabledLibraryIDsKey = "disabled_library_ids"
	legacyNextUpModeKey         = "next_up_mode"
)

// RetiredFallbackKeys lists the legacy user_settings keys whose read-time
// fallback was replaced by materialized profile-scope rows. The SQLite user
// store selects exactly these keys when it runs the replacement migration; the
// Postgres SQL migration names the same two.
func RetiredFallbackKeys() []string {
	return []string{legacyDisabledLibraryIDsKey, legacyNextUpModeKey}
}

// PlanRetiredFallback converts one frozen legacy account value into the
// profile-scope value the retired fallback read produced from it, so a
// profile without a canonical row resolves the same answer after the fallback
// is gone. The caller writes the result only for profiles that have no row for
// the key; a stored canonical row always won over the fallback, so it wins
// here too.
//
// It returns no values when the fallback resolved to nothing, which the
// contract default already means. An error means the value cannot be stored
// canonically; the caller leaves the legacy row where it is.
func (p *Planner) PlanRetiredFallback(legacyKey, raw string) ([]RuntimeValue, error) {
	planned, err := p.planRetiredFallback(legacyKey, raw)
	if err != nil {
		return nil, err
	}
	// A nil value is PlanRuntimeValue's "clear this row"; with nothing stored
	// there is nothing to clear.
	out := planned[:0]
	for _, value := range planned {
		if value.Value != nil {
			out = append(out, value)
		}
	}
	return out, nil
}

func (p *Planner) planRetiredFallback(legacyKey, raw string) ([]RuntimeValue, error) {
	switch legacyKey {
	case legacyDisabledLibraryIDsKey:
		// The fallback decoded a JSON integer array, read malformed JSON as
		// empty, and ignored non-positive ids. Normalize the same way before
		// the contract validates it, so a list the fallback honored partly is
		// kept partly rather than rejected whole.
		ids := fallbackLibraryIDs(raw)
		if len(ids) == 0 {
			return nil, nil
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return nil, fmt.Errorf("encoding %s: %w", legacyKey, err)
		}
		return p.PlanRuntimeValue(legacyKey, string(encoded))
	case legacyNextUpModeKey:
		// The fallback returned the stored string unchanged and the section
		// fetchers matched it exactly against each member, so a padded or
		// unknown value showed next-up in neither place. The contract has no
		// such state, so those profiles get the default. Reject a padded value
		// here, before PlanRuntimeValue trims it into a member the user never
		// had in effect.
		if strings.TrimSpace(raw) != raw {
			return nil, fmt.Errorf("%s value %q is padded; the fallback treated it as the default", legacyKey, raw)
		}
		return p.PlanRuntimeValue(legacyKey, raw)
	default:
		return nil, fmt.Errorf("%s has no retired read-time fallback", legacyKey)
	}
}

// fallbackLibraryIDs decodes a legacy hidden-library list the way the retired
// fallback did, then drops repeats because the contract requires unique items.
// Repeats never changed what the fallback hid.
func fallbackLibraryIDs(raw string) []int {
	var ids []int
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
