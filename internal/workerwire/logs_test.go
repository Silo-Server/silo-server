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

func TestLogSanitizationRedactsCredentialsNestedInsideQuotedLocations(t *testing.T) {
	for _, message := range []string{
		`Conversion failed: desc = "https://example.test/?api_key='FAKE_FIXTURE_SECRET'"`,
		`Conversion failed: desc = "https://worker.invalid rejected api_key='FAKE_FIXTURE_SECRET'"`,
		`Conversion failed: desc = '/private/location password="FAKE_FIXTURE_SECRET"'`,
		`Conversion failed: desc = "https://example.test rejected Authorization: Bearer FAKE_FIXTURE_SECRET"`,
		`Conversion failed: desc = "https://example.test rejected Bearer FAKE_FIXTURE_SECRET"`,
	} {
		t.Run(message, func(t *testing.T) {
			got := SanitizeLogMessage(message)
			if strings.Contains(got, "FAKE_FIXTURE_SECRET") {
				t.Fatalf("nested credential escaped redaction: %q", got)
			}
			if !strings.Contains(got, "Conversion failed") {
				t.Fatalf("diagnostic removed: %q", got)
			}
		})
	}
}

func TestLogSanitizationPreservesQuotedLocationDelimiters(t *testing.T) {
	for _, message := range []string{
		`Conversion failed: desc = '/private/customer/movie name.mkv password=FAKE_FIXTURE_SECRET'`,
		`Conversion failed: desc = "/private/customer/movie name.mkv api_key=FAKE_FIXTURE_SECRET"`,
		`Conversion failed: desc = '/private/customer/movie name.mkv Bearer FAKE_FIXTURE_SECRET'`,
		`Conversion failed: desc = "C:\private\movie name.mkv Authorization: Basic FAKE_FIXTURE_SECRET"`,
		`Conversion failed: desc = ('/private/customer/movie name.mkv password=FAKE_FIXTURE_SECRET')`,
	} {
		t.Run(message, func(t *testing.T) {
			got := SanitizeLogMessage(message)
			for _, value := range []string{"/private/customer", `C:\private`, "movie name.mkv", "FAKE_FIXTURE_SECRET"} {
				if strings.Contains(got, value) {
					t.Errorf("sensitive value %q escaped in %q", value, got)
				}
			}
			if !strings.Contains(got, "Conversion failed") {
				t.Fatalf("diagnostic removed: %q", got)
			}
		})
	}
}

func TestLogSanitizationRedactsUnterminatedCredentialQuotes(t *testing.T) {
	for _, message := range []string{
		`Conversion failed: password='FAKE_FIXTURE_SECRET`,
		`Conversion failed: api_key="FAKE_FIXTURE_SECRET`,
		`Conversion failed: Authorization: Bearer "FAKE_FIXTURE_SECRET`,
		`Conversion failed: Basic 'FAKE_FIXTURE_SECRET`,
		`Conversion failed: password=FAKE_FIXTURE'SECRET`,
		`Conversion failed: api_key=FAKE_FIXTURE"SECRET`,
	} {
		if got := SanitizeLogMessage(message); strings.Contains(got, "FAKE_FIXTURE") || strings.Contains(got, "SECRET") {
			t.Errorf("unterminated credential escaped in %q", got)
		}
	}
}
