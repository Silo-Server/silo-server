package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

const recoveryTestManifest = "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:9\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2.669000,\nseg_00009.m4s\n#EXTINF:1.669000,\nseg_00010.m4s\n#EXTINF:1.668000,\nseg_00011.m4s\n"

// This executable stands in for FFmpeg at the real process boundary. Each
// encoder stays alive until canceled; probes wait for an explicit test release.
func TestCopyRecoveryFFmpegProcess(t *testing.T) {
	address := os.Getenv("SILO_COPY_RECOVERY_PROCESS")
	if address == "" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	probe := false
	for _, arg := range args {
		probe = probe || arg == "framecrc"
	}
	if !probe {
		if err := os.WriteFile(args[len(args)-1], []byte(recoveryTestManifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(args); err != nil {
		t.Fatal(err)
	}
	release := make([]byte, 1)
	if _, err := io.ReadFull(conn, release); err != nil {
		t.Fatal(err)
	}
	if !probe && release[0] == 'g' {
		outputDir := filepath.Dir(args[len(args)-1])
		if err := os.WriteFile(filepath.Join(outputDir, "seg_00012.m4s"), []byte("restored encoder segment"), 0o600); err != nil {
			t.Fatal(err)
		}
		manifest := recoveryTestManifest + "#EXTINF:2.000000,\nseg_00012.m4s\n"
		if err := os.WriteFile(args[len(args)-1], []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, release); err != nil {
			t.Fatal(err)
		}
	}
	if probe {
		fmt.Println("#tb 0: 1/1000")
		switch release[0] {
		case 'f':
			return // Invalid probe output: no video packet timestamp.
		case 'm':
			fmt.Println("0, 17000, 17000, 41, 1024, 0x12345678")
		default:
			fmt.Println("0, 18000, 18000, 41, 1024, 0x12345678")
		}
	}
}

type copyRecoveryProcess struct {
	conn net.Conn
	args []string
}

func (p copyRecoveryProcess) isProbe() bool {
	for _, arg := range p.args {
		if arg == "framecrc" {
			return true
		}
	}
	return false
}

func newCopyRecoverySession(t *testing.T) (*TranscodeManager, *TranscodeSession, <-chan copyRecoveryProcess) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan copyRecoveryProcess, 8)
	stop := make(chan struct{})
	var connectionsMu sync.Mutex
	var connections []net.Conn
	stopped := false
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		connectionsMu.Lock()
		defer connectionsMu.Unlock()
		stopped = true
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connectionsMu.Lock()
			if stopped {
				connectionsMu.Unlock()
				_ = conn.Close()
				return
			}
			connections = append(connections, conn)
			connectionsMu.Unlock()
			var args []string
			if err := json.NewDecoder(conn).Decode(&args); err != nil {
				_ = conn.Close()
				continue
			}
			select {
			case events <- copyRecoveryProcess{conn: conn, args: args}:
			case <-stop:
				_ = conn.Close()
				return
			}
		}
	}()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\nexport SILO_COPY_RECOVERY_PROCESS='%s'\nexec '%s' -test.run='^TestCopyRecoveryFFmpegProcess$' -- \"$@\"\n", listener.Addr(), strings.ReplaceAll(bin, "'", "'\\''"))
	if err := os.WriteFile(ffmpeg, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := StartTranscode(t.Context(), TranscodeOpts{
		SessionID:              "copy-capacity-recovery",
		InputPath:              "/input.mkv",
		OutputDir:              t.TempDir(),
		FFmpegPath:             ffmpeg,
		SeekSeconds:            18.261,
		StreamOriginSeconds:    18,
		CopySeekAnchorResolved: true,
		FastStart:              true,
		TargetCodecVideo:       "copy",
		TargetCodecAudio:       "copy",
		SegmentDuration:        2,
		StartSegmentNumber:     9,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	m := NewTranscodeManager()
	m.RegisterTranscodeSession(session.Opts().SessionID, session)
	initial := awaitCopyRecoveryProcess(t, events)
	if initial.isProbe() {
		t.Fatal("initial encoder was a probe")
	}
	t.Cleanup(func() { _ = initial.conn.Close() })
	return m, session, events
}

func awaitCopyRecoveryProcess(t *testing.T, events <-chan copyRecoveryProcess) copyRecoveryProcess {
	t.Helper()
	select {
	case event := <-events:
		t.Cleanup(func() { _ = event.conn.Close() })
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("FFmpeg process did not report readiness")
		return copyRecoveryProcess{}
	}
}

type copyRecoveryResult struct {
	target SegmentRecoveryTarget
	ok     bool
	err    error
}

func startCopyRecovery(m *TranscodeManager, session *TranscodeSession) <-chan copyRecoveryResult {
	result := make(chan copyRecoveryResult, 1)
	go func() {
		target, ok, err := m.RestartSegmentLocked(context.Background(), session.Opts().SessionID, session, 10)
		result <- copyRecoveryResult{target: target, ok: ok, err: err}
	}()
	return result
}

func awaitCopyRecoveryResult(t *testing.T, results <-chan copyRecoveryResult) copyRecoveryResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("copy recovery did not finish")
		return copyRecoveryResult{}
	}
}

