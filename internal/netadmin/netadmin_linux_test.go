//go:build linux && cgo

package netadmin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestHelperRefusesInvalidRequests(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "1" {
		t.Skip("requires SILO_TEST_NET_ADMIN=1 in a Linux container with NET_ADMIN")
	}
	if state := Current(); state.Helper != HelperRunning {
		t.Fatalf("NET_ADMIN helper is not running: Current() = %+v", state)
	}
	helper.Lock()
	defer helper.Unlock()

	request := func(operation, size uint32) []byte {
		data := make([]byte, 8)
		binary.LittleEndian.PutUint32(data[:4], operation)
		binary.LittleEndian.PutUint32(data[4:], size)
		return data
	}
	force := request(2, 33554432)
	valid := protocolSocket(t, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP, nil)
	rows := []struct {
		name                    string
		data                    []byte
		count, domain, kind, ip int
	}{
		{"short request", force[:3], 1, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"empty request", nil, 0, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"zero descriptors", force, 0, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"two descriptors", force, 2, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"UDP socket", force, 1, unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_UDP},
		{"Unix socket", force, 1, unix.AF_UNIX, unix.SOCK_STREAM, 0},
		{"unknown operation", request(99, 0), 1, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"zero force size", request(2, 0), 1, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"oversized force", request(2, 1073741824), 1, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"congestion payload", request(1, 1), 1, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"legacy payload", []byte{2, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0}, 1, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
		{"truncated packet", append(force, 0), 1, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP},
	}
	// An oversized seqpacket sets MSG_TRUNC on the helper's receive. MSG_CTRUNC
	// cannot be forced with SCM_RIGHTS here: the helper's control buffer holds
	// all 253 descriptors Linux permits in one message, and it does not enable
	// automatic credentials or security labels that could consume extra space.
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var sockets []*os.File
			var fds []int
			var addresses []unix.Sockaddr
			for range row.count {
				var address unix.Sockaddr
				if row.ip == unix.IPPROTO_TCP {
					address = &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
				}
				socket := protocolSocket(t, row.domain, row.kind, row.ip, address)
				fd := int(socket.Fd())
				sockets = append(sockets, socket)
				fds = append(fds, fd)
				if address != nil {
					bound, err := unix.Getsockname(fd)
					if err != nil {
						t.Fatalf("read bound TCP address: %v", err)
					}
					addresses = append(addresses, bound)
				}
			}
			if err := rawHelperRequest(t, row.data, fds); !errors.Is(err, unix.EINVAL) {
				t.Errorf("refusal reply = %v, want EINVAL", err)
			}
			if err := rawHelperRequest(t, force, []int{int(valid.Fd())}); err != nil {
				t.Errorf("valid request after refusal = %v, want success", err)
			}
			for _, socket := range sockets {
				if err := socket.Close(); err != nil {
					t.Fatalf("close sender's descriptor: %v", err)
				}
			}
			// A leaked helper reference keeps its port bound after the sender
			// closes its copy. Rebinding checks each descriptor, including both
			// descriptors in the request with too many descriptors.
			for _, address := range addresses {
				protocolSocket(t, unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP, address)
			}
		})
	}
}

// A closed fd 2 at exec must not become the helper channel, or log writes
// reach the helper and its replies fall out of step with requests.
func TestHelperChannelAvoidsClosedStdio(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "closed_stderr" {
		t.Skip("requires SILO_TEST_NET_ADMIN=closed_stderr in a Linux container with NET_ADMIN, started with fd 2 closed")
	}
	if state := Current(); state.Helper != HelperRunning || helper.fd <= 2 {
		t.Fatalf("helper fd = %d with Current() = %+v, want a running helper above fd 2", helper.fd, state)
	}
	if target, err := os.Readlink("/proc/self/fd/2"); err != nil || target != "/dev/null" {
		t.Fatalf("fd 2 = %q (%v), want /dev/null", target, err)
	}
}

func protocolSocket(t *testing.T, domain, kind, protocol int, address unix.Sockaddr) *os.File {
	t.Helper()
	fd, err := unix.Socket(domain, kind|unix.SOCK_CLOEXEC, protocol)
	if err != nil {
		t.Fatalf("create protocol socket: %v", err)
	}
	socket := os.NewFile(uintptr(fd), "helper protocol socket")
	t.Cleanup(func() {
		if err := socket.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close protocol socket: %v", err)
		}
	})
	if address != nil {
		if err := unix.Bind(fd, address); err != nil {
			t.Fatalf("bind TCP socket (a helper reference may still be open): %v", err)
		}
	}
	return socket
}

