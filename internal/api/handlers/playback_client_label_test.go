package handlers

import "testing"

// TestGrantedAppPlayLabelIsTheServersName: a play of a session another app's
// key opened is stored under the name "Granted App", with a version, build
// and channel only when they look like versions (the origin gate drops the
// rest, so "(Silo for Apple TV)" arrives as none). The compact and full labels
// the session views and the household streams read show are then that name
// alone, and the user agent the app sent is not joined into them. Catches a
// label that joins caller-chosen text onto the reserved name.
func TestGrantedAppPlayLabelIsTheServersName(t *testing.T) {
	const name, userAgent = "Granted App", "Mozilla/5.0 (Silo for Apple TV)"
	if got := playbackClientDisplayName(name, "", userAgent); got != name {
		t.Fatalf("label %q, want %q", got, name)
	}
	if got := playbackClientFullDisplayName(name, "", "", "", userAgent); got != name {
		t.Fatalf("full label %q, want %q", got, name)
	}
	if got := playbackClientFullDisplayName(name, "2.4.0", "1400", "beta", userAgent); got != "Granted App 2.4.0 (build 1400, beta)" {
		t.Fatalf("full label with a version %q", got)
	}
}
