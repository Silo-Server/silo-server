package jellycompat

import (
	"os"
	"runtime"
	"testing"
)

// The Jellyfin Web install lock treats an empty process token as "unknown" and
// falls back to lock age, so a platform without a token reader silently loses
// the ability to tell a live installer from a crashed one. These tests pin the
// token reader on every platform Silo actually runs on.

func TestCurrentProcessTokenIdentifiesThisProcess(t *testing.T) {
	token := currentProcessToken()

	switch runtime.GOOS {
	case "linux", "darwin":
		if token == "" {
			t.Fatalf("currentProcessToken() = %q on %s, want a process start token", token, runtime.GOOS)
		}
	default:
		t.Skipf("no process token reader for %s", runtime.GOOS)
	}

	if again := processToken(os.Getpid()); again != token {
		t.Fatalf("processToken(self) = %q on a second read, want the stable token %q", again, token)
	}
}

func TestProcessTokenRejectsInvalidPIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -999} {
		if token := processToken(pid); token != "" {
			t.Fatalf("processToken(%d) = %q, want empty", pid, token)
		}
	}
}

func TestProcessTokenIsEmptyForUnusedPID(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("no process token reader for %s", runtime.GOOS)
	}

	pid, ok := unusedPID()
	if !ok {
		t.Skip("no unused PID available to probe")
	}
	if token := processToken(pid); token != "" {
		t.Fatalf("processToken(%d) = %q for an unused PID, want empty", pid, token)
	}
}

// unusedPID finds a PID with no live process behind it, so the probe cannot
// flake on a machine that happens to have a process at a hardcoded number.
func unusedPID() (int, bool) {
	for pid := 999999; pid > 999000; pid-- {
		if !processIsRunning(pid) {
			return pid, true
		}
	}
	return 0, false
}
