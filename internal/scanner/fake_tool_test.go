package scanner

import (
	"os"
	"syscall"
	"testing"
)

// writeFakeTool writes an executable stub for ffmpeg or ffprobe and fails the
// test if it cannot.
//
// The fork lock is what makes this different from a plain os.WriteFile, and it
// is the whole point of the helper. execve refuses a file that anyone still has
// open for writing, with ETXTBSY — "text file busy". Go opens the file
// O_CLOEXEC, so the fd is not meant to outlive an exec, but O_CLOEXEC only
// takes effect at the child's execve: a fork landing between this open and its
// close leaves that child holding a copy of the writing fd for the whole time
// it takes to exec something else. A test that then runs the stub it just
// wrote fails, and it fails for whichever test happened to be next to a fork —
// never the same one twice, and never on a laptop running one package at a
// time.
//
// syscall.ForkLock is the lock os/exec takes around fork for exactly this class
// of problem. Holding it for the write means no fork can observe the fd at all,
// so the window does not exist rather than being waited out: no retry, no
// sleep, no exec of a stub whose side effects a test is about to assert on.
func writeFakeTool(t *testing.T, path, script string) {
	t.Helper()
	syscall.ForkLock.Lock()
	err := os.WriteFile(path, []byte(script), 0o755)
	syscall.ForkLock.Unlock()
	if err != nil {
		t.Fatalf("writing fake tool %s: %v", path, err)
	}
}
