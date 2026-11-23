//go:build darwin

package nodeinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// launchctlPath is launchctl; tests point it at a fake.
var launchctlPath = "/bin/launchctl"

// launchctlNotFound is launchctl print's exit status for a service its domain
// does not have. Any other failure is an error, never "not loaded".
const launchctlNotFound = 113

// launchctlInProgress is launchctl bootout's exit status (EINPROGRESS) for a
// job that got the signal and is still shutting down.
const launchctlInProgress = 36

// launchctlTimeout bounds each launchctl call on its own.
const launchctlTimeout = 30 * time.Second

// errNodeStillLoaded is a stop whose deadline passed while launchd still had
// the job, so the job is still there.
var errNodeStillLoaded = errors.New("launchd still has the node's job")

// launchctl runs one launchctl call with its own timeout, in a process group
// of its own so a terminal's Ctrl-C does not cut it short; ctx still cancels
// it. A canceled ctx is the error then.
func (n nodeInstaller) launchctl(ctx context.Context, args ...string) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, launchctlTimeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, launchctlPath, args...) //nolint:gosec // G204: launchctl on this user's own domain, job and plist.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := cmd.CombinedOutput()
	switch {
	case err == nil:
		return output, nil
	case ctx.Err() != nil:
		return output, ctx.Err()
	case callCtx.Err() != nil:
		// Not an exit status: launchctl gave no answer in time.
		return output, fmt.Errorf("launchctl %s gave no answer within %d seconds: %w", args[0], int(launchctlTimeout/time.Second), callCtx.Err())
	default:
		return output, fmt.Errorf("launchctl %s failed (%w): %s", args[0], err, strings.TrimSpace(string(output)))
	}
}

// launchdPIDLine is the running process's line in launchctl print's output.
var launchdPIDLine = regexp.MustCompile(`(?m)^\tpid = ([0-9]+)$`)

// loaded reports whether launchd has the node's job. Only launchctl's "not
// found" status means it does not; any other failure is an error.
func (n nodeInstaller) loaded(ctx context.Context) (bool, error) {
	loaded, _, err := n.status(ctx)
	return loaded, err
}

// status reports whether launchd has the node's job and, while its node runs,
// the node's pid (0 otherwise).
func (n nodeInstaller) status(ctx context.Context) (loaded bool, pid int, err error) {
	output, err := n.launchctl(ctx, "print", n.service)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		if match := launchdPIDLine.FindSubmatch(output); match != nil {
			pid, _ = strconv.Atoi(string(match[1]))
		}
		return true, pid, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == launchctlNotFound:
		return false, 0, nil
	default:
		return false, 0, err
	}
}

// nodeStopWait bounds each of stop's waits for a node that drains for drain;
// tests shorten it.
var nodeStopWait = nodeStopTimeout

// stop stops the job, if launchd has it, and waits until launchd no longer
// has it. launchd caps ExitTimeOut at a minute, so stop first sends the
// running node SIGTERM itself and waits for it to drain, which can take as
// long as its streams, and only then boots the job out. sent reports that a
// signal went out, so the job may be on its way down even when stop returns
// an error.
func (n nodeInstaller) stop(ctx context.Context, out io.Writer) (sent bool, err error) {
	loaded, pid, err := n.status(ctx)
	if err != nil || !loaded {
		return false, err
	}
	notice := "Stopping the running node.\n"
	if n.drain > 0 {
		notice = "Stopping the running node. It lets active streams finish first, which can take up to " + minutes(n.drain) + ".\n"
	}
	if _, err := io.WriteString(out, notice); err != nil {
		return false, err
	}
	killed := false
	if pid > 0 {
		if killed, err = n.drainJob(ctx, out, pid, time.Now().Add(nodeStopWait(n.drain))); err != nil {
			return false, err
		}
	}
	// The bootout is not canceled with ctx: once it starts it may have gone
	// out, and its own timeout bounds it.
	_, bootoutErr := n.launchctl(context.WithoutCancel(ctx), "bootout", n.service)
	var exitErr *exec.ExitError
	if errors.As(bootoutErr, &exitErr) && exitErr.ExitCode() != launchctlInProgress {
		// launchctl refused it: the job is gone, or it was left as it is.
		loaded, err := n.loaded(ctx)
		if err == nil && !loaded {
			return true, nil
		}
		if killed {
			// The job may stay loaded after its node drained and exited, which
			// KeepAlive does not restart: start it again even when the loaded
			// check above failed.
			_, kickErr := n.launchctl(context.WithoutCancel(ctx), "kickstart", n.service)
			err = errors.Join(err, kickErr)
		}
		return false, errors.Join(bootoutErr, err)
	}
	// Sent (the job may still be shutting down), or still waiting for an
	// answer when its timeout cut it off.
	if err := n.waitGone(ctx, time.Now().Add(nodeStopWait(n.drain))); err != nil {
		if errors.Is(err, errNodeStillLoaded) {
			err = fmt.Errorf("the running node did not stop within %s: %w", minutes(nodeStopWait(n.drain)), err)
		}
		return true, err
	}
	return true, nil
}

