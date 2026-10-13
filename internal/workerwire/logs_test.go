package workerwire

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogSanitizationKeepsDiagnosticsAndRemovesLocationsAndCredentials(t *testing.T) {
	cases := []struct {
		message string
		private []string
	}{
		{`Conversion failed: 'https://user:password@core.invalid/source?grant=opaque'`, []string{"password", "core.invalid", "opaque"}},
		{`Conversion failed: '/private/a/file name.mkv' "C:\private location\file.mkv"`, []string{"/private", "private location", "file.mkv"}},
		{"Conversion failed: Authorization:\x00Bearer\u00a0private-bearer", []string{"private-bearer"}},
		{`Conversion failed: {"access_token":"private-token","node_private_key":"private-key"}`, []string{"private-token", "private-key"}},
	}
	for _, test := range cases {
		got := SanitizeLogMessage(test.message)
		if !strings.Contains(got, "Conversion failed") {
			t.Fatalf("diagnostic removed: %q", got)
		}
		for _, value := range test.private {
			if strings.Contains(got, value) {
				t.Fatalf("sensitive value %q escaped in %q", value, got)
			}
		}
	}
}

func TestLogSanitizationBoundsUTF8AndHostileMessages(t *testing.T) {
	got := SanitizeLogMessage(strings.Repeat("é", 1000))
	if len(got) > MaxLogMessageBytes || !utf8.ValidString(got) {
		t.Fatal("diagnostic exceeded safe UTF8 bound")
	}
	if got := SanitizeLogMessage(strings.Repeat("x", 17000) + "secret=private-value"); len(got) > MaxLogMessageBytes || strings.Contains(got, "private-value") {
		t.Fatal("hostile oversized diagnostic was scanned or retained")
	}
}
