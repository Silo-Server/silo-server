//go:build darwin

package nodeinstall

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/nodebuild"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// nodeServerReadyTimeout bounds the server's readiness probe before an install.
const nodeServerReadyTimeout = 5 * time.Second

// nodeReadyTimeout bounds the wait for a new node's health answer. The node
// opens its port only once the server took its key.
const nodeReadyTimeout = 60 * time.Second

// nodePollInterval is how often the installer looks at launchd and health.
const nodePollInterval = 500 * time.Millisecond

// nodeImageCopyHint is how to get the node binary that matches the server.
const nodeImageCopyHint = "docker compose cp silo:/usr/share/silo/nodes/silo-darwin-arm64 ./silo"

// nodeInstaller installs, upgrades and removes the per-user LaunchAgent.
type nodeInstaller struct {
	paths   nodeInstallPaths
	service string // gui/<uid>/org.siloserver.node
	domain  string // gui/<uid>
	out     io.Writer
	// drain is how long the running node may drain on SIGTERM, which bounds
	// every wait for it to stop: the longer of node.env's drain and the one its
	// plist recorded when it was installed, since the node read node.env when
	// it started and the file may have changed since.
	drain time.Duration
	// newDrain is the drain the node.env being installed sets, which the new
	// plist records.
	newDrain time.Duration
}

func newNodeInstaller(out io.Writer) (nodeInstaller, error) {
	if os.Geteuid() == 0 {
		return nodeInstaller{}, errors.New("run this as your own user, without sudo: the node is a LaunchAgent for that user")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nodeInstaller{}, err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	return nodeInstaller{paths: nodeInstallPathsIn(home), domain: domain, service: domain + "/" + nodeLaunchdLabel, out: out,
		drain: transcodenode.DefaultDrainTimeout}, nil
}

// nodeSignalContext ends at SIGINT or SIGTERM. After the first signal the
// default handling is back, so a second one ends the process at once.
func nodeSignalContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, stop)
	return ctx, stop
}

func installNode(args []string, in io.Reader, out io.Writer) error {
	given, help, err := parseNodeInstallFlags(args)
	if err != nil {
		return err
	}
	if help {
		_, err := fmt.Fprintln(out, nodeInstallUsage)
		return err
	}
	if !nodebuild.Node {
		return errors.New("this silo build links Homebrew libraries, so a Homebrew upgrade can stop it from starting. " +
			"Install the node binary from the server image instead:\n  " + nodeImageCopyHint + "\n  ./silo node install --server <URL>")
	}
	n, err := newNodeInstaller(out)
	if err != nil {
		return err
	}
	// Everything is checked, and the key asked for once, before anything is
	// written or locked, so a refused run leaves nothing behind.
	stored, extra, err := n.stored()
	if err != nil {
		return err
	}
	if given.key, err = askNodeKey(stored, given, func() (string, error) { return promptNodeKey(in, out) }); err != nil {
		return err
	}
	settings, err := resolveNodeInstall(stored, given)
	if err != nil {
		return err
	}
	if _, err := nodeEnvDrainTimeout(extra); err != nil {
		return err
	}
	if !playback.HomebrewFFmpegFull(playback.ResolveFFmpegPath("")) {
		return errors.New("ffmpeg-full from Homebrew is not installed: run `brew install ffmpeg-full`, then run this again")
	}
	ctx, stopSignals := nodeSignalContext()
	defer stopSignals()
	if err := checkServerReady(ctx, settings.server); err != nil {
		return err
	}
	unlock, err := lockNodeInstall(n.paths.lock)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.MkdirAll(n.paths.dir, 0o700); err != nil {
		return err
	}
	return n.installLocked(ctx, given, settings.server)
}

// askNodeKey returns the key to give the install: none when the install has one
// to keep, or the one entered at the prompt (or piped in on standard input)
// when --new-key asks for it or the install has none.
func askNodeKey(stored, given nodeInstallSettings, prompt func() (string, error)) (string, error) {
	asked := given.key == nodeKeyPrompt
	merged := mergeNodeInstall(stored, given)
	if !asked && merged.key != "" {
		return given.key, nil
	}
	if err := merged.validateAllButKey(); err != nil {
		return "", err
	}
	key, err := prompt()
	if err == nil && asked && key == "" {
		err = errors.New("--new-key needs the key: enter it at the prompt, or pipe it in on standard input")
	}
	return key, err
}

