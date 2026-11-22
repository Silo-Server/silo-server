package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func writeRecoverySegmentFile(t *testing.T, dir, name, payload string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readLeaseBytes(t *testing.T, lease *SegmentLease) string {
	t.Helper()
	defer func() { _ = lease.Close() }()
	data, err := io.ReadAll(lease.File)
	if err != nil {
		t.Fatalf("read segment lease: %v", err)
	}
	return string(data)
}

func TestOpenSegmentWithRecovery_ImmediateHit(t *testing.T) {
	dir := t.TempDir()
	writeRecoverySegmentFile(t, dir, "seg_00003.ts", "immediate-segment-bytes")

	session := &TranscodeSession{
		outputDir: dir,
		opts: TranscodeOpts{
			SessionID: "sess-immediate",
			OutputDir: dir,
		},
	}

	lease, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00003.ts", SegmentRecoveryOptions{
		SessionID: "sess-immediate",
		Component: "api",
		Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
			t.Fatal("Restart must not be called when segment is already on disk")
			return SegmentRecoveryTarget{}, false, nil
		},
	})
	if err != nil {
		t.Fatalf("OpenSegmentWithRecovery: %v", err)
	}
	if got := readLeaseBytes(t, lease); got != "immediate-segment-bytes" {
		t.Fatalf("segment bytes = %q, want %q", got, "immediate-segment-bytes")
	}
}

func TestOpenSegmentWithRecovery_WaitThenHit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		session := &TranscodeSession{
			outputDir: dir,
			running:   true,
			opts: TranscodeOpts{
				SessionID:          "sess-wait",
				OutputDir:          dir,
				StartSegmentNumber: 0,
				SegmentDuration:    2,
			},
		}

		go func() {
			time.Sleep(50 * time.Millisecond) //nolint:forbidigo // synctest advances virtual time before producing the waited segment
			writeRecoverySegmentFile(t, dir, "seg_00000.ts", "waited-segment-bytes")
		}()

		lease, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00000.ts", SegmentRecoveryOptions{
			SessionID: "sess-wait",
			Component: "api",
			Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
				t.Fatal("Restart must not be called when wait succeeds")
				return SegmentRecoveryTarget{}, false, nil
			},
		})
		if err != nil {
			t.Fatalf("OpenSegmentWithRecovery: %v", err)
		}
		if got := readLeaseBytes(t, lease); got != "waited-segment-bytes" {
			t.Fatalf("segment bytes = %q, want %q", got, "waited-segment-bytes")
		}
	})
}

