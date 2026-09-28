package settingsmigrate

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingskeys"
)

// TestPlanRetiredFallbackMatchesTheFallbackRead pins the replacement rule:
// each legacy value becomes the canonical value the retired read-time fallback
// derived from it, so no profile's effective answer changes when the fallback
// is removed.
func TestPlanRetiredFallbackMatchesTheFallbackRead(t *testing.T) {
	p := planner(t)
	tests := []struct {
		name    string
		key     string
		raw     string
		wantKey string
		want    string // "" means no row
	}{
		{name: "hidden libraries", key: "disabled_library_ids", raw: `[3,9]`, wantKey: settingskeys.UiDisabledLibraryIds, want: `[3,9]`},
		// The fallback ignored non-positive ids and repeats never mattered,
		// so the usable part survives instead of the whole list being lost.
		{name: "hidden libraries keep the ids the fallback honored", key: "disabled_library_ids", raw: `[0,3,3,-1,5]`, wantKey: settingskeys.UiDisabledLibraryIds, want: `[3,5]`},
		{name: "empty hidden libraries", key: "disabled_library_ids", raw: `[]`},
		{name: "malformed hidden libraries read as empty", key: "disabled_library_ids", raw: `[3,`},
		{name: "separate next-up row", key: "next_up_mode", raw: "separate", wantKey: settingskeys.UiNextUpMode, want: `"separate"`},
		{name: "combined next-up row", key: "next_up_mode", raw: "combined", wantKey: settingskeys.UiNextUpMode, want: `"combined"`},
		{name: "empty next-up mode is unset", key: "next_up_mode", raw: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			planned, err := p.PlanRetiredFallback(tt.key, tt.raw)
			if err != nil {
				t.Fatalf("PlanRetiredFallback(%q, %q): %v", tt.key, tt.raw, err)
			}
			if tt.want == "" {
				if len(planned) != 0 {
					t.Fatalf("planned %+v, want no row", planned)
				}
				return
			}
			if len(planned) != 1 || planned[0].Key != tt.wantKey || string(planned[0].Value) != tt.want {
				t.Fatalf("planned %+v, want one %s = %s", planned, tt.wantKey, tt.want)
			}
		})
	}
}

func TestPlanRetiredFallbackRejectsWhatTheContractCannotStore(t *testing.T) {
	p := planner(t)
	if _, err := p.PlanRetiredFallback("next_up_mode", "sideways"); err == nil {
		t.Error("an unknown next-up mode planned a row; it is not a ui.next_up_mode member")
	}
	// The fallback compared the raw string exactly, so a padded member acted
	// as the default and must not become the member.
	if _, err := p.PlanRetiredFallback("next_up_mode", " separate\n"); err == nil {
		t.Error("a padded next-up mode planned a row; the fallback treated it as the default")
	}
	if _, err := p.PlanRetiredFallback("ui_theme", "dark"); err == nil {
		t.Error("a key with no retired fallback planned a row")
	}
}