// installLocked reads the install again under the lock, since another run may
// have changed it, and installs.
func (n nodeInstaller) installLocked(ctx context.Context, given nodeInstallSettings, checkedServer string) error {
	stored, extra, err := n.stored()
	if err != nil {
		return err
	}
	settings, err := resolveNodeInstall(stored, given)
	if err != nil {
		return err
	}
	if err := n.dropStaleBackups(); err != nil {
		return err
	}
	// The new node.env keeps the drain, so it sets the new plist's ExitTimeOut
	// and, with the drain the running node's plist recorded, bounds its stop.
	if n.newDrain, err = nodeEnvDrainTimeout(extra); err != nil {
		return err
	}
	n.drain = max(n.newDrain, n.installedPlistDrain())
	if settings.server != checkedServer {
		if err := checkServerReady(ctx, settings.server); err != nil {
			return err
		}
	}
	loaded, err := n.loaded(ctx)
	if err != nil {
		return err
	}
	if ours := loaded && stored.port == settings.port; !ours && nodePortTaken(ctx, settings.port) {
		return fmt.Errorf("another program listens on port %d; choose a free one with --port", settings.port)
	}
	return n.install(ctx, settings, stored.port, extra)
}

// resolveNodeInstall lays the flags given over the stored install and
// validates the result.
func resolveNodeInstall(stored, given nodeInstallSettings) (nodeInstallSettings, error) {
	settings := mergeNodeInstall(stored, given)
	return settings, settings.validate()
}

// validate refuses settings the node would refuse at startup.
func (s nodeInstallSettings) validate() error {
	if err := s.validateAllButKey(); err != nil {
		return err
	}
	if s.key == "" {
		return errors.New("the node key is required the first time: enter the key Admin > Nodes issued at the prompt, or pipe it in on standard input")
	}
	if _, err := nodepool.ParseNodeKey(s.key); err != nil {
		return fmt.Errorf("the node key is malformed (%w); copy the key Admin > Nodes issued", err)
	}
	return nil
}

func (s nodeInstallSettings) validateAllButKey() error {
	if s.server == "" {
		return errors.New("--server is required the first time: the Silo server's URL as this Mac reaches it, such as http://localhost:8090")
	}
	parsed, ok := transcodenode.ParseCoreURL(s.server)
	if !ok {
		return errors.New("--server must be an absolute http or https URL")
	}
	// The readiness probe and the node join paths onto it, which a query or
	// fragment would swallow, and userinfo would go out as Basic auth.
	if parsed.User != nil || strings.ContainsAny(s.server, "?#") {
		return errors.New("--server must be a plain http or https URL such as http://localhost:8090")
	}
	if _, err := quoteNodeEnv(s.server); err != nil {
		return fmt.Errorf("--server: %w", err)
	}
	if s.port < 1 || s.port > 65535 {
		return errors.New("--port must be between 1 and 65535")
	}
	return nil
}

// checkServerReady asks the server's readiness probe before anything
// changes, so a run while the server restarts or migrates stops here.
func checkServerReady(ctx context.Context, server string) error {
	ctx, cancel := context.WithTimeout(ctx, nodeServerReadyTimeout)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	reason := ""
	readyURL, err := nodeServerReadyURL(server)
	var req *http.Request
	if err == nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
	}
	if err == nil {
		var resp *http.Response
		if resp, err = client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			reason = "its readiness check answered " + resp.Status
		}
	}
	var urlErr *url.Error
	switch {
	case reason != "":
	case errors.Is(err, context.Canceled):
		return errors.New("interrupted")
	case errors.Is(err, context.DeadlineExceeded):
		reason = fmt.Sprintf("no answer within %d seconds", int(nodeServerReadyTimeout/time.Second))
	case errors.As(err, &urlErr):
		reason = urlErr.Err.Error()
	default:
		reason = err.Error()
	}
	return fmt.Errorf("the server at %s isn't ready yet (%s); run this again once it is", server, reason)
}