func rawHelperRequest(t *testing.T, request []byte, fds []int) error {
	t.Helper()
	var rights []byte
	if len(fds) != 0 {
		rights = unix.UnixRights(fds...)
	}
	if err := unix.Sendmsg(helper.fd, request, rights, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatalf("send raw helper request: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		remaining := time.Until(deadline).Milliseconds()
		if remaining <= 0 {
			t.Fatal("timed out waiting for helper reply")
		}
		fds := []unix.PollFd{{Fd: int32(helper.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(remaining))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 {
			t.Fatalf("wait for helper reply: ready=%d error=%v", n, err)
		}
		return receiveReply()
	}
}

func TestRuntimeNoNewPrivsWithoutNetAdmin(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "no_net_admin_nnp" {
		t.Skip("requires SILO_TEST_NET_ADMIN=no_net_admin_nnp without NET_ADMIN and with no-new-privileges")
	}
	state := Current()
	if state.HeldAtStart || state.Helper != HelperNone {
		t.Fatalf("requires no NET_ADMIN and no helper; Current() = %+v", state)
	}
	if !state.NoNewPrivs {
		t.Errorf("NoNewPrivs = false, want true for runtime-set no_new_privs; Current() = %+v", state)
	}
}

func TestConstructorContainsRefusedAmbientGrant(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "ambient_refused" {
		t.Skip("requires inheritable and bounding NET_ADMIN, no effective grant, and ambient raises refused")
	}
	state := Current()
	if state.HeldAtStart || state.Helper != HelperNone {
		t.Errorf("Current() = %+v, want no effective grant and no helper", state)
	}
	if state.ChildrenInherit || !state.NoNewPrivs || state.Errno != "" || state.Threads != 1 {
		t.Errorf("Current() = %+v, want contained fallback, no_new_privs, no errno and one startup thread", state)
	}
	check := func(source string, contents []byte) {
		t.Helper()
		status := readCapabilityStatus(t, source, contents)
		for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapAmb"} {
			if status[field] != 0 {
				t.Errorf("%s: %s = %#x, want 0", source, field, status[field])
			}
		}
		// The unprivileged fixture has no CAP_SETPCAP, so containment must
		// protect exec even when the bounding bit cannot be dropped.
		if status["NoNewPrivs"] != 1 {
			t.Errorf("%s: NoNewPrivs = %d, want 1; CapBnd = %#x", source, status["NoNewPrivs"], status["CapBnd"])
		}
	}
	for _, path := range threadStatusPaths(t) {
		check(path, readStatusFile(t, path))
	}
	output, err := exec.CommandContext(t.Context(), "cat", "/proc/self/status").CombinedOutput()
	if err != nil {
		t.Fatalf("child cat /proc/self/status: %v; output: %s", err, output)
	}
	check("child cat /proc/self/status", output)

	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	LogState(t.Context())
	if !strings.Contains(log.String(), " children_inherit=false ") || !strings.Contains(log.String(), " no_new_privs=true ") {
		t.Errorf("fallback log = %q, want contained children and no_new_privs", log.String())
	}
}

func TestChildrenStartWithoutNetAdmin(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "1" {
		t.Skip("requires SILO_TEST_NET_ADMIN=1 in a Linux container with NET_ADMIN")
	}

	state := Current()
	if !state.HeldAtStart {
		t.Errorf("HeldAtStart = false, want true; Current() = %+v", state)
	}
	if state.Helper != HelperRunning {
		t.Errorf("Helper = %q, want %q; Current() = %+v", state.Helper, HelperRunning, state)
	}
	if state.ChildrenInherit {
		t.Errorf("ChildrenInherit = true, want false; Current() = %+v", state)
	}
	if state.Threads != 1 {
		t.Errorf("Threads = %d, want 1; Current() = %+v", state.Threads, state)
	}

	for _, path := range threadStatusPaths(t) {
		status := readCapabilityStatus(t, path, readStatusFile(t, path))
		fields := []string{"CapInh", "CapPrm", "CapEff", "CapAmb"}
		if status["NoNewPrivs"] != 1 || status["CapEff"]&(1<<unix.CAP_SETPCAP) != 0 {
			fields = append(fields, "CapBnd")
		}
		assertNetAdminAbsent(t, path, status, fields)
	}

	// A root exec can regain permitted capabilities from the bounding set.
	// Read the executed child's status rather than trusting the parent's state.
	output, err := exec.CommandContext(t.Context(), "cat", "/proc/self/status").CombinedOutput()
	if err != nil {
		t.Fatalf("child cat /proc/self/status: %v; output: %s", err, output)
	}
	status := readCapabilityStatus(t, "child cat /proc/self/status", output)
	assertNetAdminAbsent(t, "child cat /proc/self/status", status, []string{"CapEff", "CapPrm"})
}

func TestUnsupportedAmbientSetDoesNotReportError(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "ambient_unsupported" {
		t.Skip("requires SILO_TEST_NET_ADMIN=ambient_unsupported and a Linux NET_ADMIN container with ambient prctl returning EINVAL")
	}
	if _, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, unix.CAP_NET_ADMIN, 0, 0); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("ambient prctl = %v, want EINVAL from seccomp", err)
	}
	state := Current()
	if !state.HeldAtStart || state.Helper != HelperRunning {
		t.Fatalf("requires NET_ADMIN and a running helper; Current() = %+v", state)
	}
	if state.Errno != "" {
		t.Errorf("Errno = %q, want empty for an unsupported ambient set; Current() = %+v", state.Errno, state)
	}
	if state.ChildrenInherit {
		t.Errorf("ChildrenInherit = true, want false for an unsupported ambient set; Current() = %+v", state)
	}
}

