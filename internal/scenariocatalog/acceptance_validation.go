package scenariocatalog

import "fmt"

// validateAcceptanceScenarios enforces the common shape of a database-only
// acceptance cohort. Cohort-specific wrappers still choose their inventory and
// operation while this policy stays in one place.
func validateAcceptanceScenarios(selected []*Catalog, operationID, label string) error {
	for _, c := range selected {
		for _, r := range c.Rows {
			for _, s := range r.Scenarios {
				for _, requirement := range s.Requires {
					if requirement != frozenDatabaseRequirement {
						return fmt.Errorf("%s: unsupported requirement", s.ID)
					}
				}
				if s.V2Expectation.OperationID != operationID || len(s.Then) != 0 || len(s.V2Expectation.Then) != 0 {
					return fmt.Errorf("%s: unsupported %s acceptance sequence", s.ID, label)
				}
			}
		}
	}
	return nil
}