// nodeServerReadyURL is the readiness probe under server, a URL validate
// accepted, with any path it has kept as a prefix.
func nodeServerReadyURL(server string) (string, error) {
	parsed, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	return parsed.JoinPath("api", "v1", "ready").String(), nil
}

// stored reads the existing install's settings, all zero without one, and the
// other variables its node.env carries. A run stopped mid-swap can leave a
// file only at its backup, <file>.old, which is then the one read.
func (n nodeInstaller) stored() (nodeInstallSettings, map[string]string, error) {
	env, err := readInstalledFile(n.paths.env)
	if errors.Is(err, fs.ErrNotExist) {
		return nodeInstallSettings{}, nil, nil
	}
	if err != nil {
		return nodeInstallSettings{}, nil, err
	}
	key, err := readInstalledFile(n.paths.key)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nodeInstallSettings{}, nil, err
	}
	return storedNodeInstall(env, key)
}

// readInstalledFile reads path, or its backup when path is missing.
// nodeStopTimeout bounds the installer's wait for a node that drains for
// drain to exit: launchd's ExitTimeOut and a minute more.
func nodeStopTimeout(drain time.Duration) time.Duration {
	return addCapped(drain, 2*time.Minute)
}

// plistExitTimeOut finds the ExitTimeOut renderNodePlist writes.
var plistExitTimeOut = regexp.MustCompile(`<key>ExitTimeOut</key>\s*<integer>([0-9]+)</integer>`)

// installedPlistDrain is the drain the installed plist recorded: its
// ExitTimeOut less the minute nodeExitTimeout adds. 0 without a plist or one
// it can't read.
func (n nodeInstaller) installedPlistDrain() time.Duration {
	plist, err := os.ReadFile(n.paths.plist)
	if err != nil {
		return 0
	}
	match := plistExitTimeOut.FindSubmatch(plist)
	if match == nil {
		return 0
	}
	seconds, err := strconv.Atoi(string(match[1]))
	if err != nil || seconds <= 60 {
		return 0
	}
	return time.Duration(seconds-60) * time.Second
}

// swapPending is the marker an install writes before it moves files aside and
// removes once the new node is ready. With the marker, a .old file is the last
// working install; without it, a leftover of a run that finished.
func (p nodeInstallPaths) swapPending() string { return filepath.Join(p.dir, ".swap-pending") }

// installedFiles are the files a swap replaces.
func (p nodeInstallPaths) installedFiles() []string { return []string{p.binary, p.env, p.key} }

// swapCheckpoint records the LaunchAgent state before a swap begins, inside
// .swap-pending, so a retry after an interrupted swap rolls back to the
// original plist and loaded state rather than the interrupted run's.
type swapCheckpoint struct {
	HasPlist bool   `json:"has_plist"`
	Loaded   bool   `json:"loaded"`
	Plist    []byte `json:"plist,omitempty"`
}

func (n nodeInstaller) readSwapCheckpoint() (swapCheckpoint, bool, error) {
	data, err := os.ReadFile(n.paths.swapPending()) //nolint:gosec // G304: the installer's own swap marker.
	if errors.Is(err, fs.ErrNotExist) {
		return swapCheckpoint{}, false, nil
	}
	if err != nil {
		return swapCheckpoint{}, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return swapCheckpoint{}, false, nil
	}
	var cp swapCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return swapCheckpoint{}, false, fmt.Errorf("read swap marker %s: %w", n.paths.swapPending(), err)
	}
	return cp, true, nil
}

func (n nodeInstaller) writeSwapCheckpoint(oldPlist []byte, oldLoaded bool) error {
	data, err := json.Marshal(swapCheckpoint{
		HasPlist: oldPlist != nil,
		Loaded:   oldLoaded,
		Plist:    oldPlist,
	})
	if err != nil {
		return err
	}
	tmp := n.paths.swapPending() + ".tmp"
	if err := writeNodeFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, n.paths.swapPending()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// dropStaleBackups removes the .old files a finished run left behind, so a
// later rollback can't restore an older install than the one that ran. Any
// stat error other than missing stops the run instead of deleting backups.
func (n nodeInstaller) dropStaleBackups() error {
	_, err := os.Stat(n.paths.swapPending())
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check swap marker %s: %w", n.paths.swapPending(), err)
	}
	for _, path := range n.paths.installedFiles() {
		_ = os.Remove(path + ".old")
	}
	return nil
}

