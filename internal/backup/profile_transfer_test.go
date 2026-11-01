package backup

import (
	"errors"
	"strings"
	"testing"
)

// TestParseProfileExportRefusesHostileDocuments: a profile import reads an
// untrusted upload. It carries only the profile's own data, so a document
// holding account or access fields, a newer or foreign format, unknown
// fields or data, or one too large or too deep is refused before anything
// is written.
func TestParseProfileExportRefusesHostileDocuments(t *testing.T) {
	head := `"format":"silo-profile-export","version":1,"server_id":"s1","exported_at":"2026-01-01T00:00:00Z","profile_name":"Main"`
	valid := `{` + head + `,"data":{"ratings":{"ratings":[{"item":"m1","rating":4,"rated_at":"2026-01-01T00:00:00Z"}]}},"items":[]}`
	if _, err := ParseProfileExport([]byte(valid)); err != nil {
		t.Fatalf("a valid document: %v", err)
	}
	cases := []struct {
		name string
		doc  string
		want error
	}{
		{"a PIN hash", `{` + head + `,"pin_hash":"x","data":{},"items":[]}`, ErrProfileImportAccessFields},
		{"access fields in preferences", `{` + head + `,"data":{"preferences":{"profile":{"max_playback_quality":"","library_restrictions_enabled":false}}},"items":[]}`, ErrProfileImportAccessFields},
		{"a newer version", `{"format":"silo-profile-export","version":2,"data":{}}`, ErrProfileExportTooNew},
		{"another format", `{"format":"silo-restore-set","version":1}`, nil},
		{"unknown fields", `{` + head + `,"extra":true,"data":{},"items":[]}`, nil},
		{"unknown data", `{` + head + `,"data":{"passwords":{}},"items":[]}`, nil},
		{"unknown fields inside data", `{` + head + `,"data":{"ratings":{"ratings":[],"owner":1}},"items":[]}`, nil},
		{"too deep", `{` + head + `,"data":{"ratings":` + strings.Repeat("[", 64) + strings.Repeat("]", 64) + `},"items":[]}`, nil},
		{"too large", `{` + head + `,"profile_name":"` + strings.Repeat("x", MaxProfileImportBytes) + `"}`, ErrProfileImportTooLarge},
		{"repeated items", `{` + head + `,"data":{},"items":[{"content_id":"m1"},{"content_id":"m1"}]}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseProfileExport([]byte(c.doc))
			if err == nil {
				t.Fatal("accepted")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == nil {
				if _, ok := errors.AsType[*InvalidProfileExportError](err); !ok {
					t.Fatalf("err = %v, want an invalid document", err)
				}
			}
		})
	}
}
