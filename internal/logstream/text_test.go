package logstream

import (
	"encoding/json"
	"testing"
)

func TestSafeText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"/api/v2/items", "/api/v2/items"},
		{"café ☕", "café ☕"},
		{"bad-\xff", "bad-\uFFFD"},
		{"caf\xe9.mkv", "caf\uFFFD.mkv"},
		{"/probe/\x00x", "/probe/\uFFFDx"},
		{"\xff\xfe\x00", "\uFFFD\uFFFD"},
	} {
		if got := SafeText(tc.in); got != tc.want {
			t.Errorf("SafeText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSafeTextDoesNotAllocateForCleanText(t *testing.T) {
	s := "GET /api/v2/items/42 Mozilla/5.0"
	if n := testing.AllocsPerRun(100, func() { _ = SafeText(s) }); n != 0 {
		t.Fatalf("SafeText allocated %v times for clean text", n)
	}
}

func TestSafeJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want map[string]string
	}{
		{"clean", map[string]string{"a": "b"}, map[string]string{"a": "b"}},
		{"nul value", map[string]string{"path": "a\x00b"}, map[string]string{"path": "a\uFFFDb"}},
		{"nul key", map[string]string{"k\x00": "v"}, map[string]string{"k\uFFFD": "v"}},
		{"escaped backslash before u0000", map[string]string{"s": `\u0000`}, map[string]string{"s": `\u0000`}},
		{"escaped backslash before nul", map[string]string{"s": "\\\x00"}, map[string]string{"s": "\\\uFFFD"}},
	} {
		raw, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		got := SafeJSON(raw)
		var decoded map[string]string
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatalf("%s: SafeJSON(%s) = %s, not valid JSON: %v", tc.name, raw, got, err)
		}
		if len(decoded) != len(tc.want) {
			t.Fatalf("%s: decoded %q, want %q", tc.name, decoded, tc.want)
		}
		for k, v := range tc.want {
			if decoded[k] != v {
				t.Errorf("%s: decoded %q, want %q", tc.name, decoded, tc.want)
			}
		}
	}
}