// drainJob sends the node running as pid SIGTERM through launchd and waits,
// until deadline, for that process to exit. A job an older plist keeps alive
// may start again under a new pid; the signaled pid's exit still ends the
// drain. killed reports that the signal went out. A launchctl error while
// polling is retried until the deadline, since bootout would cut the drain
// short. Once the signal went out, every way out (the exit, the deadline, an
// interrupt) leads to the bootout, so the job is never left stopped by a drained node alone; an
// error means ctx ended before anything was sent.
func (n nodeInstaller) drainJob(ctx context.Context, out io.Writer, pid int, deadline time.Time) (killed bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Not canceled with ctx, as the bootout: once it starts it may have gone out.
	if _, err := n.launchctl(context.WithoutCancel(ctx), "kill", "SIGTERM", n.service); err != nil {
		// No signal: the bootout sends one.
		return false, nil
	}
	ticker := time.NewTicker(nodePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-ticker.C:
		}
		loaded, running, err := n.status(ctx)
		switch {
		case err == nil && (!loaded || running != pid):
			return true, nil
		case !time.Now().Before(deadline):
			_, _ = fmt.Fprintf(out, "The node is still finishing streams after %s and is being stopped now.\n", minutes(nodeStopWait(n.drain)))
			return true, nil
		}
	}
}

// waitGone polls launchd until it no longer has the job. At deadline it
// reports errNodeStillLoaded.
func (n nodeInstaller) waitGone(ctx context.Context, deadline time.Time) error {
	ticker := time.NewTicker(nodePollInterval)
	defer ticker.Stop()
	for {
		loaded, err := n.loaded(ctx)
		switch {
		case err != nil:
			return err
		case !loaded:
			return nil
		case !time.Now().Before(deadline):
			return errNodeStillLoaded
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// minutes names d in whole minutes, rounded up.
func minutes(d time.Duration) string {
	m := (d + time.Minute - 1) / time.Minute
	if m == 1 {
		return "1 minute"
	}
	return strconv.FormatInt(int64(m), 10) + " minutes"
}

// stopRunning stops the job for an install or uninstall, and reports whether
// launchd had it. When the stop ends early after the bootout went out,
// interrupted or on a launchctl error, the job is started again from the
// plist still in place, so the node is not left stopped.
func (n nodeInstaller) stopRunning(ctx context.Context, hasPlist bool) (wasLoaded bool, err error) {
	sent, err := n.stop(ctx, n.out)
	if err == nil {
		return sent, nil
	}
	stillLoaded := errors.Is(err, errNodeStillLoaded)
	if ctx.Err() != nil {
		err = errors.New("interrupted")
	}
	if !sent || !hasPlist || stillLoaded {
		return sent, err
	}
	if ctx.Err() != nil {
		_, _ = fmt.Fprintln(n.out, "Interrupted: the previous node starts again once it has stopped. "+
			"Interrupt again to leave it stopped until the next login.")
	}
	restartCtx, cancel := context.WithTimeout(context.Background(), nodeStopTimeout(n.drain))
	defer cancel()
	restartErr := n.waitGone(restartCtx, time.Now().Add(nodeStopTimeout(n.drain)))
	if restartErr == nil {
		restartErr = n.bootstrap(restartCtx)
	}
	if restartErr != nil {
		return sent, fmt.Errorf("%w\n%s", err, "Starting the previous node again failed: "+restartErr.Error())
	}
	return sent, fmt.Errorf("%w\n%s", err, "The previous node is running again.")
}

func (n nodeInstaller) bootstrap(ctx context.Context) error {
	if _, err := n.launchctl(ctx, "bootstrap", n.domain, n.paths.plist); err != nil {
		return fmt.Errorf("%w; the node needs a logged-in user session", err)
	}
	return nil
}
