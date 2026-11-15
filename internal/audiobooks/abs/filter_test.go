package abs

import (
	"encoding/base64"
	"testing"
)

// Real ABS clients base64-encode every filter value, the progress states and
// the no-series sentinel included; older clients send those two in the clear.
func TestParseFilterDecodesEncodedAndPlainValues(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for raw, want := range map[string]Filter{
		"progress." + enc("finished"):     {Kind: FilterProgress, Value: "finished"},
		"progress.in-progress":            {Kind: FilterProgress, Value: "in-progress"},
		"series." + enc(SentinelNoSeries): {Kind: FilterSeries, Value: SentinelNoSeries},
		"series.no-series":                {Kind: FilterSeries, Value: SentinelNoSeries},
		"genres." + enc("Fantasy"):        {Kind: FilterGenres, Value: "Fantasy"},
	} {
		got, ok := ParseFilter(raw)
		if !ok || got.Kind != want.Kind || got.Value != want.Value {
			t.Errorf("ParseFilter(%q) = %+v, %v; want %s %q", raw, got, ok, want.Kind, want.Value)
		}
	}
}