// commitSwap clears the swap marker and only then deletes the .old backups. If
// clearing the marker fails, the backups stay in place and the error is
// returned so the next run does not mistake partial cleanup for a pending swap.
func (n nodeInstaller) commitSwap() error {
	if err := os.Remove(n.paths.swapPending()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the new node is running, but clearing %s failed (%w); backups were kept", n.paths.swapPending(), err)
	}
	for _, path := range n.paths.installedFiles() {
		_ = os.Remove(path + ".old")
	}
	return nil
}

func readInstalledFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the installer's own files.
	if errors.Is(err, fs.ErrNotExist) {
		data, err = os.ReadFile(nodeInstallSwap{path: path}.old())
	}
	return data, err
}

// install stages the new binary and settings beside the old ones, stops the
// old job, swaps the files in and starts the new job. When the new job does
// not start or become ready, or the run is interrupted, the previous files
// and job come back.
func (n nodeInstaller) install(ctx context.Context, settings nodeInstallSettings, oldPort int, extra map[string]string) error {
	env, err := renderNodeEnv(settings, n.paths.key, extra)
	if err != nil {
		return err
	}
	for _, file := range []string{n.paths.log, n.paths.plist} {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
	}
	swaps, err := n.stage(env, settings.key)
	if err != nil {
		discardStaged(swaps)
		return err
	}
	cp, hasCheckpoint, err := n.readSwapCheckpoint()
	if err != nil {
		discardStaged(swaps)
		return err
	}
	currentPlist, err := os.ReadFile(n.paths.plist)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		discardStaged(swaps)
		return err
	}
	currentLoaded, err := n.stopRunning(ctx, currentPlist != nil)
	if err != nil {
		discardStaged(swaps)
		return err
	}
	oldPlist, oldLoaded := currentPlist, currentLoaded
	if hasCheckpoint {
		if cp.HasPlist {
			oldPlist = cp.Plist
		} else {
			oldPlist = nil
		}
		oldLoaded = cp.Loaded
	}
	logStart := fileSize(n.paths.log)
	if !hasCheckpoint {
		err = n.writeSwapCheckpoint(oldPlist, oldLoaded)
	}
	if err == nil {
		err = swapIn(swaps)
	}
	if err == nil {
		err = writeNodeFile(n.paths.plist, renderNodePlist(n.paths, n.newDrain), 0o644)
	}
	if err == nil {
		err = n.bootstrap(ctx)
	}
	drained := false
	if err == nil {
		drained, err = waitNodeReady(ctx, settings)
	}
	if err != nil {
		return n.failInstall(ctx, err, swaps, oldPlist, oldLoaded, logStart)
	}
	if err := n.commitSwap(); err != nil {
		return err
	}
	return n.reportInstalled(settings, oldPort, drained)
}

// failInstall rolls back an install whose new job did not come up, with a
// context of its own so an interrupted run still puts the previous node back.
// oldLoaded reports whether launchd had the previous job before the install.
func (n nodeInstaller) failInstall(ctx context.Context, err error, swaps []nodeInstallSwap, oldPlist []byte, oldLoaded bool, logStart int64) error {
	if ctx.Err() != nil {
		err = errors.New("interrupted")
		_, _ = fmt.Fprintln(n.out, "Interrupted: putting the previous node back. Interrupt again to stop at once.")
	}
	tail := nodeLogTail(n.paths.log, logStart)
	restoreCtx, cancel := context.WithTimeout(context.Background(), nodeStopTimeout(n.drain))
	defer cancel()
	restored := "The previous node was restored."
	if rollbackErr := n.rollback(restoreCtx, swaps, oldPlist, oldLoaded); rollbackErr != nil {
		restored = "Restoring the previous node failed: " + rollbackErr.Error()
	} else if oldPlist == nil {
		restored = "Nothing was installed."
	}
	return fmt.Errorf("%w\n%s\nLast lines of %s:\n%s", err, restored, n.paths.log, tail)
}