func TestInspectFailureReportsUnprovenContainment(t *testing.T) {
	if os.Getenv("SILO_TEST_NET_ADMIN") != "capget_denied" {
		t.Skip("requires SILO_TEST_NET_ADMIN=capget_denied and a Linux NET_ADMIN container with capget denied by seccomp")
	}

	state := Current()
	if state.HeldAtStart {
		t.Errorf("HeldAtStart = true, want false for a failed inspection; Current() = %+v", state)
	}
	if state.Helper != HelperInspectFailed {
		t.Errorf("Helper = %q, want %q; Current() = %+v", state.Helper, HelperInspectFailed, state)
	}
	if !state.ChildrenInherit {
		t.Errorf("ChildrenInherit = false, want true for unproven containment; Current() = %+v", state)
	}
	if !state.NoNewPrivs {
		t.Errorf("NoNewPrivs = false, want true after a failed inspection; Current() = %+v", state)
	}
	if state.Errno != "EPERM" {
		t.Errorf("Errno = %q, want EPERM; Current() = %+v", state.Errno, state)
	}
	if state.Threads != 1 {
		t.Errorf("Threads = %d, want 1; Current() = %+v", state.Threads, state)
	}

	for _, path := range threadStatusPaths(t) {
		status := readCapabilityStatus(t, path, readStatusFile(t, path))
		assertNetAdminAbsent(t, path, status, []string{"CapBnd", "CapAmb"})
	}
}

func threadStatusPaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("/proc/self/task/*/status")
	if err != nil {
		t.Fatalf("enumerate thread status files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no thread status files found in /proc/self/task")
	}
	return paths
}

func readStatusFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}

func readCapabilityStatus(t *testing.T, source string, contents []byte) map[string]uint64 {
	t.Helper()
	status := make(map[string]uint64)
	for _, line := range strings.Split(string(contents), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		base := 16
		switch name {
		case "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb":
		case "NoNewPrivs":
			base = 10
		default:
			continue
		}
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), base, 64)
		if err != nil {
			t.Fatalf("%s: parse %s: %v", source, name, err)
		}
		status[name] = parsed
	}
	for _, name := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		if _, ok := status[name]; !ok {
			t.Fatalf("%s: missing %s; cannot prove capability containment", source, name)
		}
	}
	return status
}

func assertNetAdminAbsent(t *testing.T, source string, status map[string]uint64, fields []string) {
	t.Helper()
	const netAdminBit = uint64(1) << 12
	for _, name := range fields {
		if status[name]&netAdminBit != 0 {
			t.Errorf("%s: %s = %#x, want NET_ADMIN bit 12 clear", source, name, status[name])
		}
	}
}
