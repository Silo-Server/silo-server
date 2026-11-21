//go:build darwin

package nodeinstall

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// fakeDrainingLaunchctl points launchctlPath at a script whose job runs as pid
// 100 until a kill. The first print after the kill still shows pid 100; the
// next shows afterPID (none when empty), as the job an older plist keeps alive
// would. A bootout then removes the job by the second print after it.
func fakeDrainingLaunchctl(t *testing.T, afterPID string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	script := `#!/bin/sh
dir='` + dir + `'
echo "$*" >> "$dir/calls"
pidline() { [ -n "$1" ] && printf 'state = running\n\tpid = %s\n\tpid-local endpoints = {\n\t\tpid = 1\n\t}\n' "$1"; return 0; }
case "$1" in
print)
	[ -e "$dir/gone" ] && exit 113
	[ -e "$dir/booted" ] && { touch "$dir/gone"; exit 0; }
	if [ -e "$dir/killed" ]; then
		if [ -e "$dir/exited" ]; then pidline '` + afterPID + `'; else touch "$dir/exited"; pidline 100; fi
	else
		pidline 100
	fi
	exit 0 ;;
kill) touch "$dir/killed" ;;
bootout) touch "$dir/booted" ;;
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

// stop sends the running node SIGTERM and waits for that process to exit
// before the bootout, since launchd kills a job a minute after its bootout
// whatever ExitTimeOut asks. A job an older plist restarts under a new pid
// still goes on to the bootout.
func TestStopDrainsBeforeTheBootout(t *testing.T) {
	for _, afterPID := range []string{"", "200"} {
		dir := fakeDrainingLaunchctl(t, afterPID)
		sent, err := fakeInstaller(t).stop(context.Background(), io.Discard)
		if !sent || err != nil {
			t.Fatalf("after pid %q: stop = %v, %v; want sent and no error", afterPID, sent, err)
		}
		calls := strings.Split(strings.TrimSpace(launchctlCalls(t, dir)), "\n")
		want := []string{"print", "kill SIGTERM", "print", "print", "bootout", "print", "print"}
		if len(calls) != len(want) {
			t.Fatalf("after pid %q: launchctl calls %q; want %q", afterPID, calls, want)
		}
		for i := range want {
			if !strings.HasPrefix(calls[i], want[i]) {
				t.Fatalf("after pid %q: launchctl calls %q; want %q", afterPID, calls, want)
			}
		}
	}
}

// A node still draining at the stop's deadline is booted out all the same,
// and the installer says so.
func TestStopBootsOutANodeStillDrainingAtTheDeadline(t *testing.T) {
	previous := nodeStopWait
	nodeStopWait = func(time.Duration) time.Duration { return time.Second }
	t.Cleanup(func() { nodeStopWait = previous })
	dir := fakeDrainingLaunchctl(t, "100")
	var out bytes.Buffer
	sent, err := fakeInstaller(t).stop(context.Background(), &out)
	if !sent || err != nil {
		t.Fatalf("stop = %v, %v; want sent and no error", sent, err)
	}
	if !strings.Contains(launchctlCalls(t, dir), "bootout") {
		t.Fatal("no bootout after the deadline")
	}
	if !strings.Contains(out.String(), "still finishing streams") {
		t.Fatalf("output %q does not say the node is stopped mid-stream", out.String())
	}
}

// A rollback that cannot confirm the new job stopped restores nothing and
// keeps the backups, which a node that may still run must not lose.
func TestRollbackKeepsBackupsWhenTheStopFails(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$*\" >> '" + dir + "/calls'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := launchctlPath
	launchctlPath = filepath.Join(dir, "launchctl")
	t.Cleanup(func() { launchctlPath = previous })
	n := fakeInstaller(t)
	if err := os.MkdirAll(filepath.Dir(n.paths.plist), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{n.paths.plist: "new plist", n.paths.env: "new env", n.paths.env + ".old": "old env"}
	for path, data := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := n.rollback(context.Background(), []nodeInstallSwap{{path: n.paths.env, hadOld: true, placed: true}}, []byte("old plist"), true)
	if err == nil || !strings.Contains(err.Error(), "recovery is incomplete") || !strings.Contains(err.Error(), n.paths.env+".old") {
		t.Fatalf("rollback = %v; want incomplete recovery naming the backup", err)
	}
	for path, want := range files {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q left in place", path, got, err, want)
		}
	}
	if strings.Contains(launchctlCalls(t, dir), "bootstrap") {
		t.Fatal("bootstrapped the previous job while the new one may still run")
	}
}

// A run stopped mid-swap leaves node.env and node-key only at their backups;
// a rerun without flags takes its settings from them. Without either, the
// install is new and needs --server.
func TestStoredReadsAnInterruptedSwapsBackups(t *testing.T) {
	n := fakeInstaller(t)
	if err := os.MkdirAll(n.paths.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if stored, _, err := n.stored(); err != nil || stored != (nodeInstallSettings{}) {
		t.Fatalf("no install: stored = %+v, %v; want nothing", stored, err)
	}
	if _, err := resolveNodeInstall(nodeInstallSettings{}, nodeInstallSettings{}); err == nil || !strings.Contains(err.Error(), "--server is required") {
		t.Fatalf("no install: resolve = %v; want --server required", err)
	}
	env := "MODE=\"transcode\"\nSILO_CORE_URL=\"http://localhost:8090\"\nPORT=\"8200\"\nSILO_NODE_DRAIN_TIMEOUT=\"15m\"\n"
	for path, data := range map[string]string{n.paths.env + ".old": env, n.paths.key + ".old": "old-key\n"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stored, extra, err := n.stored()
	want := nodeInstallSettings{server: "http://localhost:8090", key: "old-key", port: 8200}
	if err != nil || stored != want || extra[transcodenode.EnvDrainTimeout] != "15m" {
		t.Fatalf("stored = %+v, %v, %v; want %+v and the drain kept", stored, extra, err, want)
	}
}

// --server is a plain URL: the probe and the node join paths onto it, and
// userinfo would go out as Basic auth and show in messages.
func TestValidateRefusesServerURLsWithMore(t *testing.T) {
	for _, server := range []string{"http://user:pass@localhost:8090", "http://localhost:8090/?a=b", "http://localhost:8090?", "http://localhost:8090/#x", "http://localhost:8090#"} {
		err := nodeInstallSettings{server: server, port: 8100}.validateAllButKey()
		if err == nil || !strings.Contains(err.Error(), "plain http or https URL") {
			t.Errorf("--server %q: %v; want refused", server, err)
		}
	}
}

// The readiness probe keeps the path of a server behind a prefix, with or
// without a trailing slash.
func TestCheckServerReadyKeepsAPathPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/silo/api/v1/ready" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	for _, url := range []string{server.URL + "/silo", server.URL + "/silo/"} {
		if err := (nodeInstallSettings{server: url, port: 8100}).validateAllButKey(); err != nil {
			t.Fatalf("--server %q: %v", url, err)
		}
		if err := checkServerReady(context.Background(), url); err != nil {
			t.Fatalf("--server %q: %v", url, err)
		}
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

// Without the swap marker, .old files are a finished run's leftovers and go;
// with it, they are the last working install and stay.
func TestDropStaleBackupsFollowsTheSwapMarker(t *testing.T) {
	n := nodeInstaller{paths: nodeInstallPathsIn(t.TempDir())}
	if err := os.MkdirAll(n.paths.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeBackups := func() {
		for _, path := range n.paths.installedFiles() {
			if err := os.WriteFile(path+".old", []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	countBackups := func() (n2 int) {
		for _, path := range n.paths.installedFiles() {
			if _, err := os.Stat(path + ".old"); err == nil {
				n2++
			}
		}
		return n2
	}

	writeBackups()
	if err := os.WriteFile(n.paths.swapPending(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	n.dropStaleBackups()
	if got := countBackups(); got != 3 {
		t.Fatalf("with the marker, %d of 3 backups kept; want all", got)
	}

	if err := os.Remove(n.paths.swapPending()); err != nil {
		t.Fatal(err)
	}
	n.dropStaleBackups()
	if got := countBackups(); got != 0 {
		t.Fatalf("without the marker, %d backups left; want none", got)
	}
}
