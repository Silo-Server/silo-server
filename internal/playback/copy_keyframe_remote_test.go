package playback

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workersource"
)

type keyframeRemoteFixture struct {
	ctx         context.Context
	adapter     *workerinput.Adapter
	authority   *workersource.Authority
	active      atomic.Bool
	reads       atomic.Int64
	unavailable atomic.Bool
	path        string
}

func newKeyframeRemoteFixture(t *testing.T, path, scratch string) *keyframeRemoteFixture {
	t.Helper()
	f := &keyframeRemoteFixture{path: path}
	f.active.Store(true)
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	authority, err := workersource.New(workersource.Options{Store: workersource.NewMemoryStore(), Origin: origin, Active: func(_ context.Context, node int, work string, generation uint64) bool {
		return f.active.Load() && node == 7 && work == "keyframes" && generation == 1
	}, PathApprover: func(_ context.Context, input, kind string) error {
		if input != path || kind != "media" {
			return workersource.ErrDenied
		}
		return nil
	}, VerifyRequest: func(r *http.Request, node int) error {
		if node != 7 || r.Header.Get("X-Test-Proof") != "signed" {
			return workersource.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.authority = authority
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reads.Add(1)
		if r.Method == http.MethodGet && f.unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		authority.Handler().ServeHTTP(w, r)
	})
	server.Start()
	t.Cleanup(server.Close)
	adapter, err := workerinput.New(workerinput.Options{Origins: []string{origin}, AllowHTTP: true, LocalRoot: scratch, Allowed: func(work string, generation uint64) bool {
		return f.active.Load() && work == "keyframes" && generation == 1
	}, Sign: func(r *http.Request) error { r.Header.Set("X-Test-Proof", "signed"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	f.adapter = adapter
	t.Cleanup(func() { _ = adapter.Close() })
	f.ctx = workerinput.WithContext(t.Context(), adapter)
	return f
}
func (f *keyframeRemoteFixture) open(t *testing.T) *workerinput.Handle {
	t.Helper()
	source, err := f.authority.Acquire(t.Context(), 7, "keyframes", 1, f.path, "media")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := f.adapter.Open(f.ctx, workerinput.Source{ID: source.Ref.ID, Revision: source.Ref.Revision, URL: source.URL, Grant: source.Grant, ETag: source.ETag, Size: source.Size, WorkID: source.WorkID, Generation: source.Generation})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}
func keyframeMKV(t *testing.T, ffmpeg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "irregular.mkv")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=96x54:rate=25:duration=8", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=8", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=12", "-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-g", "1000", "-bf", "2", "-sc_threshold", "0", "-force_key_frames", "0,0.9,2.2,2.6,5.2,7.2,7.6", "-c:a", "aac", "-output_ts_offset", "0.28", path}
	if data, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, data)
	}
	return path
}
func waitKeyframeCopy(t *testing.T, ctx context.Context, session *TranscodeSession) {
	t.Helper()
	select {
	case <-session.Done():
	case <-ctx.Done():
		t.Fatal("copy did not finish")
	}
	if err := session.WaitError(); err != nil {
		t.Fatal(err)
	}
}
func compareCopyFiles(t *testing.T, want, got string) {
	t.Helper()
	files, err := os.ReadDir(want)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range files {
		name := entry.Name()
		if name != "init.mp4" && filepath.Ext(name) != ".m4s" {
			continue
		}
		a, err := os.ReadFile(filepath.Join(want, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(got, name))
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(a) != sha256.Sum256(b) {
			t.Fatalf("copy artifact differs: %s", name)
		}
		count++
	}
	if count < 3 {
		t.Fatalf("only%d copy artifacts compared", count)
	}
}

// In-process integration proves index/segment correctness with the actual core
// range authority and private adapter. It does not prove OS mount isolation.
func TestAdmittedHTTPKeyframeIndexCopyPlaylistSeekAndReconstructionMatchLocal(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	path := keyframeMKV(t, ffmpeg)
	scratch := t.TempDir()
	remote := newKeyframeRemoteFixture(t, path, scratch)
	handle := remote.open(t)
	localIndex, err := keyframes.LoadVerified(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// A cold reconstruction has no saved plan or remote cache: it must verify
	// the admitted source, never fall back to opening the loopback URL as a file.
	base := TranscodeOpts{InputPath: path, OutputDir: filepath.Join(t.TempDir(), "local"), SessionID: "copy-local", SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, TotalDuration: 12, KeyframePlaylist: true, FFmpegPath: ffmpeg, HWAccel: HWAccelNone}
	local, err := StartTranscode(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	waitKeyframeCopy(t, ctx, local)
	options := base
	options.InputPath = handle.URL()
	options.OutputDir = filepath.Join(scratch, "remote")
	options.SessionID = "copy-remote"
	options.Reconstructed = true
	network, err := StartTranscode(remote.ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = network.Close() })
	waitKeyframeCopy(t, ctx, network)
	remoteIndex, err := keyframes.LoadSourceVerified(remote.ctx, admittedKeyframeSource(handle))
	if err != nil || !reflect.DeepEqual(localIndex, remoteIndex) {
		t.Fatalf("indexes local=%+v remote=%+v err=%v", localIndex, remoteIndex, err)
	}
	a, err := local.BuildPlaybackManifest("", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := network.BuildPlaybackManifest("", "")
	if err != nil || network.copyPlan == nil || !bytes.Equal(a, b) {
		t.Fatalf("copy playlists differ err=%v local=%s remote=%s", err, a, b)
	}
	compareCopyFiles(t, base.OutputDir, options.OutputDir)
	if _, ok, err := network.RestartSegment(remote.ctx, 2); err != nil || !ok {
		t.Fatalf("seek restart ok=%v err=%v", ok, err)
	}
	waitKeyframeCopy(t, ctx, network)
	if got := network.Opts(); got.SeekSeconds != 0 || got.StartSegmentNumber != 0 || network.LastRequestedSegment() != 2 {
		t.Fatalf("planned seek changed origin/request progress: %+v requested=%d", got, network.LastRequestedSegment())
	}
	compareCopyFiles(t, base.OutputDir, options.OutputDir)
	// A new lease URL for the same revision can recover the persisted plan.
	fresh := remote.open(t)
	if fresh.URL() == handle.URL() || fresh.Identity() != handle.Identity() {
		t.Fatal("lease fixture did not preserve stable source identity")
	}
	rebuilt := options
	rebuilt.InputPath = fresh.URL()
	reads := remote.reads.Load()
	plan := resolveCopyPlan(remote.ctx, rebuilt)
	if plan == nil || !reflect.DeepEqual(plan.durations, network.copyPlan.durations) || remote.reads.Load() != reads {
		t.Fatal("renewed source did not reuse revision-bound plan locally")
	}
	remote.active.Store(false)
	if resolveCopyPlan(remote.ctx, rebuilt) != nil {
		t.Fatal("revoked source returned cached copy plan")
	}
	if _, err := keyframes.LoadSourceVerified(remote.ctx, admittedKeyframeSource(fresh)); err == nil || errors.Is(err, keyframes.ErrNoIndex) {
		t.Fatalf("revoked cached index: %v", err)
	}
}
func TestAdmittedCopyPlanRejectsChangedRevisionAndScratchEscape(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := keyframeMKV(t, ffmpeg)
	scratch := t.TempDir()
	f := newKeyframeRemoteFixture(t, path, scratch)
	first := f.open(t)
	opts := TranscodeOpts{InputPath: first.URL(), OutputDir: filepath.Join(scratch, "out"), TargetCodecVideo: "copy", SegmentDuration: 2, TotalDuration: 12, KeyframePlaylist: true, Reconstructed: true}
	if err := os.MkdirAll(opts.OutputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := resolveCopyPlan(f.ctx, opts)
	if original == nil {
		t.Fatal("no initial plan")
	}
	recordPath := filepath.Join(opts.OutputDir, copyPlanRecordFile)
	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored copyPlanRecord
	if err := json.Unmarshal(record, &stored); err != nil || stored.SourceIdentity != first.Identity() {
		t.Fatalf("record scope=%+v err=%v", stored, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime().Add(time.Hour), info.ModTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	changed := f.open(t)
	if changed.Identity() == first.Identity() {
		t.Fatal("replacement fixture did not change revision")
	}
	opts.InputPath = changed.URL()
	if resolveCopyPlan(f.ctx, opts) != nil {
		t.Fatal("changed revision reused old planned timeline")
	}
	after, err := os.ReadFile(recordPath)
	if err != nil || !bytes.Equal(record, after) {
		t.Fatal("revision mismatch overwrote established plan")
	}
	outside := filepath.Join(t.TempDir(), "escape.json")
	if err := os.WriteFile(outside, record, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, recordPath); err != nil {
		t.Fatal(err)
	}
	if resolveCopyPlan(f.ctx, opts) != nil {
		t.Fatal("read escaping plan symlink")
	}
}

func TestAdmittedKeyframeReadBudgetDoesNotPoisonCache(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := keyframeMKV(t, ffmpeg)
	f := newKeyframeRemoteFixture(t, path, t.TempDir())
	handle := f.open(t)
	source := admittedKeyframeSource(handle)
	source.OpenReader = func(ctx context.Context) (io.ReaderAt, io.Closer, error) {
		reader, err := handle.ReaderAt(ctx, workerinput.ReaderLimits{MaxBytes: 32})
		if err != nil {
			return nil, nil, err
		}
		return reader, reader, nil
	}
	if _, err := keyframes.LoadSourceVerified(f.ctx, source); !errors.Is(err, workerinput.ErrReaderLimit) || errors.Is(err, keyframes.ErrNoIndex) {
		t.Fatalf("budget error cached as unindexed: %v", err)
	}
	if handle.Validate(f.ctx) != nil {
		t.Fatal("index budget revoked media input")
	}
	idx, err := keyframes.LoadSourceVerified(f.ctx, admittedKeyframeSource(handle))
	if err != nil || len(idx.Keyframes) == 0 {
		t.Fatalf("failed to recover from transient budget: %+v %v", idx, err)
	}
}

func TestColdRemoteReconstructionDoesNotLaunchOrRecordOnTransientIndexFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launch marker uses a POSIX shell")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := keyframeMKV(t, ffmpeg)
	scratch := t.TempDir()
	f := newKeyframeRemoteFixture(t, path, scratch)
	handle := f.open(t)
	opts := TranscodeOpts{InputPath: path, OutputDir: filepath.Join(scratch, "rebuild"), SessionID: "cold-rebuild", SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, TotalDuration: 12, KeyframePlaylist: true, Reconstructed: true, HWAccel: HWAccelNone}
	prior := planCopySegments(t.Context(), opts)
	if prior == nil || len(prior.durations) < 3 {
		t.Fatal("fixture has no prior full VOD plan")
	}
	if err := os.MkdirAll(opts.OutputDir, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(opts.OutputDir, copyPlanRecordFile)
	data, err := json.Marshal(copyPlanRecord{Planned: true, SourceIdentity: handle.Identity(), Durations: prior.durations})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	} // Simulate lost worker scratch after the client received its full playlist.
	marker := filepath.Join(scratch, "launched")
	fake := filepath.Join(scratch, "fake-ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf launched > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	opts.InputPath = handle.URL()
	opts.FFmpegPath = fake
	f.unavailable.Store(true)
	session, startErr := StartTranscode(f.ctx, opts)
	if session != nil {
		<-session.Done()
		t.Cleanup(func() { _ = session.CloseProcess() })
	}
	recorded, readErr := os.ReadFile(record)
	_, launchErr := os.Stat(marker)
	if startErr == nil || errors.Is(startErr, keyframes.ErrNoIndex) || session != nil || !errors.Is(readErr, os.ErrNotExist) || !errors.Is(launchErr, os.ErrNotExist) {
		t.Fatalf("transient reconstruction changed the held playlist: err=%v session=%v record=%s recordErr=%v launched=%v", startErr, session != nil, recorded, readErr, launchErr == nil)
	}
	if f.ctx.Err() != nil || handle.Validate(f.ctx) != nil {
		t.Fatal("transient read must leave caller and source authority live")
	}
	if _, err := os.Stat(record + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconstruction left temporary decision: %v", err)
	}
	// The same active recipe can be retried after the range service recovers.
	f.unavailable.Store(false)
	recovered, err := StartTranscode(f.ctx, opts)
	if err != nil || recovered == nil {
		t.Fatalf("retry after transient failure: %v", err)
	}
	t.Cleanup(func() { _ = recovered.CloseProcess() })
	<-recovered.Done()
	if recovered.copyPlan == nil || !reflect.DeepEqual(recovered.copyPlan.durations, prior.durations) {
		t.Fatal("retry lost the established VOD timeline")
	}
}

func TestColdRemoteReconstructionPropagatesIndependentReaderLimits(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, tc := range []struct {
		name string
		want error
		open func(context.Context, *workerinput.Handle) (*workerinput.RangeReader, error)
	}{
		{name: "byte-budget", want: workerinput.ErrReaderLimit, open: func(ctx context.Context, h *workerinput.Handle) (*workerinput.RangeReader, error) {
			return h.ReaderAt(ctx, workerinput.ReaderLimits{MaxBytes: 32})
		}},
		{name: "reader-deadline", want: context.DeadlineExceeded, open: func(ctx context.Context, h *workerinput.Handle) (*workerinput.RangeReader, error) {
			bounded, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			defer cancel()
			return h.ReaderAt(bounded, workerinput.ReaderLimits{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := keyframeMKV(t, ffmpeg)
			scratch := t.TempDir()
			f := newKeyframeRemoteFixture(t, path, scratch)
			h := f.open(t)
			opts := TranscodeOpts{InputPath: h.URL(), OutputDir: filepath.Join(scratch, "rebuild"), TargetCodecVideo: "copy", SegmentDuration: 2, TotalDuration: 12, KeyframePlaylist: true, Reconstructed: true}
			if err := os.MkdirAll(opts.OutputDir, 0700); err != nil {
				t.Fatal(err)
			}
			source := admittedKeyframeSource(h)
			source.OpenReader = func(ctx context.Context) (io.ReaderAt, io.Closer, error) {
				reader, err := tc.open(ctx, h)
				if err != nil {
					return nil, nil, err
				}
				return reader, reader, nil
			}
			boundedLoad := func(ctx context.Context, _ TranscodeOpts) (keyframes.Index, error) {
				return keyframes.LoadSourceVerified(ctx, source)
			}
			plan, err := resolveCopyPlanResult(f.ctx, opts, boundedLoad)
			if plan != nil || !errors.Is(err, tc.want) || errors.Is(err, keyframes.ErrNoIndex) {
				t.Fatalf("bounded reconstruction plan=%v error=%v", plan, err)
			}
			if f.ctx.Err() != nil || h.Validate(f.ctx) != nil {
				t.Fatal("reader failure changed caller or source lifetime")
			}
			for _, name := range []string{copyPlanRecordFile, copyPlanRecordFile + ".tmp"} {
				if _, err := os.Stat(filepath.Join(opts.OutputDir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("transient rebuild left %s: %v", name, err)
				}
			}
			recovered, err := resolveCopyPlanResult(f.ctx, opts, loadCopyKeyframeIndex)
			if err != nil || recovered == nil {
				t.Fatalf("reconstruction after transient limit plan=%v err=%v", recovered, err)
			}
		})
	}
}

func TestRemoteCopyPlanUnsupportedAndFirstStartFailuresKeepFallback(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, tc := range []struct {
		name                       string
		reconstructed, unsupported bool
	}{
		{name: "unsupported-first", unsupported: true},
		{name: "unsupported-rebuild", unsupported: true, reconstructed: true},
		{name: "budget-first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			if tc.unsupported {
				path = filepath.Join(t.TempDir(), "unindexed.bin")
				if err := os.WriteFile(path, bytes.Repeat([]byte("unindexed"), 32), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				path = keyframeMKV(t, ffmpeg)
			}
			scratch := t.TempDir()
			f := newKeyframeRemoteFixture(t, path, scratch)
			h := f.open(t)
			opts := TranscodeOpts{InputPath: h.URL(), OutputDir: filepath.Join(scratch, "first"), TargetCodecVideo: "copy", SegmentDuration: 2, TotalDuration: 12, KeyframePlaylist: true, Reconstructed: tc.reconstructed}
			if err := os.MkdirAll(opts.OutputDir, 0700); err != nil {
				t.Fatal(err)
			}
			load := loadCopyKeyframeIndex
			if !tc.unsupported {
				source := admittedKeyframeSource(h)
				source.OpenReader = func(ctx context.Context) (io.ReaderAt, io.Closer, error) {
					reader, err := h.ReaderAt(ctx, workerinput.ReaderLimits{MaxBytes: 32})
					if err != nil {
						return nil, nil, err
					}
					return reader, reader, nil
				}
				load = func(ctx context.Context, _ TranscodeOpts) (keyframes.Index, error) {
					return keyframes.LoadSource(ctx, source)
				}
			}
			plan, err := resolveCopyPlanResult(f.ctx, opts, load)
			if err != nil || plan != nil {
				t.Fatalf("fallback plan=%v err=%v", plan, err)
			}
			data, err := os.ReadFile(filepath.Join(opts.OutputDir, copyPlanRecordFile))
			if err != nil {
				t.Fatal(err)
			}
			var record copyPlanRecord
			if err := json.Unmarshal(data, &record); err != nil || record.Planned || record.SourceIdentity != h.Identity() {
				t.Fatalf("fallback record=%+v err=%v", record, err)
			}
		})
	}
	// A local legacy rebuild retains its pre-existing transient-error fallback.
	opts := TranscodeOpts{InputPath: "local-unavailable", OutputDir: t.TempDir(), TargetCodecVideo: "copy", SegmentDuration: 2, TotalDuration: 12, KeyframePlaylist: true, Reconstructed: true}
	load := func(context.Context, TranscodeOpts) (keyframes.Index, error) {
		return keyframes.Index{}, errors.New("legacy index unavailable")
	}
	if plan, err := resolveCopyPlanResult(t.Context(), opts, load); plan != nil || err != nil {
		t.Fatalf("legacy fallback plan=%v err=%v", plan, err)
	}
}

func TestRemoteReconstructionRejectsInvalidPlanAuthorityBeforeProcessStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launch marker and scratch escape use POSIX shell/symlink")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, kind := range []string{"revoked", "changed-revision", "scratch-escape"} {
		t.Run(kind, func(t *testing.T) {
			path := keyframeMKV(t, ffmpeg)
			scratch := t.TempDir()
			f := newKeyframeRemoteFixture(t, path, scratch)
			h := f.open(t)
			opts := TranscodeOpts{InputPath: h.URL(), OutputDir: filepath.Join(scratch, "original"), SessionID: "invalid-plan", SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, TotalDuration: 12, KeyframePlaylist: true, Reconstructed: true, HWAccel: HWAccelNone}
			if err := os.MkdirAll(opts.OutputDir, 0700); err != nil {
				t.Fatal(err)
			}
			if plan, err := resolveCopyPlanResult(f.ctx, opts, loadCopyKeyframeIndex); err != nil || plan == nil {
				t.Fatalf("initial full VOD plan=%v error=%v", plan, err)
			}
			recordPath := filepath.Join(opts.OutputDir, copyPlanRecordFile)
			before, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "revoked":
				f.active.Store(false)
			case "changed-revision":
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				stamp := info.ModTime().Add(time.Hour)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				fresh := f.open(t)
				if fresh.Identity() == h.Identity() {
					t.Fatal("fixture did not change source revision")
				}
				opts.InputPath = fresh.URL()
			case "scratch-escape":
				escape := filepath.Join(scratch, "escaping-output")
				if err := os.Symlink(t.TempDir(), escape); err != nil {
					t.Fatal(err)
				}
				opts.OutputDir = escape
			}
			marker := filepath.Join(scratch, "launched")
			fake := filepath.Join(scratch, "fake-ffmpeg")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf launched > '"+marker+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			opts.FFmpegPath = fake
			session, startErr := StartTranscode(f.ctx, opts)
			if session != nil {
				<-session.Done()
				t.Cleanup(func() { _ = session.CloseProcess() })
			}
			if startErr == nil || session != nil {
				t.Fatalf("invalid %s plan started a process: session=%v err=%v", kind, session != nil, startErr)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid plan launched FFmpeg: %v", err)
			}
			after, err := os.ReadFile(recordPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("invalid plan replaced existing timeline: %s error=%v", after, err)
			}
			if _, err := os.Stat(filepath.Join(opts.OutputDir, copyPlanRecordFile+".tmp")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid plan left temporary record: %v", err)
			}
			if kind == "scratch-escape" {
				if _, err := os.Stat(filepath.Join(opts.OutputDir, copyPlanRecordFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid scratch gained a fallback decision: %v", err)
				}
			}
		})
	}
}