func TestOpenSegmentWithRecovery_WaitTimeoutRestartAndPostRestartOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		writeRecoverySegmentFile(t, dir, "stream.m3u8", "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:2.000000,\nseg_00000.m4s\n")
		writeRecoverySegmentFile(t, dir, "seg_00000.m4s", "initial-segment")

		genStarted := time.Now().Add(-5 * time.Second)
		session := &TranscodeSession{
			outputDir:           dir,
			running:             true,
			generationStartedAt: genStarted,
			copyPlan:            &copySegmentPlan{durations: make([]float64, 50)},
			opts: TranscodeOpts{
				SessionID:          "sess-restart",
				OutputDir:          dir,
				TargetCodecVideo:   "copy",
				StartSegmentNumber: 0,
				SegmentDuration:    2,
			},
		}

		var restartCalls atomic.Int32
		var gotSegNum int
		var gotSeenGen time.Time
		start := time.Now()
		var restartedAt time.Time
		var producerDone chan struct{}
		defer func() {
			if producerDone != nil {
				<-producerDone
			}
		}()
		const (
			targetDelay    = 30 * time.Millisecond
			lookaheadDelay = 40 * time.Millisecond
			lookaheadLimit = 1200 * time.Millisecond
		)

		lease, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00002.m4s", SegmentRecoveryOptions{
			SessionID:            "sess-restart",
			Component:            "api",
			CopyLookaheadTimeout: lookaheadLimit,
			Restart: func(_ context.Context, segNum int, seenGeneration time.Time) (SegmentRecoveryTarget, bool, error) {
				restartCalls.Add(1)
				gotSegNum = segNum
				gotSeenGen = seenGeneration
				restartedAt = time.Now()
				done := make(chan struct{})
				producerDone = done
				go func() {
					defer close(done)
					time.Sleep(targetDelay) //nolint:forbidigo // synctest advances virtual time to simulate post-restart segment production
					writeRecoverySegmentFile(t, dir, "seg_00002.m4s", "restarted-segment-2")
					time.Sleep(lookaheadDelay) //nolint:forbidigo // synctest advances virtual time to simulate copy-video lookahead segment
					writeRecoverySegmentFile(t, dir, "seg_00003.m4s", "lookahead-segment-3")
				}()
				return SegmentRecoveryTarget{
					SeekSeconds:         4.0,
					StreamOriginSeconds: 4.0,
					StartSegmentNumber:  2,
				}, true, nil
			},
		})
		returnedAt := time.Now()
		if err != nil {
			t.Fatalf("OpenSegmentWithRecovery: %v", err)
		}
		if got := readLeaseBytes(t, lease); got != "restarted-segment-2" {
			t.Fatalf("segment bytes = %q, want %q", got, "restarted-segment-2")
		}
		if restartCalls.Load() != 1 {
			t.Fatalf("restart calls = %d, want 1", restartCalls.Load())
		}
		if gotSegNum != 2 {
			t.Fatalf("restart segNum = %d, want 2", gotSegNum)
		}
		if !gotSeenGen.Equal(genStarted) {
			t.Fatalf("restart seenGeneration = %v, want %v", gotSeenGen, genStarted)
		}
		if restartedAt.Sub(start) < activeSegmentWait {
			t.Fatalf("restart happened after %v, want wait timeout >= %v", restartedAt.Sub(start), activeSegmentWait)
		}
		postRestartElapsed := returnedAt.Sub(restartedAt)
		if postRestartElapsed < targetDelay+lookaheadDelay {
			t.Fatalf("returned %v after restart, want >= %v so lookahead waited for seg_00003.m4s", postRestartElapsed, targetDelay+lookaheadDelay)
		}
		if postRestartElapsed >= lookaheadLimit {
			t.Fatalf("returned %v after restart, want < %v (lookahead timed out instead of opening seg_00003.m4s)", postRestartElapsed, lookaheadLimit)
		}
	})
}

func TestOpenSegmentWithRecovery_UnresolvedCopyVideoTargetReturnsNotFound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		session := &TranscodeSession{
			outputDir: dir,
			running:   true,
			opts: TranscodeOpts{
				SessionID:          "sess-copy-unresolved",
				OutputDir:          dir,
				TargetCodecVideo:   "copy",
				StartSegmentNumber: 0,
				SegmentDuration:    2,
			},
		}

		var restartCalls atomic.Int32
		start := time.Now()
		lease, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00040.m4s", SegmentRecoveryOptions{
			SessionID: "sess-copy-unresolved",
			Component: "jellycompat",
			Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
				restartCalls.Add(1)
				return SegmentRecoveryTarget{}, false, nil
			},
		})
		if lease != nil {
			_ = lease.Close()
			t.Fatal("expected nil lease for unresolved copy-video target")
		}
		if !errors.Is(err, ErrSegmentNotFound) {
			t.Fatalf("err = %v, want ErrSegmentNotFound", err)
		}
		if restartCalls.Load() != 1 {
			t.Fatalf("restart calls = %d, want 1", restartCalls.Load())
		}
		if elapsed := time.Since(start); elapsed >= postRestartSegmentWaitTimeout {
			t.Fatalf("unresolved copy target waited %v; must not enter post-restart wait", elapsed)
		}
	})
}