func (n nodeInstaller) reportInstalled(settings nodeInstallSettings, oldPort int, drained bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Installed the Silo transcode node (build %s).\nLog: %s\n", nodeVersion(buildinfo.Current()), n.paths.log)
	if drained {
		b.WriteString("The node is drained: an administrator resumes it in Admin > Nodes.\n")
	}
	if oldPort != 0 && oldPort != settings.port {
		fmt.Fprintf(&b, "The port changed from %d to %d, so the node's URL in Admin > Nodes must change to port %d, "+
			"such as http://host.docker.internal:%d for a server in Docker Desktop on this Mac.\n", oldPort, settings.port, settings.port, settings.port)
	} else {
		fmt.Fprintf(&b, "In Admin > Nodes, the node's URL must be one the server can reach. "+
			"For a server in Docker Desktop on this Mac, use http://host.docker.internal:%d.\n", settings.port)
	}
	_, err := io.WriteString(n.out, b.String())
	return err
}

// nodeInstallSwap is one installed file: its staged replacement and the
// previous file, kept until the new node is ready.
type nodeInstallSwap struct {
	path   string
	hadOld bool // the previous file is at old()
	placed bool // the staged file is at path
}

func (s nodeInstallSwap) staged() string { return s.path + ".new" }
func (s nodeInstallSwap) old() string    { return s.path + ".old" }

// stage writes the new files beside the installed ones. The binary is copied
// only when this is not already the installed binary.
func (n nodeInstaller) stage(env []byte, key string) ([]nodeInstallSwap, error) {
	var swaps []nodeInstallSwap
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if !sameFile(exe, n.paths.binary) {
		swaps = append(swaps, nodeInstallSwap{path: n.paths.binary})
		if err := copyFile(exe, swaps[0].staged(), 0o755); err != nil {
			return swaps, err
		}
	}
	for _, file := range []struct {
		path string
		data []byte
	}{{n.paths.env, env}, {n.paths.key, []byte(key + "\n")}} {
		swaps = append(swaps, nodeInstallSwap{path: file.path})
		if err := writeNodeFile(swaps[len(swaps)-1].staged(), file.data, 0o600); err != nil {
			return swaps, err
		}
	}
	return swaps, nil
}

// swapIn moves each previous file aside and its staged file into place,
// recording how far it got for a rollback.
func swapIn(swaps []nodeInstallSwap) error {
	for i := range swaps {
		s := &swaps[i]
		var err error
		if s.hadOld, err = s.moveAside(); err != nil {
			return err
		}
		if err := os.Rename(s.staged(), s.path); err != nil {
			return err
		}
		s.placed = true
	}
	return nil
}

