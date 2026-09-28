//go:build linux

package mediasample

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// Keep the main goroutine on the main thread past initialization, so TestMain
// can start a background child from the main thread.
func init() { runtime.LockOSThread() }

// mainThreadStart records a background start made from the main thread.
var mainThreadStart struct {
	onMainThread bool
	err          error
	// baseNice is the main thread's nice value before the start.
	baseNice int
	// childNice, threadNice, and threadIO are the child's nice value and the
	// main thread's nice value and I/O priority after the start.
	childNice, threadNice, threadIO int
}

func TestMain(m *testing.M) {
	mainThreadStart.onMainThread = unix.Gettid() == unix.Getpid()
	mainThreadStart.baseNice, _ = readNice("/proc/self/stat")
	cmd := exec.Command("sleep", "30")
	mainThreadStart.err = startFromMainThread(cmd)
	if mainThreadStart.err == nil {
		mainThreadStart.childNice, _ = readNice(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
		mainThreadStart.threadNice, _ = readNice("/proc/self/stat")
		prio, _, _ := unix.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(unix.Getpid()), 0)
		mainThreadStart.threadIO = int(prio)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	os.Exit(m.Run())
}

// startFromMainThread runs the background start path on the calling thread,
// as the goroutine startBackground spawns would.
func startFromMainThread(cmd *exec.Cmd) error {
	started := make(chan error, 1)
	startFromDiscardedThread(cmd, started)
	return <-started
}

func TestBackgroundPriorityFromMainThread(t *testing.T) {
	requirePriorityLowering(t)
	got := mainThreadStart
	if !got.onMainThread {
		t.Fatal("TestMain did not run on the main thread")
	}
	if got.baseNice == backgroundNice {
		t.Skipf("the test already runs at nice %d", backgroundNice)
	}
	if got.err != nil {
		t.Fatalf("starting sleep: %v", got.err)
	}
	if got.childNice != backgroundNice {
		t.Fatalf("background child nice %d, want %d", got.childNice, backgroundNice)
	}
	if got.threadNice != got.baseNice {
		t.Fatalf("main thread nice changed from %d to %d", got.baseNice, got.threadNice)
	}
	if got.threadIO == backgroundIOPrio {
		t.Fatalf("main thread left at I/O priority %#x (idle)", got.threadIO)
	}
}

// requirePriorityLowering skips the test when this environment does not allow
// lowering a thread's priority, for example under a seccomp profile that
// blocks ioprio_set. The probe runs on a locked thread that is discarded.
func requirePriorityLowering(t *testing.T) {
	t.Helper()
	probe := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread is discarded
		probe <- lowerThreadPriority()
	}()
	if err := <-probe; err != nil {
		t.Skipf("cannot lower thread priority here: %v", err)
	}
}

// readNice reads the nice value from a /proc stat file.
func readNice(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	nice, ok := parseStatNice(string(data))
	if !ok {
		return 0, fmt.Errorf("no nice value in %s: %q", path, data)
	}
	return nice, nil
}

// statNice reads the nice value (field 19) from a /proc stat file.
func statNice(t *testing.T, path string) int {
	t.Helper()
	nice, err := readNice(path)
	if err != nil {
		t.Fatal(err)
	}
	return nice
}

// parseStatNice extracts the nice value from a /proc stat line. The command
// name, field 2, is parenthesized and may hold spaces, so fields are counted
// from the closing parenthesis; nice is the 17th after it.
func parseStatNice(stat string) (int, bool) {
	fields := strings.Fields(stat[strings.LastIndexByte(stat, ')')+1:])
	if len(fields) < 17 {
		return 0, false
	}
	nice, err := strconv.Atoi(fields[16])
	return nice, err == nil
}

// ioPriority returns the I/O priority of the thread or process id; zero means
// the calling thread.
func ioPriority(t *testing.T, id int) int {
	t.Helper()
	prio, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(id), 0)
	if errno != 0 {
		t.Fatalf("ioprio_get(%d): %v", id, errno)
	}
	return int(prio)
}

// startSleep starts a short-lived child through the runner's start path and
// returns it running. The child is killed when the test ends.
func startSleep(t *testing.T, background bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := startCommand(cmd, background); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestBackgroundPriorityAppliesToChildOnly(t *testing.T) {
	requirePriorityLowering(t)
	// Pin the test goroutine so its own thread's priority can be read before
	// and after.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	selfStat := fmt.Sprintf("/proc/self/task/%d/stat", unix.Gettid())
	selfNice := statNice(t, selfStat)
	selfIO := ioPriority(t, 0)
	if selfNice == backgroundNice {
		t.Skipf("the test already runs at nice %d", backgroundNice)
	}

	// Each start locks and discards a thread of its own.
	for range 5 {
		child := startSleep(t, true)
		pid := child.Process.Pid
		if nice := statNice(t, fmt.Sprintf("/proc/%d/stat", pid)); nice != backgroundNice {
			t.Fatalf("background child nice %d, want %d", nice, backgroundNice)
		}
		if prio := ioPriority(t, pid); prio != backgroundIOPrio {
			t.Fatalf("background child I/O priority %#x, want %#x (idle)", prio, backgroundIOPrio)
		}
	}

	if nice := statNice(t, selfStat); nice != selfNice {
		t.Fatalf("calling thread nice changed from %d to %d", selfNice, nice)
	}
	if prio := ioPriority(t, 0); prio != selfIO {
		t.Fatalf("calling thread I/O priority changed from %#x to %#x", selfIO, prio)
	}

	// The threads that forked the children exit with their goroutines,
	// leaving no thread of this process, main thread included, at the
	// lowered priority.
	deadline := time.Now().Add(5 * time.Second)
	for {
		lowered := loweredThreads(t)
		if len(lowered) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("threads %v still run at nice %d", lowered, backgroundNice)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestForegroundStartKeepsNormalPriority(t *testing.T) {
	selfNice := statNice(t, "/proc/self/stat")
	child := startSleep(t, false)
	if nice := statNice(t, fmt.Sprintf("/proc/%d/stat", child.Process.Pid)); nice != selfNice {
		t.Fatalf("foreground child nice %d, want the server's %d", nice, selfNice)
	}
}

// loweredThreads lists this process's threads running at background nice.
func loweredThreads(t *testing.T) []string {
	t.Helper()
	tasks, err := filepath.Glob("/proc/self/task/*/stat")
	if err != nil {
		t.Fatal(err)
	}
	var lowered []string
	for _, stat := range tasks {
		data, err := os.ReadFile(stat)
		if err != nil {
			continue // the thread exited
		}
		if nice, ok := parseStatNice(string(data)); ok && nice == backgroundNice {
			lowered = append(lowered, filepath.Base(filepath.Dir(stat)))
		}
	}
	return lowered
}

func TestRunBackgroundPriority(t *testing.T) {
	requirePriorityLowering(t)
	selfNice := statNice(t, "/proc/self/stat")
	if selfNice == backgroundNice {
		t.Skipf("the test already runs at nice %d", backgroundNice)
	}
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%t", background), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "nice")
			// $$ is the fake ffmpeg itself.
			ffmpeg := writeScript(t, fmt.Sprintf(`cut -d' ' -f19 /proc/$$/stat > %q`, out))
			req := validRequest()
			req.Background = background
			runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}
			if _, err := runner.Run(context.Background(), req); err != nil {
				t.Fatalf("Run: %v", err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			want := selfNice
			if background {
				want = backgroundNice
			}
			if got := strings.TrimSpace(string(data)); got != strconv.Itoa(want) {
				t.Fatalf("ffmpeg nice %s, want %d", got, want)
			}
		})
	}
}
