package apibench

import (
	"strings"
	"testing"
)

// TestSamplerStatementsCarryTag pins the self-exclusion contract: every
// statement the sampler issues names samplerTag as an identifier, and the
// tag is only ever an identifier (a comment would be stripped from the
// stored text and a string constant normalized to $n).
func TestSamplerStatementsCarryTag(t *testing.T) {
	for name, stmt := range map[string]string{
		"installed": samplerStatements.installed,
		"database":  samplerStatements.database,
		"calls":     samplerStatements.calls,
	} {
		if !strings.Contains(stmt, " AS "+samplerTag) {
			t.Errorf("%s statement does not carry %q as an alias:\n%s", name, samplerTag, stmt)
		}
		if strings.Contains(stmt, "/*") || strings.Contains(stmt, "'"+samplerTag+"'") {
			t.Errorf("%s statement carries the tag as a comment or constant, which pg_stat_statements normalizes away:\n%s", name, stmt)
		}
	}
}