// moveAside moves the previous file to old() and reports whether old() holds
// one. A file already at old() is the last working one, left by a run that
// stopped mid-swap, so it stays the backup and the unverified file at path is
// dropped.
func (s nodeInstallSwap) moveAside() (hadOld bool, err error) {
	if _, err := os.Lstat(s.old()); err == nil {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return true, err
		}
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	err = os.Rename(s.path, s.old())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func discardStaged(swaps []nodeInstallSwap) {
	for _, s := range swaps {
		_ = os.Remove(s.staged())
	}
}

// rollback stops the new job, puts the previous files back, and starts the
// previous job again when launchd had it (oldLoaded); without one, it removes
// what the install added. Until the new job is known to be gone it touches no
// file, so a node that may still run keeps its files, and the backups stay.
func (n nodeInstaller) rollback(ctx context.Context, swaps []nodeInstallSwap, oldPlist []byte, oldLoaded bool) error {
	if _, err := n.stop(ctx, io.Discard); err != nil {
		var backups []string
		seen := make(map[string]bool, len(swaps))
		addBackup := func(old string) {
			if !seen[old] {
				seen[old] = true
				backups = append(backups, old)
			}
		}
		for _, s := range swaps {
			if s.hadOld {
				addBackup(s.old())
			}
		}
		for _, path := range n.paths.installedFiles() {
			old := path + ".old"
			if _, statErr := os.Lstat(old); statErr == nil {
				addBackup(old)
			}
		}
		kept := "no previous files were replaced"
		if len(backups) > 0 {
			kept = "the previous files are kept at " + strings.Join(backups, ", ")
		}
		return fmt.Errorf("recovery is incomplete: the new node could not be confirmed stopped (%w), so nothing was restored and %s", err, kept)
	}
	var errs []error
	restored := make(map[string]bool, len(swaps))
	for _, s := range swaps {
		if !s.placed {
			_ = os.Remove(s.staged())
		}
		switch {
		case s.hadOld:
			errs = append(errs, os.Rename(s.old(), s.path))
			restored[s.path] = true
		case s.placed:
			errs = append(errs, os.Remove(s.path))
		}
	}
	// An interrupted swap can leave a working backup (such as silo.old) for a
	// path this run did not stage or did not reach before failing. Restore it
	// before clearing the swap marker so a later run cannot drop it.
	for _, path := range n.paths.installedFiles() {
		if restored[path] {
			continue
		}
		old := path + ".old"
		if _, err := os.Lstat(old); err == nil {
			errs = append(errs, os.Rename(old, path))
		} else if !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if oldPlist == nil {
		if err := os.Remove(n.paths.plist); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	} else {
		errs = append(errs, writeNodeFile(n.paths.plist, oldPlist, 0o644))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	// The previous install is whole again, so the swap is over.
	markerErr := os.Remove(n.paths.swapPending())
	if errors.Is(markerErr, fs.ErrNotExist) {
		markerErr = nil
	}
	if oldPlist == nil || !oldLoaded {
		return markerErr
	}
	return errors.Join(markerErr, n.bootstrap(ctx))
}

func uninstallNode(out io.Writer) error {
	n, err := newNodeInstaller(out)
	if err != nil {
		return err
	}
	ctx, stopSignals := nodeSignalContext()
	defer stopSignals()
	unlock, err := lockNodeInstall(n.paths.lock)
	if err != nil {
		return err
	}
	defer unlock()
	// The node takes the default for a drain it cannot read, and so does its
	// stop.
	if _, extra, err := n.stored(); err == nil {
		if drain, err := nodeEnvDrainTimeout(extra); err == nil {
			n.drain = drain
		}
	}
	n.drain = max(n.drain, n.installedPlistDrain())
	_, dirErr := os.Stat(n.paths.dir)
	_, plistErr := os.Stat(n.paths.plist)
	loaded, err := n.loaded(ctx)
	if err != nil {
		return err
	}
	if errors.Is(dirErr, fs.ErrNotExist) && errors.Is(plistErr, fs.ErrNotExist) && !loaded {
		_, err := fmt.Fprintln(out, "No Silo transcode node is installed.")
		return err
	}
	if _, err := n.stopRunning(ctx, plistErr == nil); err != nil {
		return err
	}
	if err := os.Remove(n.paths.plist); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(n.paths.dir); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Removed the Silo transcode node. Its log stays at %s.\n", n.paths.log)
	return err
}

// waitNodeReady polls the new node's health until it answers 200 as a
// transcode node (node_control_version set) whose key the server took.
// drained reports a node an administrator drained, which stays drained
// across restarts and is ready all the same.
func waitNodeReady(ctx context.Context, settings nodeInstallSettings) (drained bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, nodeReadyTimeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	healthURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.port)) + "/api/v1/health"
	ticker := time.NewTicker(nodePollInterval)
	defer ticker.Stop()
	var last string
	for {
		health, err := fetchNodeHealth(ctx, client, healthURL)
		switch {
		case err != nil:
			last = err.Error()
		case health.NodeControlVersion < 1:
			last = "something other than a Silo transcode node answers on the port"
		case health.KeyRejected:
			return false, fmt.Errorf("the server at %s refused this node's key. Check that it is the key Admin > Nodes on that server last issued "+
				"for this node, and that this Mac's clock is within 30 seconds of the server's", settings.server)
		default:
			return health.Status == transcodenode.HealthStatusDraining || health.Status == transcodenode.HealthStatusDrained, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return false, ctx.Err()
			}
			return false, fmt.Errorf("the node was not ready within %d seconds (%s). It opens its port only once the server accepts its key, "+
				"so check the key, that this Mac reaches the server at %s, and that this Mac's clock is right",
				int(nodeReadyTimeout/time.Second), last, settings.server)
		case <-ticker.C:
		}
	}
}