func TestOpenSegmentWithRecovery_InitSegmentWait(t *testing.T) {
	t.Run("waits while running", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			dir := t.TempDir()
			session := &TranscodeSession{
				outputDir: dir,
				running:   true,
				opts: TranscodeOpts{
					SessionID: "sess-init",
					OutputDir: dir,
				},
			}

			go func() {
				time.Sleep(50 * time.Millisecond) //nolint:forbidigo // synctest advances virtual time before writing init.mp4
				writeRecoverySegmentFile(t, dir, "init.mp4", "ftyp-moov-bytes")
			}()

			lease, err := OpenSegmentWithRecovery(context.Background(), session, "init.mp4", SegmentRecoveryOptions{
				SessionID: "sess-init",
				Component: "transcodenode",
				Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
					t.Fatal("Restart must not be called for init segment")
					return SegmentRecoveryTarget{}, false, nil
				},
			})
			if err != nil {
				t.Fatalf("OpenSegmentWithRecovery(init.mp4): %v", err)
			}
			if got := readLeaseBytes(t, lease); got != "ftyp-moov-bytes" {
				t.Fatalf("init bytes = %q, want %q", got, "ftyp-moov-bytes")
			}
		})
	})

	t.Run("returns not found when stopped", func(t *testing.T) {
		dir := t.TempDir()
		session := &TranscodeSession{
			outputDir: dir,
			running:   false,
			opts: TranscodeOpts{
				SessionID: "sess-init-stopped",
				OutputDir: dir,
			},
		}

		lease, err := OpenSegmentWithRecovery(context.Background(), session, "init.mp4", SegmentRecoveryOptions{
			SessionID: "sess-init-stopped",
			Component: "api",
			Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
				t.Fatal("Restart must not be called for init segment")
				return SegmentRecoveryTarget{}, false, nil
			},
		})
		if lease != nil {
			_ = lease.Close()
			t.Fatal("expected nil lease")
		}
		if !errors.Is(err, ErrSegmentNotFound) {
			t.Fatalf("err = %v, want ErrSegmentNotFound", err)
		}
	})
}

func TestOpenSegmentWithRecovery_RestartErrorPropagated(t *testing.T) {
	dir := t.TempDir()
	session := &TranscodeSession{
		outputDir: dir,
		running:   false,
		opts: TranscodeOpts{
			SessionID: "sess-err",
			OutputDir: dir,
		},
	}

	sentinel := errors.New("restart exploded")
	lease, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00010.ts", SegmentRecoveryOptions{
		SessionID: "sess-err",
		Component: "api",
		Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
			return SegmentRecoveryTarget{}, false, sentinel
		},
	})
	if lease != nil {
		_ = lease.Close()
		t.Fatal("expected nil lease on restart error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Lines() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw := strings.TrimSpace(b.buf.String())
	if raw == "" {
		return nil
	}
	var records []map[string]any
	for line := range strings.SplitSeq(raw, "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			records = append(records, rec)
		}
	}
	return records
}

