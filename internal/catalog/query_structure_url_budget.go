package catalog

import (
	"encoding/json"
	"net/url"
	"strings"
)

// URL grammar admission precedes builder maps, indexed-value arrays and rating
// cleanup. The maps here can never grow beyond the structural limits.
func validateCatalogURLStructure(values url.Values) error {
	var budget queryStructureBudget
	groups := make(map[string]struct{})
	rules := make(map[[2]string]struct{})
	for key, rawValues := range values {
		group, rule, hasRule, hasValue := "", "", false, false
		if match := catalogGroupMatchPattern.FindStringSubmatch(key); match != nil {
			group = match[1]
		} else if match := catalogGroupRulePattern.FindStringSubmatch(key); match != nil {
			group, rule, hasRule, hasValue = match[1], match[2], true, match[3] == "value"
		} else if match := catalogGroupRuleValuePattern.FindStringSubmatch(key); match != nil {
			group, rule, hasRule, hasValue = match[1], match[2], true, true
		}
		if group != "" {
			groups[group] = struct{}{}
			budget.groups = len(groups)
		}
		if hasRule {
			rules[[2]string{group, rule}] = struct{}{}
			budget.rules = len(rules)
		}
		if err := budget.validate(); err != nil {
			return err
		}
		if hasValue {
			if err := addCatalogURLValueBytes(&budget, rawValues); err != nil {
				return err
			}
		}
	}
	// Admission also counts raw values that first-value selection or ratings
	// normalization would discard. The final composed definition is validated
	// again after implicit and explicit groups have been joined.
	budget.libraries += len(values["library_id"])
	if err := budget.validate(); err != nil {
		return err
	}
	for _, key := range []string{"genre", "status", "year_min", "year_max"} {
		if err := addCatalogURLValueBytes(&budget, values[key]); err != nil {
			return err
		}
	}
	for _, raw := range values["content_rating"] {
		for rating := range strings.SplitSeq(raw, ",") {
			budget.rules++
			if err := budget.validate(); err != nil {
				return err
			}
			if err := addCatalogURLValueBytes(&budget, []string{rating}); err != nil {
				return err
			}
		}
	}
	return nil
}

func addCatalogURLValueBytes(budget *queryStructureBudget, values []string) error {
	for _, raw := range values {
		if len(raw) > queryMaxValueBytes-budget.valueBytes {
			return ErrQueryComplexity
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		budget.valueBytes += len(encoded)
		if err := budget.validate(); err != nil {
			return err
		}
	}
	return nil
}