func TestRestartSegmentLocked_CopyRecoveryReapsBeforeProbe(t *testing.T) {
	for _, limit := range []int{1, 0} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			encodinglimit.Configure(limit)
			t.Cleanup(func() { encodinglimit.Configure(0) })
			m, session, events := newCopyRecoverySession(t)
			oldDone := session.Done()
			results := startCopyRecovery(m, session)
			var probe copyRecoveryProcess
			select {
			case probe = <-events:
				t.Cleanup(func() { _ = probe.conn.Close() })
			case result := <-results:
				t.Fatalf("recovery returned before probing: ok=%t err=%v", result.ok, result.err)
			case <-time.After(5 * time.Second):
				t.Fatal("recovery did not start its anchor probe")
			}
			if !probe.isProbe() {
				t.Fatal("replacement started before anchor resolution")
			}
			select {
			case <-oldDone:
			default:
				t.Error("anchor probe started before the old encoder was reaped")
			}
			if capacity := encodinglimit.Snapshot(); capacity.ActiveJobs != 1 {
				t.Errorf("probe capacity = %+v, want only the probe active", capacity)
			}
			if limit == 1 {
				opts := session.Opts()
				if _, _, err := ResolveCopySeekAnchor(t.Context(), opts.FFmpegPath, opts.InputPath, 30, opts.SegmentDuration); !errors.Is(err, encodinglimit.ErrAtCapacity) {
					t.Errorf("a competing anchor probe bypassed the occupied slot: %v", err)
				}
			}
			if decision := session.SegmentRecoveryDecision(10, time.Now()); decision.Reason != "transcode_restarting" {
				t.Errorf("recovery decision during probe = %+v, want restart in flight", decision)
			}
			if _, err := probe.conn.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			result := awaitCopyRecoveryResult(t, results)
			if result.err != nil || !result.ok {
				t.Fatalf("recovery: ok=%t err=%v", result.ok, result.err)
			}
			if math.Abs(result.target.SeekSeconds-20.669) > 0.0001 || result.target.StreamOriginSeconds != 18 || result.target.StartSegmentNumber != 9 || !result.target.CopySeekAnchorResolved {
				t.Fatalf("recovery changed absolute seek or manifest mapping: %+v", result.target)
			}
			replacement := awaitCopyRecoveryProcess(t, events)
			args := strings.Join(replacement.args, "\n") + "\n"
			if replacement.isProbe() || !strings.Contains(args, "-ss\n20.669\n") || !strings.Contains(args, "-start_number\n9\n") {
				t.Fatalf("replacement args changed seek or numbering: %s", args)
			}
			if seek, ok, err := session.SegmentStartTime(10); err != nil || !ok || math.Abs(seek-20.669) > 0.0001 {
				t.Fatalf("replacement manifest mapping = %.6f, %t, %v", seek, ok, err)
			}
			if capacity := encodinglimit.Snapshot(); capacity.ActiveJobs != 1 {
				t.Errorf("replacement capacity = %+v, want one encoder", capacity)
			}
			m.CloseTranscodeSession(session.Opts().SessionID, "")
			if session.IsRunning() || encodinglimit.Snapshot().ActiveJobs != 0 {
				t.Fatal("cleanup leaked the replacement encoder or its capacity slot")
			}
		})
	}
}