func TestOpenSegmentWithRecovery_LogSurfaceAttributesAndMessages(t *testing.T) {
	t.Run("jellycompat emits play_session and api messages", func(t *testing.T) {
		var logs lockedBuffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
		t.Cleanup(func() { slog.SetDefault(prev) })

		dir := t.TempDir()
		session := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SessionID: "up-1", OutputDir: dir}}

		lease, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00025.ts", SegmentRecoveryOptions{
			SessionID:   "up-1",
			Component:   "jellycompat",
			PlaySession: "fp-abc",
			Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
				writeRecoverySegmentFile(t, dir, "seg_00025.ts", "ok")
				return SegmentRecoveryTarget{SeekSeconds: 50, StartSegmentNumber: 25}, true, nil
			},
		})
		if err != nil {
			t.Fatalf("OpenSegmentWithRecovery: %v", err)
		}
		_ = lease.Close()

		var sawMissing, sawRestart bool
		for _, rec := range logs.Lines() {
			if rec["component"] != "jellycompat" || rec["session"] != "up-1" || rec["playback_session_id"] != "up-1" {
				continue
			}
			if rec["play_session"] != "fp-abc" {
				t.Fatalf("missing play_session on record: %+v", rec)
			}
			switch rec["msg"] {
			case "transcode segment missing":
				sawMissing = true
			case "transcode seek restart":
				sawRestart = true
			}
		}
		if !sawMissing || !sawRestart {
			t.Fatalf("expected missing (%v) and seek restart (%v) logs", sawMissing, sawRestart)
		}
	})

	t.Run("transcodenode preserves node log messages", func(t *testing.T) {
		var logs lockedBuffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
		t.Cleanup(func() { slog.SetDefault(prev) })

		dir := t.TempDir()
		session := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SessionID: "node-1", OutputDir: dir}}

		// Success restart logs "transcode node seek restart".
		lease, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00030.ts", SegmentRecoveryOptions{
			SessionID: "node-1",
			Component: "transcodenode",
			Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
				writeRecoverySegmentFile(t, dir, "seg_00030.ts", "ok")
				return SegmentRecoveryTarget{SeekSeconds: 60, StartSegmentNumber: 30}, true, nil
			},
		})
		if err != nil {
			t.Fatalf("OpenSegmentWithRecovery: %v", err)
		}
		_ = lease.Close()

		// Error restart logs "restart transcode node at missing segment".
		_, err = OpenSegmentWithRecovery(context.Background(), session, "seg_00031.ts", SegmentRecoveryOptions{
			SessionID: "node-1",
			Component: "transcodenode",
			Restart: func(context.Context, int, time.Time) (SegmentRecoveryTarget, bool, error) {
				return SegmentRecoveryTarget{}, false, errors.New("boom")
			},
		})
		if err == nil {
			t.Fatal("expected restart error")
		}

		var sawNodeRestart, sawNodeErr bool
		for _, rec := range logs.Lines() {
			if rec["component"] != "transcodenode" || rec["session"] != "node-1" {
				continue
			}
			if _, hasPlay := rec["play_session"]; hasPlay {
				t.Fatalf("unexpected play_session on transcodenode record: %+v", rec)
			}
			switch rec["msg"] {
			case "transcode node seek restart":
				sawNodeRestart = true
			case "restart transcode node at missing segment":
				sawNodeErr = true
			}
		}
		if !sawNodeRestart || !sawNodeErr {
			t.Fatalf("expected node seek restart (%v) and node restart error (%v) logs", sawNodeRestart, sawNodeErr)
		}
	})
}

func TestOpenSegmentWithRecovery_SegmentNumberNameOverride(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		session := &TranscodeSession{
			outputDir: dir,
			running:   true,
			copyPlan:  &copySegmentPlan{durations: make([]float64, 5)},
			opts: TranscodeOpts{
				SessionID:          "sess-multidot",
				OutputDir:          dir,
				TargetCodecVideo:   "copy",
				StartSegmentNumber: 0,
				SegmentDuration:    2,
			},
		}

		// Segment 40 is past the 5-segment copy plan, so recovery returns
		// ErrSegmentNotFound immediately instead of waiting 10s as an init segment.
		start := time.Now()
		_, err := OpenSegmentWithRecovery(context.Background(), session, "seg_00040.m4s.extra", SegmentRecoveryOptions{
			SessionID:         "sess-multidot",
			Component:         "jellycompat",
			SegmentNumberName: "seg_00040",
		})
		if !errors.Is(err, ErrSegmentNotFound) {
			t.Fatalf("err = %v, want ErrSegmentNotFound", err)
		}
		if elapsed := time.Since(start); elapsed >= time.Second {
			t.Fatalf("elapsed = %v, want immediate out-of-range ErrSegmentNotFound without init-segment wait", elapsed)
		}
	})
}