func fetchNodeHealth(ctx context.Context, client *http.Client, healthURL string) (transcodenode.HealthResponse, error) {
	var health transcodenode.HealthResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return health, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return health, errors.New("no answer on the node's port")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return health, fmt.Errorf("health answered %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err != nil {
		return health, errors.New("health answer unreadable")
	}
	return health, nil
}

// nodePortTaken reports whether something answers on port at this Mac's
// loopback, which a port Docker Desktop publishes does. It connects rather
// than binds, since a bind on every interface can raise the firewall's prompt.
func nodePortTaken(ctx context.Context, port int) bool {
	dialer := net.Dialer{Timeout: time.Second}
	for _, host := range []string{"127.0.0.1", "::1"} {
		if conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port))); err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// promptNodeKey asks for the key, without echoing it, when stdin is a
// terminal; otherwise it reads the key from stdin's first line. An
// interrupt at the prompt restores the terminal before the process ends.
func promptNodeKey(in io.Reader, out io.Writer) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		if _, err := fmt.Fprintln(out, "Reading the node key from standard input."); err != nil {
			return "", err
		}
		return readNodeKeyLine(in)
	}
	fd := int(file.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	done := make(chan struct{})
	defer close(done)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			_ = term.Restore(fd, state)
			_, _ = fmt.Fprintln(out)
			os.Exit(130)
		case <-done:
		}
	}()
	if _, err := fmt.Fprint(out, "Node key from Admin > Nodes (not shown): "); err != nil {
		return "", err
	}
	key, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(out)
	return strings.TrimSpace(string(key)), err
}

// readNodeKeyLine reads a piped key: the first line of in, trimmed. A key is
// short, so it reads at most 4 KiB.
func readNodeKeyLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(in, 4<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// lockNodeInstall holds an exclusive lock on path until the returned func
// runs, so two installs cannot overlap. The lock file is never removed, so
// every run locks the same file.
func lockNodeInstall(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: the installer's own lock file.
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("another silo node install or uninstall is running")
		}
		return nil, err
	}
	return func() { _ = file.Close() }, nil
}

// writeNodeFile writes data to path with mode, whatever mode an existing file
// had.
func writeNodeFile(path string, data []byte, mode os.FileMode) error {
	return writeNodeFileFrom(path, bytes.NewReader(data), mode)
}

func writeNodeFileFrom(path string, src io.Reader, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // G304: a path under the user's own Library.
	if err != nil {
		return err
	}
	_, err = io.Copy(file, src)
	err = errors.Join(err, file.Chmod(mode), file.Sync(), file.Close())
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func copyFile(from, to string, mode os.FileMode) error {
	src, err := os.Open(from) //nolint:gosec // G304: this process's own executable.
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	return writeNodeFileFrom(to, src, mode)
}

// sameFile reports whether a and b name one existing file.
func sameFile(a, b string) bool {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	return err == nil && os.SameFile(aInfo, bInfo)
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

// nodeLogTail returns the last lines the log gained after offset from, or the
// log's last lines when it gained none.
func nodeLogTail(path string, from int64) string {
	const maxLines, maxBytes = 20, 64 << 10
	file, err := os.Open(path) //nolint:gosec // G304: the node's own log.
	if err != nil {
		return "  (no log)"
	}
	defer func() { _ = file.Close() }()
	size := fileSize(path)
	if from >= size {
		from = 0
	}
	from = max(from, size-maxBytes)
	data, err := io.ReadAll(io.NewSectionReader(file, from, size-from))
	if err != nil {
		return "  (log unreadable)"
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return "  (empty)"
	}
	return "  " + strings.Join(lines, "\n  ")
}