func TestRestartSegmentLocked_CopyRecoverySerializesCleanup(t *testing.T) {
	for _, cleanup := range []string{"close", "close_if", "shutdown"} {
		t.Run(cleanup, func(t *testing.T) {
			encodinglimit.Configure(1)
			t.Cleanup(func() { encodinglimit.Configure(0) })
			m, session, events := newCopyRecoverySession(t)
			results := startCopyRecovery(m, session)
			probe := awaitCopyRecoveryProcess(t, events)
			if !probe.isProbe() {
				t.Fatal("expected a blocked anchor probe")
			}
			closed := make(chan struct{})
			id := session.Opts().SessionID
			go func() {
				defer close(closed)
				switch cleanup {
				case "close":
					m.CloseTranscodeSession(id, "")
				case "close_if":
					m.CloseTranscodeSessionIf(id, session, "")
				default:
					ctx, cancel := context.WithCancel(context.Background())
					done := m.StartShutdownCleanup(ctx)
					cancel()
					<-done
				}
			}()
			// Observe the cleanup waiting on the same lifecycle lock, or its
			// premature completion. No delay is used as evidence of serialization.
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
		waiting:
			for {
				m.lifecycleMu.Lock()
				lock := m.lifecycleLocks[id]
				waiting := lock != nil && lock.refs == 2
				m.lifecycleMu.Unlock()
				if waiting {
					break
				}
				select {
				case <-closed:
					t.Error("cleanup completed while copy recovery still owned the lifecycle lock")
					break waiting
				case <-ticker.C:
				case <-deadline.C:
					t.Error("cleanup did not enter the lifecycle lock")
					break waiting
				}
			}
			if _, err := probe.conn.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			result := awaitCopyRecoveryResult(t, results)
			if result.err != nil || !result.ok {
				t.Errorf("in-flight recovery failed: ok=%t err=%v", result.ok, result.err)
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not finish after recovery released its lifecycle lock")
			}
			if session.IsRunning() || encodinglimit.Snapshot().ActiveJobs != 0 {
				t.Error("concurrent cleanup leaked a replacement process or capacity slot")
			}
			if m.GetTranscodeSession(id) != nil {
				t.Error("cleanup left the session registered")
			}
		})
	}
}

func TestRestartSegmentLocked_CopyRecoveryUnresolvedKeepsEncoder(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	m, session, events := newCopyRecoverySession(t)
	before := session.Opts()
	target, ok, err := m.RestartSegmentLocked(t.Context(), before.SessionID, session, 99)
	if err != nil || ok || target != (SegmentRecoveryTarget{}) {
		t.Fatalf("unresolved recovery = %+v, %t, %v", target, ok, err)
	}
	if !session.IsRunning() || session.Opts() != before || encodinglimit.Snapshot().ActiveJobs != 1 {
		t.Fatal("unresolved recovery stopped or changed the encoder")
	}
	select {
	case event := <-events:
		_ = event.conn.Close()
		t.Fatal("unresolved recovery launched a process")
	default:
	}
}

