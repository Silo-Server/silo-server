package main

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// TestEverySignInServiceGetsTheDemoExemption: each auth service the server
// builds for a password sign-in surface (the native login in
// internal/api/router.go, the Audiobookshelf- and Jellyfin-compatible
// logins here) is given the per-name attempt limiter and, with it, the demo
// account exemption. The services are built inside main and NewRouter, so
// this reads their source. Catches a dropped SetDemoSignIn, after which that
// surface would quietly keep the per-name limit for the demo account.
func TestEverySignInServiceGetsTheDemoExemption(t *testing.T) {
	receivers := func(call string) []string {
		t.Helper()
		pattern := regexp.MustCompile(`([A-Za-z_][\w.]*)\.` + call + `\(`)
		var found []string
		for _, path := range []string{"main.go", "../../internal/api/router.go"} {
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range pattern.FindAllSubmatch(source, -1) {
				found = append(found, path+": "+string(match[1]))
			}
		}
		slices.Sort(found)
		return found
	}
	limited, exempted := receivers("SetSignInAttempts"), receivers("SetDemoSignIn")
	if len(limited) != 3 {
		t.Fatalf("services given the attempt limiter: %q, want the three sign-in surfaces", limited)
	}
	if !slices.Equal(limited, exempted) {
		t.Fatalf("services given the attempt limiter %q, given the demo exemption %q; want the same", limited, exempted)
	}
}
