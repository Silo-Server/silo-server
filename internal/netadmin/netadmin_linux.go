//go:build linux && cgo

package netadmin

/*
#define _GNU_SOURCE
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <linux/capability.h>
#include <limits.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <signal.h>
#include <stdint.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/uio.h>
#include <sys/wait.h>
#include <unistd.h>

enum {
    SILO_NA_NONE, SILO_NA_RUNNING, SILO_NA_START_FAILED, SILO_NA_INSPECT_FAILED,
    SILO_NA_CONGESTION = 1, SILO_NA_FORCE = 2,
    SILO_NA_REQUEST_SIZE = 8, SILO_NA_MAX_FDS = 253
};

typedef struct {
    int held, helper, children_inherit, no_new_privs, threads, error;
    int fd;
    pid_t pid;
} silo_netadmin_state;

static silo_netadmin_state silo_state = {.fd = -1};

static void silo_record_error(int error) {
    if (silo_state.error == 0) silo_state.error = error;
}

static int silo_capget(struct __user_cap_data_struct data[2]) {
    struct __user_cap_header_struct header = {_LINUX_CAPABILITY_VERSION_3, 0};
    return syscall(SYS_capget, &header, data);
}

static int silo_capset(struct __user_cap_data_struct data[2]) {
    struct __user_cap_header_struct header = {_LINUX_CAPABILITY_VERSION_3, 0};
    return syscall(SYS_capset, &header, data);
}

// The Go runtime opens /dev/null on a closed fd 0, 1 or 2 only after this
// constructor runs. Do it first so the helper channel never becomes stdio.
static int silo_reserve_stdio(void) {
    for (int fd = 0; fd <= 2; fd++) {
        if (fcntl(fd, F_GETFD) >= 0) continue;
        if (errno != EBADF || open("/dev/null", O_RDWR) != fd) { silo_record_error(errno); return 0; }
    }
    return 1;
}

static void silo_count_threads(void) {
    DIR *dir = opendir("/proc/self/task");
    if (dir == NULL) { silo_record_error(errno); return; }
    struct dirent *entry;
    errno = 0;
    while ((entry = readdir(dir)) != NULL) {
        if (entry->d_name[0] >= '0' && entry->d_name[0] <= '9') silo_state.threads++;
    }
    if (errno != 0) { silo_record_error(errno); silo_state.threads = 0; }
    if (closedir(dir) < 0) silo_record_error(errno);
}

static void silo_drop_bounding_and_ambient(void) {
    int bounding = prctl(PR_CAPBSET_READ, CAP_NET_ADMIN, 0, 0, 0);
    if (bounding < 0) silo_record_error(errno);
    if (bounding == 1 && prctl(PR_CAPBSET_DROP, CAP_NET_ADMIN, 0, 0, 0) < 0 && errno != EPERM)
        silo_record_error(errno);
    if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_LOWER, CAP_NET_ADMIN, 0, 0) < 0 && errno != EINVAL)
        silo_record_error(errno);
}

static void silo_read_containment(void) {
    struct __user_cap_data_struct data[2] = {{0}};
    int read_error = 0;
    if (silo_capget(data) < 0) { silo_record_error(errno); read_error = 1; }
    int bounding = prctl(PR_CAPBSET_READ, CAP_NET_ADMIN, 0, 0, 0);
    if (bounding < 0) { silo_record_error(errno); read_error = 1; }
    int ambient = prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_IS_SET, CAP_NET_ADMIN, 0, 0);
    if (ambient < 0) {
        if (errno == EINVAL) ambient = 0;
        else { silo_record_error(errno); read_error = 1; }
    }
    int nnp = prctl(PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0);
    if (nnp < 0) { silo_record_error(errno); read_error = 1; }
    silo_state.no_new_privs = nnp == 1;
    uint32_t retained = data[0].permitted | data[0].inheritable | data[0].effective;
    silo_state.children_inherit = read_error || (retained & (1U << CAP_NET_ADMIN)) ||
        ambient == 1 || (bounding == 1 && nnp != 1);
}

static uint32_t silo_read_u32(const unsigned char *bytes) {
    return (uint32_t)bytes[0] | (uint32_t)bytes[1] << 8 |
        (uint32_t)bytes[2] << 16 | (uint32_t)bytes[3] << 24;
}

static int silo_reply(int fd, int error) {
    unsigned char reply[4];
    for (int i = 0; i < 4; i++) reply[i] = (uint32_t)error >> (8 * i);
    ssize_t n;
    do { n = send(fd, reply, sizeof(reply), MSG_NOSIGNAL); } while (n < 0 && errno == EINTR);
    return n == sizeof(reply) ? 0 : -1;
}

static int silo_tcp_socket(int fd) {
    int domain, type, protocol;
    socklen_t length = sizeof(int);
    if (getsockopt(fd, SOL_SOCKET, SO_DOMAIN, &domain, &length) < 0) return 0;
    length = sizeof(int);
    if (getsockopt(fd, SOL_SOCKET, SO_TYPE, &type, &length) < 0) return 0;
    length = sizeof(int);
    if (getsockopt(fd, SOL_SOCKET, SO_PROTOCOL, &protocol, &length) < 0) return 0;
    return (domain == AF_INET || domain == AF_INET6) && type == SOCK_STREAM && protocol == IPPROTO_TCP;
}

static int silo_apply_option(const unsigned char request[SILO_NA_REQUEST_SIZE], int fd) {
    if (!silo_tcp_socket(fd)) return EINVAL;
    uint32_t operation = silo_read_u32(request);
    uint32_t size = silo_read_u32(request + 4);
    int result;
    if (operation == SILO_NA_CONGESTION && size == 0) {
        result = setsockopt(fd, IPPROTO_TCP, TCP_CONGESTION, "bbr", 3);
    } else if (operation == SILO_NA_FORCE && size > 0 && size <= INT_MAX / 2) {
        int bytes = (int)size;
        result = setsockopt(fd, SOL_SOCKET, SO_SNDBUFFORCE, &bytes, sizeof(bytes));
    } else {
        return EINVAL;
    }
    return result < 0 ? errno : 0;
}

static void silo_serve(int fd) {
    for (;;) {
        unsigned char request[SILO_NA_REQUEST_SIZE] = {0};
        union {
            struct cmsghdr alignment;
            unsigned char bytes[CMSG_SPACE(SILO_NA_MAX_FDS * sizeof(int))];
        } control;
        struct iovec iov = {request, sizeof(request)};
        struct msghdr message = {0};
        message.msg_iov = &iov;
        message.msg_iovlen = 1;
        message.msg_control = control.bytes;
        message.msg_controllen = sizeof(control.bytes);
        ssize_t n;
        do { n = recvmsg(fd, &message, MSG_CMSG_CLOEXEC); } while (n < 0 && errno == EINTR);
        if (n < 0) return;

        int fds[SILO_NA_MAX_FDS], count = 0, invalid = 0;
        for (struct cmsghdr *cmsg = CMSG_FIRSTHDR(&message); cmsg != NULL; cmsg = CMSG_NXTHDR(&message, cmsg)) {
            if (cmsg->cmsg_level != SOL_SOCKET || cmsg->cmsg_type != SCM_RIGHTS || cmsg->cmsg_len < CMSG_LEN(0)) {
                invalid = 1;
                continue;
            }
            size_t size = cmsg->cmsg_len - CMSG_LEN(0);
            if (size % sizeof(int) != 0) invalid = 1;
            for (size_t offset = 0; offset + sizeof(int) <= size; offset += sizeof(int)) {
                int received;
                memcpy(&received, CMSG_DATA(cmsg) + offset, sizeof(received));
                if (count < SILO_NA_MAX_FDS) fds[count++] = received;
                else { close(received); invalid = 1; }
            }
        }
        // A zero-length seqpacket is also an invalid request, not necessarily EOF.
        if (n == 0 && count == 0) {
            struct pollfd peer = {fd, POLLRDHUP, 0};
            int ready;
            do { ready = poll(&peer, 1, 0); } while (ready < 0 && errno == EINTR);
            if (ready < 0 || (peer.revents & (POLLHUP | POLLRDHUP))) return;
        }
        int valid = !invalid && n == sizeof(request) && !(message.msg_flags & (MSG_TRUNC | MSG_CTRUNC));
        int error = EINVAL;
        if (valid && count == 1) error = silo_apply_option(request, fds[0]);
        // Every installed descriptor is closed before the reply, even on refusal.
        for (int i = 0; i < count; i++) close(fds[i]);
        if (silo_reply(fd, error) < 0) return;
    }
}

static void silo_helper(int fd, pid_t parent) {
    int error = 0;
    if (prctl(PR_SET_PDEATHSIG, SIGKILL, 0, 0, 0) < 0) error = errno;
    if (getppid() != parent) _exit(0);
    struct __user_cap_data_struct data[2] = {{0}};
    data[0].effective = data[0].permitted = 1U << CAP_NET_ADMIN;
    if (error == 0 && silo_capset(data) < 0) error = errno;
    if (error == 0 && prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) error = errno;
    // The parent reports running only after the helper restricts its privileges.
    if (silo_reply(fd, error) == 0 && error == 0) silo_serve(fd);
    close(fd);
    _exit(0);
}

static void silo_start_helper(void) {
    int pair[2];
    if (socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, pair) < 0) {
        silo_state.helper = SILO_NA_START_FAILED;
        silo_record_error(errno);
        return;
    }
    pid_t parent = getpid();
    pid_t pid = fork();
    if (pid == 0) { close(pair[0]); silo_helper(pair[1], parent); }
    int error = errno;
    close(pair[1]);
    if (pid < 0) {
        close(pair[0]);
        silo_state.helper = SILO_NA_START_FAILED;
        silo_record_error(error);
        return;
    }
    unsigned char reply[4];
    ssize_t n;
    do { n = recv(pair[0], reply, sizeof(reply), MSG_TRUNC); } while (n < 0 && errno == EINTR);
    error = n < 0 ? errno : n != sizeof(reply) ? EPROTO : (int)silo_read_u32(reply);
    if (error != 0) {
        close(pair[0]);
        while (waitpid(pid, NULL, 0) < 0 && errno == EINTR) {}
        silo_state.helper = SILO_NA_START_FAILED;
        silo_record_error(error);
        return;
    }
    silo_state.fd = pair[0];
    silo_state.pid = pid;
    silo_state.helper = SILO_NA_RUNNING;
}

__attribute__((constructor)) static void silo_netadmin_init(void) {
    int stdio_reserved = silo_reserve_stdio();
    silo_count_threads();
    struct __user_cap_data_struct data[2] = {{0}};
    if (silo_capget(data) < 0) {
        silo_record_error(errno);
        silo_state.helper = SILO_NA_INSPECT_FAILED;
        silo_state.children_inherit = 1;
        silo_drop_bounding_and_ambient();
        // Unknown sets cannot be rewritten; still stop children gaining privileges.
        if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) silo_record_error(errno);
        int nnp = prctl(PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0);
        if (nnp < 0) silo_record_error(errno);
        silo_state.no_new_privs = nnp == 1;
        return;
    }
    int bounding = prctl(PR_CAPBSET_READ, CAP_NET_ADMIN, 0, 0, 0);
    if (bounding < 0) silo_record_error(errno);
    int ambient = prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_IS_SET, CAP_NET_ADMIN, 0, 0);
    if (ambient < 0 && errno == EINVAL) ambient = 0;
    else if (ambient < 0) silo_record_error(errno);
    uint32_t retained = data[0].effective | data[0].permitted | data[0].inheritable;
    if (!(retained & (1U << CAP_NET_ADMIN)) && bounding == 0 && ambient == 0) {
        silo_read_containment();
        return;
    }
    if (data[0].effective & (1U << CAP_NET_ADMIN)) {
        silo_state.held = 1;
        if (stdio_reserved) silo_start_helper();
        else silo_state.helper = SILO_NA_START_FAILED;
    }
    silo_drop_bounding_and_ambient();
    data[0].effective &= ~(1U << CAP_NET_ADMIN);
    data[0].permitted &= ~(1U << CAP_NET_ADMIN);
    data[0].inheritable &= ~(1U << CAP_NET_ADMIN);
    if (silo_capset(data) < 0) silo_record_error(errno);
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) silo_record_error(errno);
    silo_read_containment();
}

static silo_netadmin_state silo_netadmin_snapshot(void) { return silo_state; }
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"golang.org/x/sys/unix"
)

var helper = struct {
	sync.Mutex
	state State
	fd    int
	pid   int
}{fd: -1}

func init() {
	snapshot := C.silo_netadmin_snapshot()
	helper.state = State{
		HeldAtStart:     snapshot.held != 0,
		ChildrenInherit: snapshot.children_inherit != 0,
		NoNewPrivs:      snapshot.no_new_privs != 0,
		Threads:         int(snapshot.threads),
	}
	switch snapshot.helper {
	case C.SILO_NA_RUNNING:
		helper.state.Helper = HelperRunning
	case C.SILO_NA_START_FAILED:
		helper.state.Helper = HelperStartFailed
	case C.SILO_NA_INSPECT_FAILED:
		helper.state.Helper = HelperInspectFailed
	default:
		helper.state.Helper = HelperNone
	}
	if snapshot.error > 0 {
		helper.state.Errno = unix.ErrnoName(unix.Errno(snapshot.error))
	}
	helper.fd = int(snapshot.fd)
	helper.pid = int(snapshot.pid)
}

// Current returns the process's fixed startup state, except that Helper becomes
// HelperReleased when a running helper is released.
func Current() State {
	helper.Lock()
	defer helper.Unlock()
	return helper.state
}

// SetCongestion asks the helper to set TCP_CONGESTION to bbr on fd.
// It returns a kernel errno on refusal, or an error when the helper is
// unavailable or communication fails.
func SetCongestion(fd int) error {
	return socketOption(fd, C.SILO_NA_CONGESTION, 0)
}

// ForceSendBuffer asks the helper to set SO_SNDBUFFORCE to bytes on fd without
// a readback. Bytes must be in [1, 1073741823], or it returns EINVAL. Other
// errors follow SetCongestion.
func ForceSendBuffer(fd, bytes int) error {
	if bytes <= 0 || bytes > math.MaxInt32/2 {
		return unix.EINVAL
	}
	return socketOption(fd, C.SILO_NA_FORCE, uint32(bytes))
}

func socketOption(fd int, operation, size uint32) error {
	helper.Lock()
	defer helper.Unlock()
	if helper.state.Helper != HelperRunning {
		return ErrUnavailable
	}
	var request [C.SILO_NA_REQUEST_SIZE]byte
	binary.LittleEndian.PutUint32(request[:], operation)
	binary.LittleEndian.PutUint32(request[4:], size)
	err := unix.Sendmsg(helper.fd, request[:], unix.UnixRights(fd), nil, unix.MSG_NOSIGNAL)
	if err != nil {
		releaseLocked()
		return fmt.Errorf("%w: send request: %w", ErrUnavailable, err)
	}
	err = receiveReply()
	if err != nil {
		// Only a bare errno is a valid refusal reply; transport failures end the helper.
		if _, refused := err.(unix.Errno); !refused { //nolint:errorlint // Wrapped transport errnos are not helper refusal replies.
			releaseLocked()
		}
	}
	return err
}

func receiveReply() error {
	var reply [4]byte
	n, _, flags, _, err := unix.Recvmsg(helper.fd, reply[:], nil, 0)
	if err != nil {
		return fmt.Errorf("receive reply: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("receive reply: %w", io.EOF)
	}
	if n != len(reply) || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return fmt.Errorf("malformed reply: %w", unix.EPROTO)
	}
	errno := binary.LittleEndian.Uint32(reply[:])
	if errno > 4095 {
		return fmt.Errorf("invalid errno: %w", unix.EPROTO)
	}
	if errno != 0 {
		return unix.Errno(errno)
	}
	return nil
}

// Release idempotently closes the helper channel and reaps a running helper.
// Later socket operations return ErrUnavailable.
func Release() {
	helper.Lock()
	defer helper.Unlock()
	releaseLocked()
}

func releaseLocked() {
	if helper.state.Helper != HelperRunning {
		return
	}
	_ = unix.Close(helper.fd)
	helper.fd = -1
	var err error
	for {
		_, err = unix.Wait4(helper.pid, nil, 0, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	helper.state.Helper = HelperReleased
}