func TestRestartSegmentLocked_CopyRecoveryProbeFailureRestoresProgress(t *testing.T) {
	for _, limit := range []int{1, 0} {
		for _, release := range []byte{'f', 'm'} {
			t.Run(fmt.Sprintf("limit_%d_%c", limit, release), func(t *testing.T) {
				encodinglimit.Configure(limit)
				t.Cleanup(func() { encodinglimit.Configure(0) })
				m, session, events := newCopyRecoverySession(t)
				before := session.Opts()
				retained := filepath.Join(before.OutputDir, "seg_00009.m4s")
				if err := os.WriteFile(retained, []byte("existing segment"), 0o600); err != nil {
					t.Fatal(err)
				}
				hooks := make(chan struct{}, 2)
				session.SetRestartHook(func(context.Context) { hooks <- struct{}{} })
				var restored copyRecoveryProcess
				// Neither retry can improve the probe result. A valid producer must
				// still survive so ordinary segment requests can make progress.
				for range 2 {
					results := startCopyRecovery(m, session)
					probe := awaitCopyRecoveryProcess(t, events)
					if !probe.isProbe() {
						t.Fatal("recovery did not start an anchor probe")
					}
					if _, err := probe.conn.Write([]byte{release}); err != nil {
						t.Fatal(err)
					}
					result := awaitCopyRecoveryResult(t, results)
					if result.ok || release == 'f' && result.err == nil || release == 'm' && result.err != nil {
						t.Fatalf("failed or unmapped probe outcome = %+v", result)
					}
					if !session.IsRunning() || encodinglimit.Snapshot().ActiveJobs != 1 || session.Opts() != before {
						t.Fatal("failed or unmapped probe left no encoder with the previous recipe")
					}
					restored = awaitCopyRecoveryProcess(t, events)
					if restored.isProbe() || !slices.Equal(restored.args, buildFFmpegArgs(before)) {
						t.Fatalf("restored encoder changed the previous recipe: %v", restored.args)
					}
					if data, err := os.ReadFile(retained); err != nil || string(data) != "existing segment" {
						t.Fatalf("restored encoder discarded previous output: %q, %v", data, err)
					}
					select {
					case <-hooks:
					default:
						t.Fatal("restored encoder did not re-arm its restart hook")
					}
				}
				if _, ok, err := m.RestartSegmentLocked(t.Context(), before.SessionID, session, 99); ok || err != nil || !session.IsRunning() {
					t.Fatalf("out-of-manifest retry lost the restored producer: %t, %v", ok, err)
				}
				if _, err := restored.conn.Write([]byte{'g'}); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(restored.conn, make([]byte, 1)); err != nil {
					t.Fatal(err)
				}
				lease, err := session.WaitForOpenSegment("seg_00012.m4s", time.Second)
				if err != nil {
					t.Fatalf("restored encoder did not produce the next segment: %v", err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
				if seek, ok, err := session.SegmentStartTime(12); err != nil || !ok || math.Abs(seek-24.006) > 0.0001 {
					t.Fatalf("restored encoder changed URI timing: %.6f, %t, %v", seek, ok, err)
				}
				m.CloseTranscodeSession(before.SessionID, "")
				if encodinglimit.Snapshot().ActiveJobs != 0 {
					t.Fatal("restored encoder cleanup leaked capacity")
				}
			})
		}
	}
}

func TestRestartSegmentLocked_CopyRecoveryRestoreFailureAllowsCleanup(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	m, session, events := newCopyRecoverySession(t)
	before := session.Opts()
	oldDone := session.Done()
	crashed := make(chan struct{}, 1)
	m.OnFFmpegCrash = func(_ context.Context, id string, failed *TranscodeSession) {
		if id != before.SessionID || failed != session {
			t.Error("restoration failure cleaned up the wrong session")
		}
		m.CloseTranscodeSessionUnderLifecycle(id, "")
		crashed <- struct{}{}
	}
	results := startCopyRecovery(m, session)
	probe := awaitCopyRecoveryProcess(t, events)
	if err := os.Remove(before.FFmpegPath); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.conn.Write([]byte{'m'}); err != nil {
		t.Fatal(err)
	}
	result := awaitCopyRecoveryResult(t, results)
	if result.ok || result.err == nil {
		t.Fatalf("failed encoder restoration was reported as a retryable miss: %+v", result)
	}
	awaitCopyRecoveryMonitor(t, m.monitorLocalTranscodeExit(before.SessionID, session, oldDone, 0))
	select {
	case <-crashed:
	default:
		t.Fatal("intentional probe cancellation hid a failed encoder restoration")
	}
	if m.GetTranscodeSession(before.SessionID) != nil || encodinglimit.Snapshot().ActiveJobs != 0 {
		t.Fatal("failed encoder restoration blocked reconstruction or leaked capacity")
	}
}

func TestRestartSegmentLocked_CopyRecoveryRestoreRespectsCapacity(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	m, session, events := newCopyRecoverySession(t)
	before := session.Opts()
	oldDone := session.Done()
	var competitor *TranscodeSession
	err := m.restartSessionLocked(before.SessionID, session, func() error {
		return session.restart(t.Context(), SegmentRecoveryTarget{}, func() (SegmentRecoveryTarget, bool, error) {
			// Another workload can take the freed slot after probing. Restoration
			// must use normal admission and must not stop that unrelated workload.
			opts := before
			opts.SessionID = "competing-encoder"
			opts.OutputDir = t.TempDir()
			var startErr error
			competitor, startErr = StartTranscode(t.Context(), opts)
			if startErr != nil {
				return SegmentRecoveryTarget{}, false, startErr
			}
			t.Cleanup(func() { _ = competitor.Close() })
			awaitCopyRecoveryProcess(t, events)
			return SegmentRecoveryTarget{}, false, nil
		})
	})
	if !errors.Is(err, encodinglimit.ErrAtCapacity) {
		t.Fatalf("restoration bypassed occupied capacity or hid its refusal: %v", err)
	}
	m.OnFFmpegCrash = func(_ context.Context, id string, failed *TranscodeSession) {
		m.CloseTranscodeSessionIfUnderLifecycle(id, failed, "")
	}
	awaitCopyRecoveryMonitor(t, m.monitorLocalTranscodeExit(before.SessionID, session, oldDone, 0))
	if m.GetTranscodeSession(before.SessionID) != nil || !competitor.IsRunning() || encodinglimit.Snapshot().ActiveJobs != 1 {
		t.Fatal("restoration refusal left a dead session or disturbed the competing encoder")
	}
	if err := competitor.Close(); err != nil {
		t.Fatal(err)
	}
	if encodinglimit.Snapshot().ActiveJobs != 0 {
		t.Fatal("competing encoder leaked capacity")
	}
}

func TestRestartSegmentLocked_CopyRecoveryProbeFailureCanRetry(t *testing.T) {
	for _, release := range []byte{'f', 'm'} {
		t.Run(string(release), func(t *testing.T) {
			encodinglimit.Configure(1)
			t.Cleanup(func() { encodinglimit.Configure(0) })
			m, session, events := newCopyRecoverySession(t)
			before := session.Opts()
			results := startCopyRecovery(m, session)
			probe := awaitCopyRecoveryProcess(t, events)
			if _, err := probe.conn.Write([]byte{release}); err != nil {
				t.Fatal(err)
			}
			result := awaitCopyRecoveryResult(t, results)
			if result.ok || release == 'f' && result.err == nil || release == 'm' && result.err != nil {
				t.Fatalf("failed or unmapped probe outcome = %+v", result)
			}
			if !session.IsRunning() || encodinglimit.Snapshot().ActiveJobs != 1 || session.Opts() != before {
				t.Fatal("failed or unmapped probe did not restore the previous recipe")
			}
			restored := awaitCopyRecoveryProcess(t, events)
			if restored.isProbe() {
				t.Fatal("failed probe started another probe instead of restoring the encoder")
			}
			if manifest, err := os.ReadFile(filepath.Join(before.OutputDir, "stream.m3u8")); err != nil || string(manifest) != recoveryTestManifest {
				t.Fatalf("failed probe changed the manifest: %s, %v", manifest, err)
			}
			results = startCopyRecovery(m, session)
			probe = awaitCopyRecoveryProcess(t, events)
			if _, err := probe.conn.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			result = awaitCopyRecoveryResult(t, results)
			if !result.ok || result.err != nil || !session.IsRunning() {
				t.Fatalf("failed probe prevented a successful retry: %+v", result)
			}
			m.CloseTranscodeSession(before.SessionID, "")
			if encodinglimit.Snapshot().ActiveJobs != 0 {
				t.Fatal("retry cleanup leaked a slot")
			}
		})
	}
}

func TestRestartSegmentLocked_CopyRecoverySerializesConcurrentRestart(t *testing.T) {
	encodinglimit.Configure(1)
	t.Cleanup(func() { encodinglimit.Configure(0) })
	m, session, events := newCopyRecoverySession(t)
	id := session.Opts().SessionID
	results := startCopyRecovery(m, session)
	probe := awaitCopyRecoveryProcess(t, events)
	errs := make(chan error, 1)
	go func() {
		errs <- m.RestartSessionLockedWithCopySeekAnchor(context.Background(), id, session, 40, 20, 40)
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		m.lifecycleMu.Lock()
		lock := m.lifecycleLocks[id]
		waiting := lock != nil && lock.refs == 2
		m.lifecycleMu.Unlock()
		if waiting {
			break
		}
		select {
		case err := <-errs:
			t.Fatalf("concurrent restart bypassed the lifecycle lock: %v", err)
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("concurrent restart did not enter the lifecycle lock")
		}
	}
	if _, err := probe.conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if result := awaitCopyRecoveryResult(t, results); result.err != nil || !result.ok {
		t.Fatalf("copy recovery failed: %+v", result)
	}
	select {
	case err := <-errs:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent restart did not finish")
	}
	opts := session.Opts()
	if !session.IsRunning() || opts.SeekSeconds != 40 || opts.StartSegmentNumber != 20 || encodinglimit.Snapshot().ActiveJobs != 1 {
		t.Fatalf("concurrent restart left inconsistent recipe or processes: %+v", opts)
	}
	m.CloseTranscodeSession(id, "")
	if encodinglimit.Snapshot().ActiveJobs != 0 {
		t.Fatal("concurrent restart cleanup leaked capacity")
	}
}

func TestRestartSegmentLocked_CopyRecoveryIgnoresQueuedOldProcessExit(t *testing.T) {
	for _, release := range []byte{1, 'f', 'm'} {
		t.Run(fmt.Sprintf("probe_%d", release), func(t *testing.T) {
			encodinglimit.Configure(1)
			t.Cleanup(func() { encodinglimit.Configure(0) })
			m, session, events := newCopyRecoverySession(t)
			id := session.Opts().SessionID
			crashes := make(chan bool, 2)
			m.OnFFmpegCrash = func(_ context.Context, id string, dead *TranscodeSession) {
				crashes <- m.CloseTranscodeSessionIfUnderLifecycle(id, dead, "")
			}
			oldDone := session.Done()
			results := startCopyRecovery(m, session)
			probe := awaitCopyRecoveryProcess(t, events)
			// Run the real exit monitor with its grace period disabled. Its
			// observed process has exited, but the copy probe still owns lifecycle.
			finished := m.monitorLocalTranscodeExit(id, session, oldDone, 0)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
		waiting:
			for {
				m.lifecycleMu.Lock()
				lock := m.lifecycleLocks[id]
				waiting := lock != nil && lock.refs == 2
				m.lifecycleMu.Unlock()
				if waiting {
					break
				}
				select {
				case <-finished:
					t.Error("old exit monitor bypassed the in-flight recovery lifecycle lock")
					break waiting
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("old exit monitor did not enter the lifecycle lock")
				}
			}
			if _, err := probe.conn.Write([]byte{release}); err != nil {
				t.Fatal(err)
			}
			result := awaitCopyRecoveryResult(t, results)
			awaitCopyRecoveryMonitor(t, finished)
			select {
			case matched := <-crashes:
				t.Errorf("intentional old-process cancellation triggered crash cleanup (matched=%t)", matched)
			default:
			}
			if m.GetTranscodeSession(id) != session {
				t.Fatal("old exit monitor removed the recovering session")
			}
			if release != 1 {
				if result.ok || release == 'f' && result.err == nil || release == 'm' && result.err != nil {
					t.Fatalf("probe failure changed its reported outcome: %+v", result)
				}
			} else if result.err != nil || !result.ok {
				t.Fatalf("old exit monitor interfered with the successful replacement: %+v", result)
			}
			if !session.IsRunning() || session.Done() == oldDone || encodinglimit.Snapshot().ActiveJobs != 1 {
				t.Fatal("old exit monitor interfered with the replacement or restored encoder")
			}
			awaitCopyRecoveryProcess(t, events)
			replacementDone := session.Done()
			session.mu.Lock()
			process := session.cmd.Process
			session.mu.Unlock()
			if err := process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-replacementDone:
			case <-time.After(5 * time.Second):
				t.Fatal("replacement crash was not reaped")
			}
			// A late observer for the old child must not report a later child's
			// crash, even though that child is also stopped now.
			awaitCopyRecoveryMonitor(t, m.monitorLocalTranscodeExit(id, session, oldDone, 0))
			select {
			case <-crashes:
				t.Fatal("old process monitor cleaned up a later generation's crash")
			default:
			}
			awaitCopyRecoveryMonitor(t, m.monitorLocalTranscodeExit(id, session, replacementDone, 0))
			select {
			case matched := <-crashes:
				if !matched {
					t.Error("the current process crash did not close its mapped session")
				}
			default:
				t.Error("the current process crash no longer triggers cleanup")
			}
			if m.GetTranscodeSession(id) != nil || encodinglimit.Snapshot().ActiveJobs != 0 {
				t.Error("current process crash cleanup leaked the session or capacity")
			}
		})
	}
}

func awaitCopyRecoveryMonitor(t *testing.T, finished <-chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("exit monitor did not finish")
	}
}
