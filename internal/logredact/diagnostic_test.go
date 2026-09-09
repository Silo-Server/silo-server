package logredact

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeQueryCredentialAliases(t *testing.T) {
	for _, key := range []string{"ApiKey", "api_key", "API_KEY", "%41piKey", "Api%5fKey", "AccessToken", "X-Emby-Token", "Pw", "PASSWORD", "Authorization", "Cookie"} {
		t.Run(key, func(t *testing.T) {
			got := SanitizeQuery(key + "=first-secret&" + key + "=second-secret&Limit=20&Tag=a&Tag=b")
			if strings.Contains(got, "secret") {
				t.Fatalf("credential leaked: %q", got)
			}
			query, err := url.ParseQuery(got)
			if err != nil || query.Get("Limit") != "20" || strings.Join(query["Tag"], ",") != "a,b" {
				t.Fatalf("lost harmless diagnostics: %q, %v", got, err)
			}
		})
	}
}

func TestSanitizeQueryFailsClosed(t *testing.T) {
	for _, raw := range []string{"ApiKey=secret%ZZ", "ApiKey=secret;Limit=20", "ApiKey=secret&bad=%"} {
		if got := SanitizeQuery(raw); strings.Contains(got, "secret") || !strings.Contains(got, "omitted") {
			t.Fatalf("invalid query not omitted: %q", got)
		}
	}
	if got := SanitizeQuery(""); got != "" {
		t.Fatalf("empty query = %q", got)
	}
}

func TestSanitizeRequestURL(t *testing.T) {
	for _, raw := range []string{
		"/stream?ApiKey=query-secret&Static=true#fragment-secret",
		"https://operator:user-secret@example.com/stream?api_key=query-secret&Static=true#fragment-secret",
	} {
		got := SanitizeRequestURL(raw)
		if strings.Contains(got, "secret") || !strings.Contains(got, "/stream?") || !strings.Contains(got, "Static=true") {
			t.Fatalf("unsafe or unhelpful URL: %q", got)
		}
	}
	for _, raw := range []string{"http://%secret", "scheme:opaque-secret"} {
		if got := SanitizeRequestURL(raw); got != invalidURLPlaceholder {
			t.Fatalf("invalid/opaque URL not omitted: %q", got)
		}
	}
}

func TestSanitizeJSONNestedCredentialsAndExactNumbers(t *testing.T) {
	const body = `{"Pw":"first-secret","Pw":"duplicate-secret","p\u0077":"escaped-secret","API_KEY":{"nested":"object-secret"},"Items":[{"AccessToken":"session-secret","Url":"/stream?%41piKey=url-secret&Static=true"}],"PositionTicks":9007199254740993,"Enabled":true,"Name":"Example","Optional":null}`
	got := SanitizeJSON([]byte(body))
	if strings.Contains(string(got), "secret") || !json.Valid(got) {
		t.Fatalf("unsafe JSON: %s", got)
	}
	for _, want := range []string{"9007199254740993", `"Enabled": true`, `"Name": "Example"`, `"Optional": null`, "Static=true", Placeholder} {
		if !strings.Contains(string(got), want) {
			t.Errorf("lost diagnostic %q: %s", want, got)
		}
	}
	if !strings.Contains(body, "first-secret") {
		t.Fatal("input was modified")
	}
}

func TestSanitizeJSONOmitsUnsafeRepresentations(t *testing.T) {
	for _, body := range []string{
		`{"Pw":"truncated-secret"`,
		`{"Pw":"first-secret"} {"Pw":"second-secret"}`,
		`Pw=form-secret`,
		`<Password>xml-secret</Password>`,
		`"scalar-secret"`,
		"\xffbinary-secret",
	} {
		got := string(SanitizeJSON([]byte(body)))
		if strings.Contains(got, "secret") || !strings.Contains(got, "omitted") {
			t.Fatalf("unsafe body not omitted: %q", got)
		}
	}
}

func TestSanitizeJSONPreservesOrdinaryText(t *testing.T) {
	for _, value := range []string{"Can he escape? Nobody knows", "Why?", "Read https://example.com for details?", "A story? Part #2", "Still here?\nYes."} {
		body, err := json.Marshal(map[string]string{"Overview": value})
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		if err := json.Unmarshal(SanitizeJSON(body), &got); err != nil {
			t.Fatal(err)
		}
		if got["Overview"] != value {
			t.Errorf("ordinary text changed: got %q, want %q", got["Overview"], value)
		}
	}
}

func TestSanitizeJSONRedactsURLFormsWithSpaces(t *testing.T) {
	for _, value := range []string{
		"/stream?ApiKey=url-secret&Name=Two Words",
		"https://example.com/stream?api_key=url-secret&Name=Two Words",
		"HTTPS://example.com/stream?API_KEY=url-secret",
		"https://operator:url-secret@%invalid/stream",
		" /stream?ApiKey=url-secret&Name=Two Words",
	} {
		body, err := json.Marshal(map[string]string{"DirectStreamUrl": value})
		if err != nil {
			t.Fatal(err)
		}
		if got := string(SanitizeJSON(body)); strings.Contains(got, "url-secret") {
			t.Errorf("URL credential escaped redaction: %s", got)
		}
	}
}
