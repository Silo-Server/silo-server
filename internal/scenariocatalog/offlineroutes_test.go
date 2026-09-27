package scenariocatalog

import (
	"strings"
	"testing"
)

func TestOfflineRoutesRoundTrip(t *testing.T) {
	wiring := []string{"Config", "DB"}
	encoded := EncodeOfflineRoutes(wiring, []string{"POST /b", "GET /a", "GET /a"})
	set, err := DecodeOfflineRoutes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.Wiring, " "); got != "Config DB" {
		t.Fatalf("wiring = %q", got)
	}
	if len(set.Routes) != 2 || !set.Has("GET", "/a") || !set.Has("POST", "/b") || set.Has("GET", "/b") {
		t.Fatalf("routes = %v", set.Routes)
	}
	// Sorted and deduplicated, header comments first.
	body := strings.TrimPrefix(string(encoded), offlineRoutesHeader)
	if body != "wiring: Config DB\nGET /a\nPOST /b\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestDecodeOfflineRoutesRejectsWeakFiles(t *testing.T) {
	cases := map[string]string{
		"no wiring":      "GET /a\n",
		"empty wiring":   "wiring:\nGET /a\n",
		"two wirings":    "wiring: A\nwiring: B\nGET /a\n",
		"no routes":      "# only a header\nwiring: Config\n",
		"unrooted path":  "wiring: Config\nGET a\n",
		"lowercase verb": "wiring: Config\nget /a\n",
		"three fields":   "wiring: Config\nGET /a extra\n",
	}
	for name, data := range cases {
		if _, err := DecodeOfflineRoutes([]byte(data)); err == nil {
			t.Errorf("%s: decoded without error", name)
		}
	}
}

func TestLoadOfflineRoutesReadsCommittedFile(t *testing.T) {
	set, err := LoadOfflineRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if !set.Has("GET", "/api/v1/health") {
		t.Fatal("committed offline route set lacks GET /api/v1/health, which registers unconditionally")
	}
}
