//go:build darwin

package nodeinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// fakeLaunchctl points launchctlPath at a script that logs its calls to
// <dir>/calls and answers bootout with bootoutStatus. print reports the job
// loaded until <dir>/gone exists, which the first print after a bootout
// creates, so the job is still shutting down at that print and gone at the
// next.
func fakeLaunchctl(t *testing.T, bootoutStatus string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	script := `#!/bin/sh
dir='` + dir + `'
echo "$*" >> "$dir/calls"
case "$1" in
print) [ -e "$dir/gone" ] && exit 113; [ -e "$dir/booted" ] && touch "$dir/gone"; exit 0 ;;
bootout) touch "$dir/booted"; exit ` + bootoutStatus + ` ;;
esac
exit 0
`
	path := filepath.Join(dir, "launchctl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := launchctlPath
	launchctlPath = path
	t.Cleanup(func() { launchctlPath = previous })
	return dir
}

func fakeInstaller(t *testing.T) nodeInstaller {
	return nodeInstaller{paths: nodeInstallPathsIn(t.TempDir()), service: "gui/0/" + nodeLaunchdLabel, domain: "gui/0",
		out: io.Discard, drain: transcodenode.DefaultDrainTimeout}
}

func launchctlCalls(t *testing.T, dir string) string {
	t.Helper()
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(calls)
}

// A bootout that answers EINPROGRESS went out: stop waits for the job, still
// loaded at the next print, to go.
func TestStopTreatsBootoutInProgressAsSent(t *testing.T) {
	dir := fakeLaunchctl(t, "36")
	sent, err := fakeInstaller(t).stop(context.Background(), io.Discard)
	if !sent || err != nil {
		t.Fatalf("stop = %v, %v; want sent and no error", sent, err)
	}
	if calls := launchctlCalls(t, dir); !strings.Contains(calls, "bootout") || strings.Count(calls, "print") < 3 {
		t.Fatalf("launchctl calls:\n%s\nwant a bootout, then prints until the job is gone", calls)
	}
}

// A rollback restores the previous plist, and starts its job only when
// launchd had it before the install.
func TestRollbackRestoresWhetherTheJobWasLoaded(t *testing.T) {
	for _, oldLoaded := range []bool{false, true} {
		dir := fakeLaunchctl(t, "0")
		if err := os.WriteFile(filepath.Join(dir, "gone"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		n := fakeInstaller(t)
		if err := os.MkdirAll(filepath.Dir(n.paths.plist), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := n.rollback(context.Background(), nil, []byte("old plist"), oldLoaded); err != nil {
			t.Fatal(err)
		}
		if plist, err := os.ReadFile(n.paths.plist); err != nil || string(plist) != "old plist" {
			t.Fatalf("plist = %q, %v; want the previous one", plist, err)
		}
		if bootstrapped := strings.Contains(launchctlCalls(t, dir), "bootstrap"); bootstrapped != oldLoaded {
			t.Fatalf("old job loaded %v: bootstrapped %v", oldLoaded, bootstrapped)
		}
	}
}

// A retry after a run that stopped mid-swap keeps the backup that run made,
// not the unverified file it left in place.
func TestSwapInKeepsAnExistingBackup(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("f", "unverified")
	write("f.old", "last working")
	write("f.new", "staged")
	write("g", "current")
	write("g.new", "staged")
	swaps := []nodeInstallSwap{{path: filepath.Join(dir, "f")}, {path: filepath.Join(dir, "g")}}
	if err := swapIn(swaps); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"f": "staged", "f.old": "last working", "g": "staged", "g.old": "current"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if !swaps[0].hadOld || !swaps[1].hadOld {
		t.Fatalf("swaps = %+v, want both with a backup", swaps)
	}
}

// The install lock sits outside the folder uninstall removes, so removing the
// folder under a held lock does not let another run take a new one.
func TestNodeInstallLockOutlivesTheInstallFolder(t *testing.T) {
	paths := nodeInstallPathsIn(t.TempDir())
	if strings.HasPrefix(paths.lock, paths.dir+string(filepath.Separator)) {
		t.Fatalf("lock %s is inside %s", paths.lock, paths.dir)
	}
	unlock, err := lockNodeInstall(paths.lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for path, want := range map[string]os.FileMode{filepath.Dir(paths.lock): os.ModeDir | 0o700, paths.lock: 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode() != want {
			t.Fatalf("%s: %v, %v; want mode %v", path, info, err, want)
		}
	}
	if err := os.RemoveAll(paths.dir); err != nil {
		t.Fatal(err)
	}
	if again, err := lockNodeInstall(paths.lock); err == nil {
		again()
		t.Fatal("a second run took the lock while the first held it")
	}
}

// --new-key asks for the key even when the install has one; without it, a
// stored key is kept without asking.
func TestAskNodeKey(t *testing.T) {
	stored := nodeInstallSettings{server: "http://localhost:8090", key: "stored", port: 8100}
	prompted := func(key string) func() (string, error) { return func() (string, error) { return key, nil } }
	noPrompt := func() (string, error) { t.Fatal("asked for a key"); return "", nil }

	if key, err := askNodeKey(stored, nodeInstallSettings{}, noPrompt); err != nil || key != "" {
		t.Fatalf("rerun: key %q, %v; want the stored key kept", key, err)
	}
	if key, err := askNodeKey(stored, nodeInstallSettings{key: nodeKeyPrompt}, prompted("new")); err != nil || key != "new" {
		t.Fatalf("--new-key: key %q, %v; want the entered key", key, err)
	}
	if _, err := askNodeKey(stored, nodeInstallSettings{key: nodeKeyPrompt}, prompted("")); err == nil {
		t.Fatal("--new-key with no key entered kept the stored key")
	}
}

// A key piped in on standard input is its first line, trimmed; the key never
// has to be a command-line argument.
func TestPromptNodeKeyReadsAPipedKey(t *testing.T) {
	for in, want := range map[string]string{"snk1_1_abc\n": "snk1_1_abc", "  snk1_1_abc  ": "snk1_1_abc", "snk1_1_abc\nmore\n": "snk1_1_abc", "": ""} {
		if key, err := promptNodeKey(strings.NewReader(in), io.Discard); err != nil || key != want {
			t.Fatalf("piped %q: key %q, %v; want %q", in, key, err, want)
		}
	}
}
