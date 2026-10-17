package workeridentity

import "testing"

func TestSameOriginRequiresValidEquivalentAuthority(t *testing.T) {
	for _, tc := range []struct {
		left, right      string
		allowHTTP, match bool
	}{
		{"https://WORKER.example:443/", "https://worker.example", false, true},
		{"http://WORKER.example:80/", "http://worker.example", true, true},
		{"https://[2001:DB8::1]:443/", "https://[2001:db8::1]", false, true},
		{"https://worker.example/path", "https://worker.example", false, false},
		{"https://worker.example?query", "https://worker.example", false, false},
		{"https://worker.example#fragment", "https://worker.example", false, false},
		{"https://user@worker.example", "https://worker.example", false, false},
		{"http://worker.example", "http://worker.example", false, false},
		{"https://worker.example:444", "https://worker.example", false, false},
		{"https://worker.example.attacker", "https://worker.example", false, false},
	} {
		if match := SameOrigin(tc.left, tc.right, tc.allowHTTP); match != tc.match {
			t.Errorf("SameOrigin(%q,%q,%v)=%v want%v", tc.left, tc.right, tc.allowHTTP, match, tc.match)
		}
	}
}
