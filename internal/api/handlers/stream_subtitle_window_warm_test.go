package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/remotestream"
)

const warmSubtitleVTT = "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n"

// newVirtualSubtitleWindowFixture builds a StreamHandler rooted at dir that
// resolves a virtual source through a live relay and extracts text subtitles
// through a fake ffmpeg at dir/ffmpeg. The caller writes the ffmpeg script and
// gets back the handler, the bound session, the virtual file, and the pinned
// source URI.
func newVirtualSubtitleWindowFixture(t *testing.T, dir, ffmpegScript string) (*StreamHandler, *playback.Session, *models.MediaFile, string) {
	t.Helper()
	writeExecutableScript(t, filepath.Join(dir, "ffmpeg"), ffmpegScript)
	// A detached warm validates the layout of its own relay registration
	// before extracting, so the default probe must agree with the fixture row.
	// Tests that change file.SubtitleTracks install a matching probe with
	// writeSubtitleLayoutProbe.
	writeSubtitleLayoutProbe(t, dir, models.SubtitleTrack{Index: 0, Codec: "subrip"})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("provider-media"))
	}))
	t.Cleanup(upstream.Close)

	handler := NewStreamHandler(nil, nil)
	handler.PlaybackConfig = playbackTestConfig(filepath.Join(dir, "ffmpeg"), dir)
	handler.SubtitleCache = playback.NewSubtitleCache(func() string { return dir })
	handler.RemoteStreamRelay = remotestream.NewRelay()
	t.Cleanup(func() { _ = handler.RemoteStreamRelay.Close(context.Background()) })
	handler.AllowPrivateStreams = func(int) bool { return true }
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		return ResolvedVirtualMedia{URL: upstream.URL + "/video.mkv"}, nil
	})

	virtualURI := "virtual://movie/tt-window?result=cand-window"
	file := &models.MediaFile{
		ID: 91, ContentID: "movie-window", FilePath: virtualURI,
		SubtitleTracks:             []models.SubtitleTrack{{Index: 0, Codec: "subrip"}},
		VirtualOwnerInstallationID: 5,
	}
	session := &playback.Session{
		ID: "sess-window", UserID: 1, ProfileID: "profile-1", MediaFileID: file.ID,
		VirtualSourceURI: virtualURI, VirtualSubtitleTracks: file.SubtitleTracks,
		// Full-track virtual warms are opportunistic and only run while the
		// viewer is paused; the window-miss tests exercise that paused path.
		IsPaused: true,
	}
	// The window-miss warm is detached; wait for it to settle before the
	// fixture's temp dir is removed, or the warm keeps writing into a directory
	// t.TempDir is tearing down. Registered after the relay-close cleanup so
	// LIFO runs this wait first — the warm needs the relay it resolved through.
	t.Cleanup(handler.waitForBackgroundSubtitleWarms)
	return handler, session, file, virtualURI
}

// writeSubtitleLayoutProbe installs an ffprobe that reports the given subtitle
// layout for any input. The detached warm probes its own relay registration
// before extracting, so a test whose row differs from the fixture default must
// make the probe agree or the warm abandons as unvalidated.
func writeSubtitleLayoutProbe(t *testing.T, dir string, tracks ...models.SubtitleTrack) {
	t.Helper()
	type probedStream struct {
		Index       int               `json:"index"`
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	}
	streams := make([]probedStream, 0, len(tracks))
	for _, track := range tracks {
		streams = append(streams, probedStream{
			Index:     track.Index,
			CodecName: track.Codec,
			CodecType: "subtitle",
			Tags:      map[string]string{"language": track.Language},
			Disposition: map[string]int{
				"default": boolFlag(track.Default),
				"forced":  boolFlag(track.Forced),
			},
		})
	}
	payload, err := json.Marshal(map[string]any{"streams": streams})
	if err != nil {
		t.Fatal(err)
	}
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\nprintf '%s' '"+string(payload)+"'\n")
}

func boolFlag(set bool) int {
	if set {
		return 1
	}
	return 0
}

// supWindowPagePrintf writes one complete PGS display set through the shell: a
// PCS segment (type 0x16, 4-byte payload) immediately followed by an END
// segment (type 0x80, no payload), 30 bytes in all. It is the shell form of
// pgsWindowPage in the playback tests. Only a payload holding a complete
// display set is cacheable, so a stub that wrote the "PG" magic alone was
// served but never stored, and every repeat re-extracted the page.
const supWindowPagePrintf = `printf 'PG\000\000\000\000\000\000\000\000\026\000\004\000\001\002\003PG\000\000\000\000\000\000\000\000\200\000\000'`

// pgsWindowStubScript is the ffmpeg stub for the bitmap window fixtures. It
// logs its arguments to argsLog when one is given and writes a real .sup page.
func pgsWindowStubScript(argsLog string) string {
	script := "#!/bin/sh\n"
	if argsLog != "" {
		script += "printf '%s\\n' \"$*\" >> '" + argsLog + "'\n"
	}
	return script + supWindowPagePrintf + "\n"
}

// warmArgsLogScript logs every invocation and distinguishes the full-track
// warm (no -ss) from the windowed extract. The warm blocks on gate so the
// coalescing test can hold it in flight.
func warmArgsLogScript(argsLog, gate string) string {
	return "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"case \"$*\" in\n" +
		"  *-ss*)\n    cat <<'VTT'\n" + warmSubtitleVTT + "VTT\n    ;;\n" +
		"  *)\n    while [ ! -f '" + gate + "' ]; do sleep 0.01; done\n    cat <<'VTT'\n" + warmSubtitleVTT + "VTT\n    ;;\n" +
		"esac\n"
}

// warmPidLogScript is warmArgsLogScript plus a pid log: a full-track warm
// records its own process id before blocking on the gate, so a test can tell a
// resumed warm (phase 1 yields the pipe to an admitted page and resumes) from
// a second concurrent warm for the same identity.
func warmPidLogScript(argsLog, gate, pidLog string) string {
	return "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"case \"$*\" in\n" +
		"  *-ss*)\n    cat <<'VTT'\n" + warmSubtitleVTT + "VTT\n    ;;\n" +
		"  *)\n    printf '%s\\n' \"$$\" >> '" + pidLog + "'\n" +
		"    while [ ! -f '" + gate + "' ]; do sleep 0.01; done\n    cat <<'VTT'\n" + warmSubtitleVTT + "VTT\n    ;;\n" +
		"esac\n"
}

// readWarmProcesses returns the process ids of the full-track warms logged so
// far. A writer appends one pid per line, so a torn trailing line is skipped.
func readWarmProcesses(t *testing.T, pidLog string) []int {
	t.Helper()
	data, err := os.ReadFile(pidLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// warmLogBuffer collects the cache's warm log lines. The detached warm logs
// from its own goroutine while the test reads, so the buffer is guarded.
type warmLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *warmLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *warmLogBuffer) count(marker string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), marker)
}

func (b *warmLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForWarmProcesses waits until want full-track warm processes have logged
// their pid, then returns every pid seen. Reaching two means the admitted page
// yielded the first warm and it resumed, so the two processes are one logical
// warm rather than a duplicated fill.
func waitForWarmProcesses(t *testing.T, pidLog string, want int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pids := readWarmProcesses(t, pidLog); len(pids) >= want {
			return pids
		}
		//nolint:forbidigo // Polls the warm pid log: observable state the fake ffmpeg appends to, not a timing guess.
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("full-track warm processes = %d, want %d: the admitted page must yield the first warm and it must resume",
		len(readWarmProcesses(t, pidLog)), want)
	return nil
}

// liveProcessCount reports how many of pids are still running. The killed
// extract of a yielded warm is waited on before the resume starts, so its pid
// is already gone here and only a concurrent duplicate would still be alive.
func liveProcessCount(pids []int) int {
	live := 0
	for _, pid := range pids {
		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := p.Signal(syscall.Signal(0)); err == nil {
			live++
		}
	}
	return live
}

func ffmpegLogLines(t *testing.T, argsLog string) (window, warm int) {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	if os.IsNotExist(err) {
		return 0, 0
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "-ss") {
			window++
		} else {
			warm++
		}
	}
	return window, warm
}

func waitForWarmInvocation(t *testing.T, argsLog string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, warm := ffmpegLogLines(t, argsLog); warm > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("detached full-track warm never started")
}

func waitForCommittedTextEntry(t *testing.T, c *playback.SubtitleCache, identity string, trackIndex int, codec string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.HasCommittedTextEntry("unused", identity, trackIndex, codec, "") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("warm never committed a %s entry under identity %q", codec, identity)
}

// A windowed virtual text miss must stream its own window and then start
// exactly one logical detached full-track warm under the serve identity. A
// second window miss while that warm is in flight must not start a second fill.
//
// One logical warm can spawn two ffmpeg processes: an admitted page yields the
// running warm (phase 1 cancels its extract with ErrSubtitleWarmCanceled and
// resumes it once the page settles), so the process count is not the warm
// count. The assertions below count logical warms — a resumed warm is the same
// run — and prove the two processes never overlapped.
func TestVirtualTextWindowMissStartsOneLogicalWarmWithServeIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	pidLog := filepath.Join(dir, "ffmpeg.warm.pids")
	gate := filepath.Join(dir, "warm.gate")
	// The cache logs one "warm finished" per committed logical fill, so the log
	// is where a second fill would show up even when relay admission sheds its
	// duplicate before ffmpeg runs. Start lines are not used: a preempted fill
	// may be replaced by another admitted warm for the same key.
	warmLogs := &warmLogBuffer{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(warmLogs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	// Registered before the fixture, so LIFO restores the logger after the
	// fixture has waited for its detached warms to settle.
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, warmPidLogScript(argsLog, gate, pidLog))
	// Always release the warm, even if an assertion fails, so the blocked fake
	// ffmpeg cannot outlive the test. Registered after the fixture's wait
	// cleanup so LIFO releases the gate before the wait blocks on it.
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("go"), 0o644) })

	serve := func(position int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/subtitle?position="+strconv.Itoa(position)+"&duration=600", nil)
		rec := httptest.NewRecorder()
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
		return rec
	}

	if rec := serve(600); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("first window: status=%d body=%q", rec.Code, rec.Body.String())
	}
	// The first window request started its detached warm; wait until it holds
	// the in-flight fill before issuing the second miss. A different window
	// avoids the exact-window last-good cache and exercises the warm path.
	waitForWarmInvocation(t, argsLog)
	// The second serve kills this process. Wait for its pid too: a kill between
	// the fake ffmpeg's args and pid writes would leave only the resume's pid,
	// and the two-process assertion below would fail on the missing first one.
	waitForWarmProcesses(t, pidLog, 1)

	if rec := serve(601); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("second window: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// The second miss was served from an admitted page, which yielded the
	// warm. Wait for its resume, so both run ids are recorded and the second
	// one is known to belong to the same logical warm.
	warmPids := waitForWarmProcesses(t, pidLog, 2)
	if live := liveProcessCount(warmPids); live != 1 {
		t.Fatalf("concurrent full-track warms = %d, want 1: a resumed warm is one logical run", live)
	}

	// Release the warm and wait for the entry it commits.
	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, 0)
	waitForCommittedTextEntry(t, handler.SubtitleCache, identity, 0, "subrip")
	// Wait for the warm goroutine to log its outcome. The commit lands before
	// the "warm finished" line, so the cache entry alone is not a safe signal.
	handler.waitForBackgroundSubtitleWarms()

	window, warm := ffmpegLogLines(t, argsLog)
	if window != 2 {
		t.Fatalf("windowed extracts = %d, want 2", window)
	}
	// One logical fill commits. Which invocation owns it depends on the fill
	// race the admitted page creates: the preempted warm may resume, or the
	// second serve's coalescing warm may take the key after the first fill is
	// discarded. Counting "warm started" would race that ownership; the
	// committed fill is stable, and any second concurrent read still trips the
	// liveness check above.
	if finished := warmLogs.count("subtitle cache warm finished"); finished != 1 {
		t.Fatalf("committed full-track warms = %d, want exactly 1: the second window miss must not start a second fill\n%s",
			finished, warmLogs.String())
	}
	// The single logical fill runs the killed extract and then one replacement
	// (the warm's resume, or the second serve's warm when it won the key).
	if warm != 2 {
		t.Fatalf("full-track ffmpeg runs = %d, want 2 (one logical warm: killed extract + one replacement)", warm)
	}
}

// A repeated identical virtual window must serve the last-good slice without a
// second ffmpeg run. This is the client retry pattern the provider outage
// produces; the detached full-track warm is unrelated and stays coalesced.
func TestVirtualTextWindowRepeatServesLastGoodWithoutExtract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	gate := filepath.Join(dir, "warm.gate")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, warmArgsLogScript(argsLog, gate))
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("go"), 0o644) })

	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=600", nil)
		rec := httptest.NewRecorder()
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
		return rec
	}

	for attempt := 0; attempt < 2; attempt++ {
		rec := serve()
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
			t.Fatalf("attempt %d: status=%d body=%q", attempt, rec.Code, rec.Body.String())
		}
	}
	if window, _ := ffmpegLogLines(t, argsLog); window != 1 {
		t.Fatalf("windowed extracts = %d, want 1 for a repeated window", window)
	}
}

// A committed full-track artifact makes the row-vs-evidence drift probe
// unnecessary: the windowed request must skip the probe and serve its window
// from the cached artifact.
func TestVirtualTextWindowCommittedEntrySkipsDriftProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.log")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	// The drift probe derives ffprobe from the ffmpeg path; install one that
	// records any probe invocation. It reports an empty live layout, so if the
	// probe ran at all the test would still see its log line.
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\necho probe >> '"+probeLog+"'\nprintf '{\"streams\":[]}'\n")

	identity := virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, 0)
	done := handler.SubtitleCache.WarmTrackInBackground(playback.StreamExtractOpts{
		InputPath:     "unused",
		CacheIdentity: identity,
		TrackIndex:    0,
		SourceCodec:   "subrip",
		FFmpegPath:    filepath.Join(dir, "ffmpeg"),
	}, playback.StreamExtractSubtitle, "")
	<-done
	if !handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("pre-warm did not commit the serve identity")
	}
	if err := os.Remove(argsLog); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=600", nil)
	rec := httptest.NewRecorder()
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, true, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("window with committed entry: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := countLogLines(t, probeLog); got != 0 {
		t.Fatalf("drift probe ran %d times despite a committed artifact, want 0", got)
	}
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.TrimSpace(string(data))
	if !strings.Contains(args, "subtitle-cache") {
		t.Fatalf("windowed extract did not read the cached artifact: %s", args)
	}
	if !strings.Contains(args, "-ss") {
		t.Fatalf("windowed extract lost its window: %s", args)
	}
}

// Adjacent windows on the same pinned release share the drift-probe verdict:
// the first request probes, the next reuses the mapping instead of paying the
// ffprobe tax again before it can extract. The warm is made to fail so no
// committed artifact can short-circuit the test through the other skip path.
func TestVirtualSubtitleDriftProbeReusedWithinTTL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	probeLog := filepath.Join(dir, "ffprobe.log")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *-ss*) cat <<'VTT'\n" + warmSubtitleVTT + "VTT\n    ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, script)
	// Only serve-path probes belong in probeLog. Parking an empty session
	// manager makes the detached warm's session lookup fail, so no warm starts
	// (and probes) behind the assertions.
	handler.sessionMgr = playback.NewSessionManager(0, 0)
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\necho probe >> '"+probeLog+"'\n"+
			"printf '%s' '{\"streams\":[{\"index\":0,\"codec_name\":\"subrip\",\"codec_type\":\"subtitle\",\"tags\":{}}]}'\n")

	serve := func(position int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet,
			"/subtitle?position="+strconv.Itoa(position)+"&duration=120", nil)
		rec := httptest.NewRecorder()
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, true, "vtt")
		return rec
	}
	for _, position := range []int{100, 220} {
		if rec := serve(position); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
			t.Fatalf("window %d: status=%d body=%q", position, rec.Code, rec.Body.String())
		}
	}
	if got := countLogLines(t, probeLog); got != 1 {
		t.Fatalf("drift probes = %d, want 1 (the second window reuses the verdict)", got)
	}
}

// The drift-probe memo must cover a steady viewer's page cadence: the web
// player asks for the next 120 s window with a 60 s prefetch, so a TTL shorter
// than a few page cycles re-probes the live layout (seconds of work) on every
// second or third page even though the pinned release has not changed. Three
// consecutive cadence steps must reuse one verdict. The identity re-key that a
// layout change causes is covered by
// TestVirtualSubtitleLayoutReuseMissesAfterInventoryChanges.
func TestVirtualSubtitleLayoutReuseCoversPageCadence(t *testing.T) {
	if virtualSubtitleLayoutCacheTTL < 5*time.Minute {
		t.Fatalf("layout probe TTL = %v, want at least 5m to cover a 120s page cadence", virtualSubtitleLayoutCacheTTL)
	}
	handler := &StreamHandler{}
	opts := &playback.StreamExtractOpts{CacheIdentity: "identity-cadence", TrackIndex: 0, SourceCodec: "subrip"}
	verifiedAt := time.Now()
	handler.rememberVirtualSubtitleLayout(
		virtualSubtitleLayoutKey(opts.CacheIdentity, opts.TrackIndex), *opts, verifiedAt)

	for page := 1; page <= 3; page++ {
		at := verifiedAt.Add(time.Duration(page) * 120 * time.Second)
		if _, ok := handler.reuseVirtualSubtitleLayout(opts, at); !ok {
			t.Fatalf("page %d at +%s re-probed inside the memo window", page, at.Sub(verifiedAt))
		}
	}
}

// A verdict older than the TTL is dropped, so a long playback revalidates the
// live layout instead of trusting a mapping forever.
func TestVirtualSubtitleLayoutReuseExpires(t *testing.T) {
	handler := &StreamHandler{}
	opts := &playback.StreamExtractOpts{CacheIdentity: "identity-a", TrackIndex: 0, SourceCodec: "subrip"}
	verifiedAt := time.Now()
	handler.rememberVirtualSubtitleLayout(virtualSubtitleLayoutKey(opts.CacheIdentity, opts.TrackIndex),
		playback.StreamExtractOpts{CacheIdentity: opts.CacheIdentity, TrackIndex: 2, SourceCodec: "ass"}, verifiedAt)

	verdict, ok := handler.reuseVirtualSubtitleLayout(opts, verifiedAt.Add(virtualSubtitleLayoutCacheTTL-time.Second))
	if !ok || verdict.trackIndex != 2 || verdict.sourceCodec != "ass" {
		t.Fatalf("verdict inside TTL = %+v ok=%v, want the remapped ordinal", verdict, ok)
	}
	if _, ok := handler.reuseVirtualSubtitleLayout(opts, verifiedAt.Add(virtualSubtitleLayoutCacheTTL+time.Second)); ok {
		t.Fatal("verdict must expire after the TTL")
	}
}

// The probe memo is keyed on the pre-probe cache identity, and that identity
// folds in the row's subtitle inventory token. A re-probe that establishes a
// different layout therefore re-keys the identity, so a verdict recorded for
// the previous inventory must not be reused inside the TTL.
func TestVirtualSubtitleLayoutReuseMissesAfterInventoryChanges(t *testing.T) {
	handler := &StreamHandler{}
	uri := "virtual://movie/tt-inventory?result=cand-inventory"
	file := &models.MediaFile{
		ID:             7,
		SubtitleTracks: []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "eng"}},
	}
	verifiedAt := time.Now()
	before := playback.StreamExtractOpts{
		CacheIdentity: virtualSubtitleCacheIdentity(file, uri, 0),
		TrackIndex:    0,
		SourceCodec:   "subrip",
	}
	handler.rememberVirtualSubtitleLayout(
		virtualSubtitleLayoutKey(before.CacheIdentity, before.TrackIndex), before, verifiedAt)
	if _, ok := handler.reuseVirtualSubtitleLayout(&before, verifiedAt.Add(time.Second)); !ok {
		t.Fatal("verdict was not reused for an unchanged inventory inside the TTL")
	}

	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 0, Codec: "subrip", Language: "eng"},
		{Index: 1, Codec: "ass", Language: "fre"},
	}
	after := playback.StreamExtractOpts{
		CacheIdentity: virtualSubtitleCacheIdentity(file, uri, 0),
		TrackIndex:    0,
		SourceCodec:   "subrip",
	}
	if after.CacheIdentity == before.CacheIdentity {
		t.Fatal("inventory token did not re-key the cache identity")
	}
	if _, ok := handler.reuseVirtualSubtitleLayout(&after, verifiedAt.Add(time.Second)); ok {
		t.Fatal("verdict recorded for the previous inventory was reused after the layout changed")
	}
}

// A probe verdict is scoped to the exact identity it was established for. A
// drift remap re-keys the request onto the effective ordinal, and a candidate
// rotation changes the pinned result, so neither may reuse a verdict recorded
// for the plan-ordinal identity. (Inventory changes are covered above; this
// pins the other two re-key events a rotation produces.)
func TestVirtualSubtitleLayoutVerdictNotReusedAcrossRemapOrRotation(t *testing.T) {
	handler := &StreamHandler{}
	uri := "virtual://movie/tt-remap-verdict?result=cand-a"
	file := &models.MediaFile{ID: 11, SubtitleTracks: []models.SubtitleTrack{
		{Index: 0, Codec: "subrip", Language: "eng"},
		{Index: 1, Codec: "subrip", Language: "fre"},
	}}
	verifiedAt := time.Now()
	plan := playback.StreamExtractOpts{
		CacheIdentity: virtualSubtitleCacheIdentity(file, uri, 0),
		TrackIndex:    0,
		SourceCodec:   "subrip",
	}
	// The plan-ordinal probe established that this request's track now lives at
	// the other ordinal.
	handler.rememberVirtualSubtitleLayout(
		virtualSubtitleLayoutKey(plan.CacheIdentity, plan.TrackIndex),
		playback.StreamExtractOpts{CacheIdentity: plan.CacheIdentity, TrackIndex: 1, SourceCodec: "subrip"},
		verifiedAt,
	)
	if verdict, ok := handler.reuseVirtualSubtitleLayout(&plan, verifiedAt.Add(time.Second)); !ok || verdict.trackIndex != 1 {
		t.Fatalf("plan request did not reuse its own verdict: %+v ok=%v", verdict, ok)
	}

	remapped := playback.StreamExtractOpts{
		CacheIdentity: virtualSubtitleCacheIdentity(file, uri, 1),
		TrackIndex:    1,
		SourceCodec:   "subrip",
	}
	if _, ok := handler.reuseVirtualSubtitleLayout(&remapped, verifiedAt.Add(time.Second)); ok {
		t.Fatal("post-remap identity reused the plan-ordinal verdict")
	}

	rotated := playback.StreamExtractOpts{
		CacheIdentity: virtualSubtitleCacheIdentity(file, "virtual://movie/tt-remap-verdict?result=cand-b", 0),
		TrackIndex:    0,
		SourceCodec:   "subrip",
	}
	if rotated.CacheIdentity == plan.CacheIdentity {
		t.Fatal("candidate rotation did not re-key the cache identity")
	}
	if _, ok := handler.reuseVirtualSubtitleLayout(&rotated, verifiedAt.Add(time.Second)); ok {
		t.Fatal("rotated candidate reused the previous candidate's verdict")
	}
}

// A text warm survives a resume but still stops when the playhead leaves the
// position the warm was started at, or when the session ends. The abort itself
// is ErrSubtitleWarmCanceled, which the cache treats as an intentional
// abandonment rather than a source failure.
func TestVirtualSubtitleWarmCancelReason(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 91, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := &StreamHandler{sessionMgr: manager}
	anchorAt := time.Now()
	const anchor = 100.0
	if err := manager.UpdateProgress(session.ID, anchor, false); err != nil {
		t.Fatal(err)
	}

	if reason := handler.virtualSubtitleWarmCancelReason(session, anchor, anchorAt, anchorAt.Add(30*time.Second)); reason != "" {
		t.Fatalf("normal playback canceled the warm: %q", reason)
	}
	if err := manager.UpdateProgress(session.ID, anchor+500, false); err != nil {
		t.Fatal(err)
	}
	if reason := handler.virtualSubtitleWarmCancelReason(session, anchor, anchorAt, anchorAt.Add(2*time.Second)); reason != "seek forward" {
		t.Fatalf("forward-seek reason = %q", reason)
	}
	if err := manager.UpdateProgress(session.ID, anchor-60, false); err != nil {
		t.Fatal(err)
	}
	if reason := handler.virtualSubtitleWarmCancelReason(session, anchor, anchorAt, anchorAt.Add(3*time.Second)); reason != "seek backward" {
		t.Fatalf("backward-seek reason = %q", reason)
	}
	if err := manager.StopSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if reason := handler.virtualSubtitleWarmCancelReason(session, anchor, anchorAt, anchorAt.Add(4*time.Second)); reason != "session ended" {
		t.Fatalf("session-end reason = %q", reason)
	}
}

// The window-miss warm keys on the exact ordinal the serve path hands it, so
// after a drift remap the post-remap identity is what gets populated and the
// remapped serve path finds it. The start-path warm still keys on the plan
// ordinal; the serve-path warm is authoritative for the remap case, so an
// orphaned plan-ordinal entry self-heals on the next window.
func TestVirtualTextWindowWarmKeysOnServeOrdinalAfterRemap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")

	const remappedOrdinal = 1
	// This is the identity the remapped serve path computes; the warm must
	// commit under it.
	serveIdentity := virtualSubtitleCacheIdentity(file, virtualURI, remappedOrdinal)
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   serveIdentity,
		TrackIndex:      remappedOrdinal,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, true, "")
	waitForCommittedTextEntry(t, handler.SubtitleCache, serveIdentity, remappedOrdinal, "subrip")

	planIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	if handler.SubtitleCache.HasCommittedTextEntry("unused", planIdentity, 0, "subrip", "") {
		t.Fatal("warm must not commit under the orphaned plan ordinal")
	}
}

// The warm resolves its own relay registration, so it validates that
// registration before extracting. When the live release rotated, it remaps
// onto the same-class live track and extracts that stream. The fill was admitted
// under the plan-ordinal identity the serve handed it, so the warm discards that
// fill and re-keys onto the identity its own probe established: the artifact
// commits under the same key the remapped serve path computes, and a concurrent
// page for the same logical track finds it instead of demuxing a second time.
func TestVirtualSubtitleWarmRemapsOntoLiveLayoutBeforeCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "eng"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	// The live release rotated: two subrip tracks, and the planned English one
	// is now the second.
	writeSubtitleLayoutProbe(t, dir,
		models.SubtitleTrack{Index: 3, Codec: "subrip", Language: "jpn"},
		models.SubtitleTrack{Index: 7, Codec: "subrip", Language: "eng"},
	)

	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   virtualSubtitleCacheIdentity(file, virtualURI, 0),
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, true, "")

	const remappedOrdinal = 1
	serveIdentity := virtualSubtitleCacheIdentity(file, virtualURI, remappedOrdinal)
	waitForCommittedTextEntry(t, handler.SubtitleCache, serveIdentity, remappedOrdinal, "subrip")
	planIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	if handler.SubtitleCache.HasCommittedTextEntry("unused", planIdentity, 0, "subrip", "") {
		t.Fatal("warm committed under the plan ordinal instead of the identity its probe established")
	}
	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	warmArgs := strings.TrimSpace(string(data))
	if !strings.Contains(warmArgs, "-map 0:s:1") {
		t.Fatalf("warm args = %q, want the remapped 0:s:1", warmArgs)
	}
	// The remapped serve path must find the committed artifact with no second
	// source demux: the warm key and the serve key are the same key. A windowed
	// serve still runs the converter, but only against the local artifact.
	var extracts atomic.Int32
	var servedFromSource atomic.Bool
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=600", nil)
	result, serveErr := handler.SubtitleCache.ServeExtractWithResult(rec, req, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   serveIdentity,
		TrackIndex:      remappedOrdinal,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, func(_ context.Context, o playback.StreamExtractOpts) error {
		extracts.Add(1)
		if o.InputIsExtractedText == "" && !strings.Contains(o.InputPath, "subtitle-cache") {
			servedFromSource.Store(true)
		}
		_, err := o.Writer.Write([]byte(warmSubtitleVTT))
		return err
	})
	if serveErr != nil {
		t.Fatalf("serve on the warm identity: %v", serveErr)
	}
	if !result.ServedCommittedArtifact || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("remapped serve did not use the warm artifact: result=%+v status=%d body=%q",
			result, rec.Code, rec.Body.String())
	}
	if servedFromSource.Load() {
		t.Fatalf("remapped serve demuxed the source (%d extract calls) despite the warm artifact", extracts.Load())
	}
}

// A paging request that races an inventory rotation must not replay the
// previous inventory's window slice and must not warm under the old identity.
// The window cache, the committed artifact, and the warm all key on the
// identity, so the rotated row re-keys them together even for the exact same
// source-time range.
func TestVirtualTextWindowPagingRekeysAfterInventoryRotation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	rotated := filepath.Join(dir, "rotated.marker")
	// The fake extractor labels its output by the rotation marker, so a stale
	// window slice from before the rotation is visible in the response body.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"if [ -f '" + rotated + "' ]; then\ncat <<'VTT'\nWEBVTT\n\n00:00:00.000 --> 00:00:01.000\nROTATED\nVTT\n" +
		"else\ncat <<'VTT'\nWEBVTT\n\n00:00:00.000 --> 00:00:01.000\nPLAN\nVTT\nfi\n"
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir, script)
	planTracks := file.SubtitleTracks
	session.VirtualSubtitleEvidenceSet = true
	session.VirtualSubtitleEvidenceFileID = file.ID
	session.VirtualSubtitleEvidenceURI = virtualURI
	session.VirtualSubtitleTracks = planTracks

	serve := func(position int, drift bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle?position="+strconv.Itoa(position)+"&duration=600", nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, drift, "vtt")
		return rec
	}

	// Page 1 is evidence-bound: the row still matches the carried inventory, so
	// it serves and warms under the plan identity.
	first := serve(600, false)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "PLAN") {
		t.Fatalf("first page: status=%d body=%q", first.Code, first.Body.String())
	}
	handler.waitForBackgroundSubtitleWarms()
	planIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	if !handler.SubtitleCache.HasCommittedTextEntry("unused", planIdentity, 0, "subrip", "") {
		t.Fatal("first page did not warm under the plan identity")
	}

	// The catalog row rotates its inventory (a second track appears) and the
	// live release matches the new row, while the session still carries the
	// plan-time inventory. The next page must treat the row as drifted.
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 0, Codec: "subrip"},
		{Index: 1, Codec: "ass", Language: "fre"},
	}
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)
	if err := os.WriteFile(rotated, []byte("rotated"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := serve(600, true)
	if second.Code != http.StatusOK {
		t.Fatalf("second page: status=%d body=%q", second.Code, second.Body.String())
	}
	if strings.Contains(second.Body.String(), "PLAN") {
		t.Fatalf("second page replayed the pre-rotation window: body=%q", second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "ROTATED") {
		t.Fatalf("second page did not read the rotated release: body=%q", second.Body.String())
	}
	rotatedIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	if rotatedIdentity == planIdentity {
		t.Fatal("inventory rotation did not re-key the cache identity")
	}
	if window, _ := ffmpegLogLines(t, argsLog); window != 2 {
		t.Fatalf("windowed extracts = %d, want 2: the rotated page must not reuse the pre-rotation window", window)
	}
	handler.waitForBackgroundSubtitleWarms()
	if !handler.SubtitleCache.HasCommittedTextEntry("unused", rotatedIdentity, 0, "subrip", "") {
		t.Fatal("rotated page did not warm under the rotated identity")
	}
	if !handler.SubtitleCache.HasCommittedTextEntry("unused", planIdentity, 0, "subrip", "") {
		t.Fatal("rotation discarded the pre-rotation artifact instead of leaving it under its own key")
	}
}

// A live layout with no same-class track cannot be remapped, so the warm
// abandons before extracting: nothing is committed, and because that is an
// intentional abandonment rather than a source failure, the failure cooldown
// stays clear and a later warm can still commit.
func TestVirtualSubtitleWarmAbandonsUnmatchedLayoutWithoutCooldown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	writeSubtitleLayoutProbe(t, dir, models.SubtitleTrack{Index: 0, Codec: "hdmv_pgs_subtitle"})

	opts := playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   virtualSubtitleCacheIdentity(file, virtualURI, 0),
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, opts, nil, true, "")
	handler.waitForBackgroundSubtitleWarms()

	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("warm extracted from an unmatched live layout: %v", err)
	}
	if handler.SubtitleCache.HasCommittedTextEntry("unused", opts.CacheIdentity, 0, "subrip", "") {
		t.Fatal("warm committed an entry it never validated")
	}

	// A matching probe on the next attempt proves the abandonment did not arm
	// the per-identity failure cooldown.
	writeSubtitleLayoutProbe(t, dir, models.SubtitleTrack{Index: 0, Codec: "subrip"})
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, opts, nil, true, "")
	waitForCommittedTextEntry(t, handler.SubtitleCache, opts.CacheIdentity, 0, "subrip")
}

// A non-virtual handler (virtualActive false) must not start any warm: local
// sources keep the cache's own serve-path warming.
func TestVirtualTextWindowWarmSkipsNonVirtual(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\n")
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)

	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     100,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, false, "")

	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("no warm should run for a non-virtual request: %v", err)
	}
}

// A playing session may start one detached text warm. Without it, every
// window on a large virtual source re-demuxes the relay input and the
// full-track artifact never appears while the viewer keeps watching, so
// rendering stops when the client's lookahead runs out. Coalescing through
// WarmTrackInBackground still keeps it single-flighted.
func TestVirtualTextWindowWarmStartsDuringPlayback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	session.IsPaused = false
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)

	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     100,
		DurationSeconds: 120,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, true, "")

	waitForCommittedTextEntry(t, handler.SubtitleCache, identity, 0, "subrip")
}

// The paused-only admission is for the full-track bitmap warm and must stay for
// it: a whole .sup is tens of megabytes demuxed from the whole container, and it
// is never the page playback needs next. Only a bounded bitmap page routes to the
// progressive fill while playing (TestVirtualBitmapPageMissStartsProgressiveFillWhilePlaying);
// the full-track shape still waits for a pause.
func TestVirtualPGSFullTrackWarmSkipsActivePlayback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	session.IsPaused = false
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)

	handler.warmVirtualSubtitle(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "hdmv_pgs_subtitle",
		SeekSeconds:     100,
		DurationSeconds: 120,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, true, "", false, false, false)

	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("no full-track PGS warm should run while the session is playing: %v", err)
	}
}

// A virtual PGS whole-track fetch must be bounded to an implicit window and
// advertise its range, then warm the full .sup so a repeat can scan the cached
// artifact. This is the live regression: the whole-track .sup exceeded the
// client deadline, the partial fill was discarded on disconnect, and every
// repeat paid a fresh full remote demux while an uncommitted warm contended.
func TestVirtualPGSWholeTrackFetchServesImplicitWindowThenWarms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	gate := filepath.Join(dir, "warm.gate")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir, warmArgsLogScript(argsLog, gate))
	// Release the warm before the fixture's wait cleanup blocks on it, even if
	// an assertion fails and the gate is never opened inline.
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("go"), 0o644) })
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("PGS whole-track fetch = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-120.000" {
		t.Fatalf("PGS windowed coverage header = %q, want the player-sized page 0.000-120.000", got)
	}

	waitForWarmInvocation(t, argsLog)
	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	waitForCommittedEntry(t, handler.SubtitleCache, identity, 0, "hdmv_pgs_subtitle")

	window, warm := ffmpegLogLines(t, argsLog)
	if window != 1 {
		t.Fatalf("windowed PGS extracts = %d, want exactly 1", window)
	}
	if warm != 1 {
		t.Fatalf("full-track PGS warms = %d, want exactly 1", warm)
	}
}

// A cold whole-track ASS fetch against a large virtual source must be bounded
// to an implicit self-contained ASS window instead of demuxing the whole
// container, and advertise the range.
func TestVirtualImplicitWindowAppliesToASS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "ass")
	if rec.Code != http.StatusOK {
		t.Fatalf("ASS whole-track fetch = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-120.000" {
		t.Fatalf("ASS windowed coverage header = %q, want 0.000-120.000", got)
	}
	windowLine := windowedFFmpegArgs(t, argsLog)
	for _, want := range []string{"-ss 0.000", "-to 120.000", "-copyts", "-c:s copy", "-f ass pipe:1"} {
		if !strings.Contains(windowLine, want) {
			t.Fatalf("implicit ASS window args %q missing %q", windowLine, want)
		}
	}
}

// Once a full-track .sup is committed, a repeat whole-track PGS fetch must
// serve it whole from the cache with no ffmpeg and no coverage header — the
// warm paying off instead of re-demuxing the source.
func TestVirtualPGSWholeTrackFetchServesCommittedWholeTrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	done := handler.SubtitleCache.WarmTrackInBackground(playback.StreamExtractOpts{
		InputPath:     "unused",
		CacheIdentity: identity,
		TrackIndex:    0,
		SourceCodec:   "hdmv_pgs_subtitle",
		FFmpegPath:    filepath.Join(dir, "ffmpeg"),
	}, playback.StreamExtractSubtitle, "")
	<-done
	if !handler.SubtitleCache.HasCommittedEntry("unused", identity, 0, "hdmv_pgs_subtitle", "") {
		t.Fatal("pre-warm did not commit the .sup artifact")
	}
	if err := os.Remove(argsLog); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("PGS cache serve = %d %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("ffmpeg ran for a committed PGS cache hit: %v", err)
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "" {
		t.Fatalf("whole-track PGS response carried a coverage header: %q", got)
	}
}

// A cold PGS fetch that opts into windowed mode without supplying bounds
// (?windowed=1 only) is still a whole-track read: it must join the server's
// implicit window rather than demux the container to EOF, advertise the
// covered range so the client re-fetches, and warm the full .sup.
func TestVirtualPGSBareWindowedFetchJoinsImplicitWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	gate := filepath.Join(dir, "warm.gate")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir, warmArgsLogScript(argsLog, gate))
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("go"), 0o644) })
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?windowed=1", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("PGS bare windowed fetch = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-120.000" {
		t.Fatalf("PGS bare windowed coverage header = %q, want the player-sized page 0.000-120.000", got)
	}
	if got := rec.Header().Get(playback.SubtitleWindowedHeader); got != "true" {
		t.Fatalf("PGS bare windowed header = %q, want true", got)
	}

	waitForWarmInvocation(t, argsLog)
	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	waitForCommittedEntry(t, handler.SubtitleCache, identity, 0, "hdmv_pgs_subtitle")

	window, warm := ffmpegLogLines(t, argsLog)
	if window != 1 {
		t.Fatalf("windowed PGS extracts = %d, want exactly 1", window)
	}
	if warm != 1 {
		t.Fatalf("full-track PGS warms = %d, want exactly 1", warm)
	}
}

// windowedFFmpegArgs returns the single logged invocation that carries -ss.
func windowedFFmpegArgs(t *testing.T, argsLog string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(argsLog)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if strings.Contains(line, "-ss") {
					return line
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no windowed ffmpeg invocation logged")
	return ""
}

// waitForCommittedEntry is waitForCommittedTextEntry generalized to every
// sidecar class, so a PGS warm can be asserted.
func waitForCommittedEntry(t *testing.T, c *playback.SubtitleCache, identity string, trackIndex int, codec string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.HasCommittedEntry("unused", identity, trackIndex, codec, "") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("warm never committed a %s entry under identity %q", codec, identity)
}

// A detached virtual text warm that cannot resolve its relay input must not pin
// the cache's warm slot and in-flight fill forever: resolution runs on a child
// of the extraction context, so a stuck resolver is canceled when the resolve
// budget expires and a later warm can take the slot.
func TestVirtualTextWindowWarmResolveTimeoutReleasesSlot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	oldTimeout := virtualSubtitleWarmResolveTimeout
	virtualSubtitleWarmResolveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { virtualSubtitleWarmResolveTimeout = oldTimeout })

	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")

	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	released := make(chan struct{}, 2)
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		if calls.Add(1) <= 2 {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			released <- struct{}{}
			return ResolvedVirtualMedia{}, ctx.Err()
		}
		return ResolvedVirtualMedia{URL: "http://127.0.0.1:1/video.mkv"}, nil
	})

	optsFor := func(track int) playback.StreamExtractOpts {
		return playback.StreamExtractOpts{
			InputPath:       "unused",
			CacheIdentity:   virtualSubtitleCacheIdentity(file, virtualURI, track),
			TrackIndex:      track,
			SourceCodec:     "subrip",
			SeekSeconds:     600,
			DurationSeconds: 600,
			FFmpegPath:      filepath.Join(dir, "ffmpeg"),
		}
	}

	// Occupy both warm slots with resolvers that block until canceled.
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, optsFor(0), nil, true, "")
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, optsFor(1), nil, true, "")
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("blocked warm resolver never started")
		}
	}

	// With both slots held, a third warm is dropped rather than queued.
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, optsFor(2), nil, true, "")
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("a warm extracted while both slots were held: %v", err)
	}

	// The resolve deadline must cancel both resolvers and release the slots.
	for i := 0; i < 2; i++ {
		select {
		case <-released:
		case <-time.After(2 * time.Second):
			t.Fatal("blocked warm resolver was not canceled by the resolve deadline")
		}
	}

	// A retried warm can now take a slot and commit.
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 2)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, optsFor(2), nil, true, "")
		if handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 2, "subrip", "") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retried warm never committed after the resolve deadline released the slots")
}

// A virtual PGS (.sup) response commits 200 before ffmpeg spawns, so a drift
// probe that cannot establish the live layout must fail closed with a
// retryable error rather than let an unvalidated extraction commit a possibly
// truncated track. Text/ASS fails closed the same way: the URL names a
// plan-time ordinal whose validity depended on the release the probe was
// supposed to confirm, and a same-ordinal different-language rotation would
// never trip the post-spawn map net.
func TestVirtualSubtitleProbeFailureFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	t.Run("pgs_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		argsLog := filepath.Join(dir, "ffmpeg.args")
		handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
			"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\nprintf 'SUP'\n")
		writeExecutableScript(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nexit 1\n")
		file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
		session.VirtualSubtitleTracks = file.SubtitleTracks

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, true)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("PGS probe failure = %d %q, want retryable 503 before any 200", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
			t.Fatalf("ffmpeg spawned after a failed PGS probe: %v", err)
		}
	})

	t.Run("text_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		argsLog := filepath.Join(dir, "ffmpeg.args")
		handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
			"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
		writeExecutableScript(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nexit 1\n")

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, true)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("text probe failure = %d %q, want retryable 503 (never serve a drifted ordinal unverified)", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
			t.Fatalf("ffmpeg spawned after a failed text drift probe: %v", err)
		}
	})
}

// A client that cancels the layout probe — a replan or a navigation away aborts
// it on the request context in milliseconds — is an abandoned request, not a
// source failure. Production answered 503 dependency_unavailable (and logged an
// ERROR) for a 74 ms request nobody was waiting on, which is the media proxy's
// client-cancel bug in the subtitle probe. A genuine probe failure keeps the
// retryable 503 (TestVirtualSubtitleProbeFailureFailsClosed).
func TestVirtualSubtitleProbeCancelIsAbandonedNotUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, pgsWindowStubScript(""))
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nexit 1\n")
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil).WithContext(ctx)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, true)
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled probe = %d %q, want 408 client_canceled (an abandoned request, not a dependency failure)", rec.Code, rec.Body.String())
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("canceled probe body = %q: %v", rec.Body.String(), err)
	}
	if body.Error != virtualProxyDispositionClientCanceled {
		t.Fatalf("canceled probe code = %q, want %q", body.Error, virtualProxyDispositionClientCanceled)
	}
}

// A cold virtual font fetch returns a pending response within its short client
// budget while the extraction keeps running. The detached flight, not the HTTP
// waiter, owns the relay registration: releasing it at handler return would
// leave ffprobe/ffmpeg opening a relay entry that no longer exists.
func TestHandleSubtitleFontsKeepsRelayForDetachedFlight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	restore := setFontBundleClientWait(50 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	urlLog := filepath.Join(dir, "font.url")
	gate := filepath.Join(dir, "font.gate")
	probeScript := "#!/bin/sh\n" +
		"for a in \"$@\"; do url=\"$a\"; done\n" +
		"printf '%s' \"$url\" > '" + urlLog + "'\n" +
		"while [ ! -f '" + gate + "' ]; do sleep 0.01; done\n" +
		fontProbeJSON
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"), probeScript)
	writeExecutableScript(t, filepath.Join(dir, "ffmpeg"), fontFFmpegDumpScript(filepath.Join(dir, "dump.log")))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("provider-media"))
	}))
	t.Cleanup(upstream.Close)

	virtualURI := "virtual://movie/tt-fonts?result=cand-fonts"
	file := &models.MediaFile{ID: 42, ContentID: "movie-fonts", FilePath: virtualURI,
		SubtitleTracks:             []models.SubtitleTrack{{Index: 4, Codec: "ass"}},
		VirtualOwnerInstallationID: 5,
	}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateStreamState(session.ID, playback.SessionStreamState{
		VirtualSourceSet:                 true,
		VirtualSourceURI:                 virtualURI,
		VirtualSourceOwnerInstallationID: 5,
		VirtualSubtitleEvidenceSet:       true,
		VirtualSubtitleTracks:            file.SubtitleTracks,
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfig(filepath.Join(dir, "ffmpeg"), dir)
	handler.SubtitleCache = playback.NewSubtitleCache(func() string { return dir })
	handler.RemoteStreamRelay = remotestream.NewRelay()
	t.Cleanup(func() { _ = handler.RemoteStreamRelay.Close(context.Background()) })
	handler.AllowPrivateStreams = func(int) bool { return true }
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		return ResolvedVirtualMedia{URL: upstream.URL + "/video.mkv"}, nil
	})

	recorder := httptest.NewRecorder()
	handler.HandleSubtitleFonts(recorder, fontBundleHTTPRequest(session.ID, "0"))
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Fatalf("cold font fetch = %d %q, want 200 pending bundle", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(fontBundlePendingHeader); got != "true" {
		t.Fatalf("pending marker = %q, want true", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("pending Cache-Control = %q, want no-store", got)
	}

	relayURL := waitForLoggedURL(t, urlLog)
	resp, err := http.Get(relayURL)
	if err != nil {
		t.Fatalf("relay GET after handler return: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relay URL after handler return = %d, want 200; the HTTP waiter released the flight's source", resp.StatusCode)
	}

	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := fontBundleCacheKey(file, handler.ffmpegPath())
	if !pollHandlerFontBundle(t, handler.SubtitleCache, key) {
		t.Fatal("detached extraction never committed after the HTTP waiter returned")
	}
}

func waitForLoggedURL(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if u := strings.TrimSpace(string(data)); u != "" {
				return u
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("font extraction never recorded its relay input URL")
	return ""
}

// A cold whole-track text fetch against a virtual source must be bounded to an
// implicit first window instead of demuxing the entire container, then start
// exactly one detached full-track warm so later fetches read a small artifact.
// Without this the response needs a full multi-GB read and is aborted by the
// client long before it completes.
func TestVirtualWholeTrackTextFetchServesImplicitWindowThenWarms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	gate := filepath.Join(dir, "warm.gate")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir, warmArgsLogScript(argsLog, gate))
	// Always release the warm, even if an assertion fails, so the blocked fake
	// ffmpeg cannot outlive the test. Registered after the fixture's wait
	// cleanup so LIFO releases the gate before the wait blocks on it.
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("go"), 0o644) })

	rec := httptest.NewRecorder()
	// No position/duration: the native whole-track fetch the live measurement
	// showed. The server must synthesize the window itself.
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("whole-track fetch = %d %q, want a bounded window body", rec.Code, rec.Body.String())
	}
	// A whole-track request served only a window must say so, or the client
	// cannot know it needs to request subsequent windows.
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-120.000" {
		t.Fatalf("windowed coverage header = %q, want 0.000-120.000", got)
	}
	if got := rec.Header().Get(playback.SubtitleWindowedHeader); got != "true" {
		t.Fatalf("windowed marker = %q, want true", got)
	}

	waitForWarmInvocation(t, argsLog)
	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	waitForCommittedTextEntry(t, handler.SubtitleCache, identity, 0, "subrip")

	window, warm := ffmpegLogLines(t, argsLog)
	if window != 1 {
		t.Fatalf("windowed extracts = %d, want exactly 1", window)
	}
	if warm != 1 {
		t.Fatalf("full-track warms = %d, want exactly 1", warm)
	}

	data, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	var windowLine string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, "-ss") {
			windowLine = line
		}
	}
	if !strings.Contains(windowLine, "-ss 0.000") || !strings.Contains(windowLine, "-to 120.000") {
		t.Fatalf("implicit window args = %q, want -ss 0.000 -to 120.000", windowLine)
	}

	// Warm regression: once the detached warm has committed, a second
	// whole-track fetch is served from the small artifact with no source
	// demux — the cache path must pay off rather than re-read the multi-GB
	// source (which is what made the measured repeat fetch slower than the
	// first, because no artifact ever completed).
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec2, req2, file, 0, session, false, "vtt")
	if rec2.Code != http.StatusOK || rec2.Body.String() != warmSubtitleVTT {
		t.Fatalf("post-warm fetch = %d %q, want the committed artifact", rec2.Code, rec2.Body.String())
	}
	if got := rec2.Header().Get(playback.SubtitleCoverageHeader); got != "" {
		t.Fatalf("post-warm whole-track fetch carried a coverage header: %q", got)
	}
	window2, warm2 := ffmpegLogLines(t, argsLog)
	if window2 != window || warm2 != warm {
		t.Fatalf("post-warm fetch ran ffmpeg: window %d->%d warm %d->%d", window, window2, warm, warm2)
	}
}

// When a committed full-track artifact already exists, a whole-track request
// must serve it whole from the cache — no window, no ffmpeg — so an established
// virtual track still returns every cue in one response.
func TestVirtualWholeTrackTextFetchServesCommittedWholeTrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	done := handler.SubtitleCache.WarmTrackInBackground(playback.StreamExtractOpts{
		InputPath:     "unused",
		CacheIdentity: identity,
		TrackIndex:    0,
		SourceCodec:   "subrip",
		FFmpegPath:    filepath.Join(dir, "ffmpeg"),
	}, playback.StreamExtractSubtitle, "")
	<-done
	if !handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("pre-warm did not commit the whole-track artifact")
	}
	if err := os.Remove(argsLog); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || rec.Body.String() != warmSubtitleVTT {
		t.Fatalf("whole-track cache serve = %d %q, want the committed artifact", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("ffmpeg ran for a whole-track cache hit: %v", err)
	}
	// A whole-track response must not advertise a window; clients treat the
	// header's presence as "you got a slice".
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "" {
		t.Fatalf("whole-track response carried a coverage header: %q", got)
	}
	if got := rec.Header().Get(playback.SubtitleWindowedHeader); got != "" {
		t.Fatalf("whole-track response carried a windowed marker: %q", got)
	}
}

// Local files keep the existing whole-track behavior: a local read is cheap and
// bounding it would silently drop cues for a client that wants the complete
// artifact.
func TestLocalWholeTrackTextFetchStaysUnwindowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	writeExecutableScript(t, filepath.Join(dir, "ffmpeg"),
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	mediaPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := NewStreamHandler(nil, nil)
	handler.PlaybackConfig = playbackTestConfig(filepath.Join(dir, "ffmpeg"), dir)
	handler.SubtitleCache = playback.NewSubtitleCache(func() string { return dir })
	file := &models.MediaFile{
		ID: 12, ContentID: "local-movie", FilePath: mediaPath,
		SubtitleTracks: []models.SubtitleTrack{{Index: 0, Codec: "subrip"}},
	}
	session := &playback.Session{ID: "sess-local", UserID: 1, ProfileID: "profile-1", MediaFileID: file.ID}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("local whole-track fetch = %d %q", rec.Code, rec.Body.String())
	}

	window, warm := ffmpegLogLines(t, argsLog)
	if window != 0 || warm != 1 {
		t.Fatalf("local whole-track fetch ran window=%d warm=%d, want window=0 warm=1", window, warm)
	}
}

// A small known virtual source keeps the complete artifact: reading it whole is
// cheap, so bounding it would only drop cues.
func TestVirtualSmallKnownSourceWholeTrackFetchStaysUnwindowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	file.FileSize = 10 << 20 // 10 MiB, below the implicit-window threshold

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("small virtual whole-track fetch = %d %q", rec.Code, rec.Body.String())
	}

	window, whole := ffmpegLogLines(t, argsLog)
	if window != 0 || whole != 1 {
		t.Fatalf("small virtual fetch ran window=%d whole=%d, want window=0 whole=1", window, whole)
	}
}

// A virtual position without a duration is window intent but open-ended. On a
// large/unknown source the server must supply the implicit window duration so
// the extract cannot run to EOF, and must advertise the resulting bounded range
// rather than `0.000-*`.
func TestVirtualPositionWithoutDurationClampsToImplicitWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=0", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("position-only fetch = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-120.000" {
		t.Fatalf("position-only coverage header = %q, want 0.000-120.000", got)
	}
	if got := rec.Header().Get(playback.SubtitleWindowedHeader); got != "true" {
		t.Fatalf("position-only windowed marker = %q, want true", got)
	}
	// The advertised range must match the ffmpeg command: the open `-ss 0`
	// without a `-to` was the unbounded extract this finding is about.
	windowLine := windowedFFmpegArgs(t, argsLog)
	for _, want := range []string{"-ss 0.000", "-to 120.000"} {
		if !strings.Contains(windowLine, want) {
			t.Fatalf("position-only window args %q missing %q", windowLine, want)
		}
	}
}

// A small known virtual source keeps its existing open-ended behavior: reading
// it whole is cheap, so the implicit-window clamp must not touch it.
func TestVirtualSmallKnownSourcePositionOnlyStaysOpenEnded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	file.FileSize = 10 << 20 // below the implicit-window threshold

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=0", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("small-source position-only fetch = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-*" {
		t.Fatalf("small-source coverage header = %q, want open-ended 0.000-*", got)
	}
}

// A subtitle error response that never committed a 200 must not carry the
// bounded-window markers: a header-only classifier would otherwise read a
// failure as a valid window.
func TestSubtitleErrorResponseOmitsCoverageHeaders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\necho 'intentional generic extract failure' >&2\nexit 1\n")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=0", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failing extract = %d %q, want 500", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(playback.SubtitleCoverageHeader); got != "" {
		t.Fatalf("error response carried coverage header %q", got)
	}
	if got := rec.Header().Get(playback.SubtitleWindowedHeader); got != "" {
		t.Fatalf("error response carried windowed marker %q", got)
	}
}

// An upstream 5xx must answer retryable with a Retry-After, and the next client
// retry must not spawn ffmpeg again while the identity is in its backoff.
func TestSubtitleUpstreamFailureAnswersRetryableAndBacksOff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\necho 'Server returned 5XX Server Error reply' >&2\nexit 8\n")

	serve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "ass")
		return rec
	}

	first := serve()
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first status = %d body=%q, want 503", first.Code, first.Body.String())
	}
	retryAfter, err := strconv.Atoi(first.Header().Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After = %q, want a positive delay", first.Header().Get("Retry-After"))
	}
	firstCalls := countLogLines(t, argsLog)
	if firstCalls == 0 {
		t.Fatal("first request never ran ffmpeg")
	}

	second := serve()
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d body=%q, want 503", second.Code, second.Body.String())
	}
	if got := countLogLines(t, argsLog); got != firstCalls {
		t.Fatalf("ffmpeg ran %d times on a retry inside the backoff, want %d", got, firstCalls)
	}
	retryAfter, err = strconv.Atoi(second.Header().Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("second Retry-After = %q, want a positive delay", second.Header().Get("Retry-After"))
	}
}

// The implicit window is the same 120 s page the web player requests, so a
// text whole-track client without a windowing contract is still bounded to a
// read that finishes inside a fetch deadline. The page starts on the
// span-aligned grid so every retry inside it asks for the same window.
func TestVirtualSubtitleImplicitWindowDurationAndResumeStart(t *testing.T) {
	if virtualSubtitleImplicitWindowSeconds != 120 {
		t.Fatalf("implicit window = %vs, want 120s", virtualSubtitleImplicitWindowSeconds)
	}
	handler := &StreamHandler{}
	opts := playback.StreamExtractOpts{TrackIndex: 0, SourceCodec: "subrip"}
	handler.applyImplicitVirtualWindow(&opts, &models.MediaFile{FileSize: 1 << 30}, &playback.Session{Position: 3700}, true)
	if !opts.WindowRequested {
		t.Fatal("cold whole-track virtual fetch was not bounded to the implicit window")
	}
	if opts.SeekSeconds != 3600 {
		t.Fatalf("implicit start = %v, want the containing page start (3600)", opts.SeekSeconds)
	}
	if opts.DurationSeconds != 120 {
		t.Fatalf("implicit duration = %v, want 120", opts.DurationSeconds)
	}
}

// The implicit window start is the span-aligned page containing the session
// position: a repeat fetch from the same page must ask for the range the
// previous request produced, and every page must contain its playhead.
func TestImplicitVirtualWindowStart(t *testing.T) {
	cases := []struct {
		name    string
		session *playback.Session
		span    float64
		want    float64
	}{
		{name: "nil session", session: nil, span: 120, want: 0},
		{name: "fresh session", session: &playback.Session{Position: 0}, span: 120, want: 0},
		{name: "start of the first page", session: &playback.Session{Position: 1}, span: 120, want: 0},
		{name: "inside the first page", session: &playback.Session{Position: 30.237}, span: 120, want: 0},
		{name: "on a page boundary", session: &playback.Session{Position: 240}, span: 120, want: 240},
		{name: "inside a later page", session: &playback.Session{Position: 663.412}, span: 120, want: 600},
		{name: "text page", session: &playback.Session{Position: 3700}, span: 600, want: 3600},
		{name: "negative position", session: &playback.Session{Position: -5}, span: 120, want: 0},
		{name: "zero span", session: &playback.Session{Position: 3700}, span: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := implicitVirtualWindowStart(tc.session, tc.span); got != tc.want {
				t.Fatalf("implicit window start = %v, want %v", got, tc.want)
			}
		})
	}
}

// A whole-track bitmap (PGS) fetch is bounded to the player's own 120 s page.
// A 600 s page can only be produced by demuxing 600 s of the container — ~2.9 GB
// and 65-105 s on the measured 50 GB source — which no client fetch survives,
// so the page never completes and never becomes reusable. 120 s completes inside
// a fetch and is stored for the rest of the page.
func TestVirtualWholeTrackBitmapFetchUsesPlayerSizedPage(t *testing.T) {
	handler := &StreamHandler{}
	large := &models.MediaFile{FileSize: 50 << 30}

	opts := playback.StreamExtractOpts{TrackIndex: 2, SourceCodec: "hdmv_pgs_subtitle"}
	handler.applyImplicitVirtualWindow(&opts, large, &playback.Session{Position: 0}, true)
	if !opts.WindowRequested || !opts.AllowWindow {
		t.Fatalf("bitmap fetch was not windowed: requested=%v allow=%v", opts.WindowRequested, opts.AllowWindow)
	}
	if opts.SeekSeconds != 0 || opts.DurationSeconds != 120 {
		t.Fatalf("first bitmap page = [%v,%v], want [0,120]", opts.SeekSeconds, opts.DurationSeconds)
	}

	// A resumed playhead gets the 120 s page that contains it, not a page
	// anchored 2 s behind the playhead.
	opts = playback.StreamExtractOpts{TrackIndex: 2, SourceCodec: "hdmv_pgs_subtitle"}
	handler.applyImplicitVirtualWindow(&opts, large, &playback.Session{Position: 663.412}, true)
	if opts.SeekSeconds != 600 || opts.DurationSeconds != 120 {
		t.Fatalf("resumed bitmap page = [%v,%v], want [600,720]", opts.SeekSeconds, opts.DurationSeconds)
	}
}

// The production PGS failure: a whole-track bitmap client re-fetches every few
// seconds while the first page is still extracting. Each request used to start
// its page at the live playhead, so its range ran past the page the previous
// request produced, the covering-window cache never matched, and the server
// demuxed the container again on every request — 65-105 s each, every one
// abandoned, so the track rendered from the first trickle and then went silent.
// Inside one page the second request must now be answered by the page the first
// one stored, with no second extraction of that page.
func TestVirtualWholeTrackBitmapPageIsReusedWithinItsPage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	file.FileSize = 50 << 30
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	// A playing session: the paused-only full-track warm would never run, but a
	// page miss starts the progressive fill, whose artifact then answers every
	// later request for the track.
	session.IsPaused = false
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	serve := func(position float64) *httptest.ResponseRecorder {
		session.Position = position
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
		return rec
	}

	first := serve(0)
	if first.Code != http.StatusOK {
		t.Fatalf("first fetch = %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get(playback.SubtitleCoverageHeader); got != "0.000-120.000" {
		t.Fatalf("first coverage = %q, want 0.000-120.000", got)
	}

	// Eight seconds of playback later — still inside the same page. The first
	// request's detached warm must have settled before the position moves: the
	// fixture shares one session pointer with it, so a live mutation would be a
	// test race (in production the warm reads a session-manager snapshot).
	handler.waitForBackgroundSubtitleWarms()
	second := serve(8)
	if second.Code != http.StatusOK {
		t.Fatalf("second fetch = %d %q", second.Code, second.Body.String())
	}
	// No page is extracted a second time: not at the shifted playhead, and not
	// the page again. The track's artifact answered the request whole, which is
	// why it carries no bounded-window coverage.
	if got := countArgsMatching(t, argsLog, "-ss 6.000"); got != 0 {
		t.Fatalf("playhead-anchored page [6,126] was extracted %d times, want 0", got)
	}
	if got := countArgsMatching(t, argsLog, "-ss 0.000", "-to 120.000"); got != 1 {
		t.Fatalf("page [0,120] was extracted %d times, want exactly 1", got)
	}
	if got := second.Header().Get(playback.SubtitleCoverageHeader); got != "" {
		t.Fatalf("second coverage = %q, want none: the committed artifact is served whole", got)
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("second body = %q, want the first page's bytes %q", second.Body.String(), first.Body.String())
	}
	if !handler.SubtitleCache.HasCommittedEntry(file.FilePath, virtualSubtitleCacheIdentity(file, virtualURI, 0), 0, "hdmv_pgs_subtitle", "") {
		t.Fatal("the progressive fill never committed the track's artifact")
	}
}

// A playing bitmap page is what the track's progressive artifact exists to
// answer, so a page miss starts that fill — and only that fill. The fill demuxes
// the track once from source zero and publishes pages from the artifact as it
// parses, so the bounded page prefetch would read the same source a second time
// for pages the fill already produces. Both the page the viewer is on and every
// later page come from that one artifact.
func TestVirtualBitmapPageMissStartsProgressiveFillWhilePlaying(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	file.FileSize = 50 << 30
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	session.IsPaused = false
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	session.Position = 30
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch = %d %q", rec.Code, rec.Body.String())
	}

	// The fill is detached: wait for it to settle before reading the log.
	handler.waitForBackgroundSubtitleWarms()
	window, warm := ffmpegLogLines(t, argsLog)
	if warm != 1 {
		t.Fatalf("progressive fills = %d, want exactly 1: a page miss is what starts it", warm)
	}
	if window != 1 {
		t.Fatalf("source extracts = %d, want only the page this request served", window)
	}
	if got := countArgsMatching(t, argsLog, "-ss 120.000"); got != 0 {
		t.Fatalf("next-page prefetch extracts = %d, want 0: the fill publishes that page", got)
	}
	// The fill reached end of file in this stub, so its artifact replaced every
	// later source read for the track.
	waitForCommittedEntry(t, handler.SubtitleCache, virtualSubtitleCacheIdentity(file, session.VirtualSourceURI, 0), 0, "hdmv_pgs_subtitle")
}

// A bitmap request whose start is off the page grid must be extracted as the
// whole page that contains it. This is the production shape: the web client sent
// the raw playhead (1944.6 and 1939.6, five seconds apart), each request
// produced an identical 229,103-byte slice, nothing was reused, and every page
// boundary showed a blank gap while the next slice was read. The snapped page
// must still cover what the caller asked for and answer the next request inside
// it from the page it stored.
func TestVirtualBitmapUnalignedWindowSnapsToItsPage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	file.FileSize = 50 << 30
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	// A playing session: the full-track .sup warm stays paused-only, so both
	// requests can only be answered by pages.
	session.IsPaused = false
	session.Position = 1944.6
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	serve := func(position string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle?windowed=1&position="+position+"&duration=120", nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
		return rec
	}

	first := serve("1944.6")
	if first.Code != http.StatusOK {
		t.Fatalf("first unaligned page = %d %q", first.Code, first.Body.String())
	}
	// The page containing 1944.6 starts at 1920; the requested range reaches
	// 2064.6, so the extract runs to the next page boundary and advertises
	// exactly what it produced.
	if got := first.Header().Get(playback.SubtitleCoverageHeader); got != "1920.000-2160.000" {
		t.Fatalf("first coverage = %q, want the whole pages 1920.000-2160.000", got)
	}
	handler.waitForBackgroundSubtitleWarms()

	second := serve("1939.6")
	if second.Code != http.StatusOK {
		t.Fatalf("second unaligned page = %d %q", second.Code, second.Body.String())
	}
	if got := second.Header().Get(playback.SubtitleCoverageHeader); got != "1920.000-2160.000" {
		t.Fatalf("second coverage = %q, want the same pages 1920.000-2160.000", got)
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("second body = %q, want the stored page's bytes %q", second.Body.String(), first.Body.String())
	}
	// The second request inside the page never reached ffmpeg, and nothing was
	// ever extracted at the playhead the client sent.
	if got := countArgsMatching(t, argsLog, "-ss 1944.6"); got != 0 {
		t.Fatalf("playhead-anchored slice [1944.6,...] was extracted %d times, want 0", got)
	}
	if got := countArgsMatching(t, argsLog, "-ss 1939.6"); got != 0 {
		t.Fatalf("playhead-anchored slice [1939.6,...] was extracted %d times, want 0", got)
	}
	if got := countArgsMatching(t, argsLog, "-ss 1920.000", "-to 2160.000"); got != 1 {
		t.Fatalf("page [1920,2160] was extracted %d times, want exactly 1", got)
	}
}

// The page after a served page must come from the track's progressive artifact,
// not from a second relay read. The fill parses the track once from source zero
// and keeps every display set it has read, so the next page is cut out of the
// local artifact even though the artifact is whole-track bytes rather than a
// page-shaped slice.
func TestVirtualBitmapNextPageIsCutFromTheFillArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	file.FileSize = 50 << 30
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "fr"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	session.IsPaused = false
	session.Position = 1824.6
	writeSubtitleLayoutProbe(t, dir, file.SubtitleTracks...)

	serve := func(position string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle?windowed=1&position="+position+"&duration=120", nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false)
		return rec
	}

	first := serve("1800")
	if first.Code != http.StatusOK {
		t.Fatalf("first page = %d %q", first.Code, first.Body.String())
	}
	handler.waitForBackgroundSubtitleWarms()
	// Two relay reads so far: the page this request served, and the fill that
	// page miss started. Nothing here is a second page read.
	relayReadsAfterFirst := countArgsMatching(t, argsLog, "http://")
	if relayReadsAfterFirst != 2 {
		t.Fatalf("relay reads after the first page = %d, want the page extract plus the one fill", relayReadsAfterFirst)
	}
	if got := countArgsMatching(t, argsLog, "-ss 1824"); got != 0 {
		t.Fatalf("live-playhead slice [1824.6,...] was extracted %d times, want 0", got)
	}

	second := serve("1920")
	if second.Code != http.StatusOK {
		t.Fatalf("next page = %d %q", second.Code, second.Body.String())
	}
	handler.waitForBackgroundSubtitleWarms()
	// The next page was cut from the local artifact: no second relay read for
	// any window of this track.
	if got := countArgsMatching(t, argsLog, "http://"); got != relayReadsAfterFirst {
		t.Fatalf("relay reads after the next page = %d, want the %d from before: the artifact answers it", got, relayReadsAfterFirst)
	}
	// The page is cut from the local artifact (its input is the cached .sup, not
	// the relay), with the artifact's own seek slack applied.
	if got := countArgsMatching(t, argsLog, "-seek_timestamp 1", ".sup"); got == 0 {
		logged, _ := os.ReadFile(argsLog)
		t.Fatalf("the next page was never cut from the artifact; ffmpeg log:\n%s", logged)
	}
	if second.Body.Len() == 0 {
		t.Fatal("the next page body is empty")
	}
}

// countArgsMatching counts logged ffmpeg invocations containing every want.
func countArgsMatching(t *testing.T, argsLog string, wants ...string) int {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		matched := true
		for _, want := range wants {
			if !strings.Contains(line, want) {
				matched = false
				break
			}
		}
		if matched {
			count++
		}
	}
	return count
}

// A warm opens its own relay registration, so a layout verdict the triggering
// serve established is only an ordinal hint: it never authorizes the bytes the
// warm reads. Even with a matching, fresh seed the warm must probe its own
// registration, and a probe that cannot establish the layout fails closed
// without committing anything.
func TestVirtualSubtitleWarmProbesOwnRegistrationDespiteInheritedVerdict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\nprintf 'probe\\n' >> '"+probeLog+"'\nexit 1\n")

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, &virtualSubtitleLayoutSeed{
		cacheIdentity: identity,
		trackIndex:    0,
		sourceCodec:   "subrip",
		verifiedAt:    time.Now(),
	}, true, "")
	handler.waitForBackgroundSubtitleWarms()

	if data, err := os.ReadFile(probeLog); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("warm trusted the serve's verdict instead of probing its own registration (probe log %q, err %v)", data, err)
	}
	if handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("warm committed bytes after failing to establish its own live layout")
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("warm extracted without a validated layout: %v", err)
	}
}

// Inheritance is identity-keyed: a verdict recorded for a different pinned
// result or inventory must not authorize this warm. It falls back to its own
// probe and fails closed when the layout cannot be established, committing
// nothing.
func TestVirtualSubtitleWarmStaleIdentityStillReprobesAndFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\nprintf 'probe\\n' >> '"+probeLog+"'\nexit 1\n")

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	staleIdentity := virtualSubtitleCacheIdentity(file, "virtual://movie/tt-window?result=previous", 0)
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, &virtualSubtitleLayoutSeed{
		cacheIdentity: staleIdentity,
		trackIndex:    0,
		sourceCodec:   "subrip",
		verifiedAt:    time.Now(),
	}, true, "")
	handler.waitForBackgroundSubtitleWarms()

	if data, err := os.ReadFile(probeLog); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("warm trusted a verdict for a different identity (probe log %q, err %v)", data, err)
	}
	if handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("warm committed bytes after failing to establish the live layout")
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("warm extracted without a validated layout: %v", err)
	}
}

// A serve that trusted its plan-time evidence instead of probing has no live
// layout verdict to lend. If it hands the warm a seed anyway, the warm must
// still probe its own registration, and a probe that cannot establish the
// layout fails closed without committing anything.
func TestVirtualSubtitleWarmProbesOwnRegistrationDespiteEvidenceSeed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\nprintf 'probe\\n' >> '"+probeLog+"'\nexit 1\n")

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, &virtualSubtitleLayoutSeed{
		cacheIdentity: identity,
		trackIndex:    0,
		sourceCodec:   "subrip",
		verifiedAt:    time.Now(),
	}, true, "")
	handler.waitForBackgroundSubtitleWarms()

	if data, err := os.ReadFile(probeLog); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("warm trusted an evidence seed instead of probing its own registration (probe log %q, err %v)", data, err)
	}
	if handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("warm committed bytes after failing to establish its own live layout")
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("warm extracted without a validated layout: %v", err)
	}
}

// An inherited ordinal hint is bounded by the same TTL as the layout memo. A
// seed older than that is not a hint the serve established for this identity
// any more, so the warm ignores it, probes its own registration, and fails
// closed when the layout cannot be established.
func TestVirtualSubtitleWarmExpiredHintReprobesAndFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\nprintf 'probe\\n' >> '"+probeLog+"'\nexit 1\n")

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, &virtualSubtitleLayoutSeed{
		cacheIdentity: identity,
		trackIndex:    0,
		sourceCodec:   "subrip",
		verifiedAt:    time.Now().Add(-virtualSubtitleLayoutCacheTTL - time.Second),
	}, true, "")
	handler.waitForBackgroundSubtitleWarms()

	if data, err := os.ReadFile(probeLog); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("warm trusted an expired hint (probe log %q, err %v)", data, err)
	}
	if handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("warm committed bytes after failing to establish the live layout")
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("warm extracted without a validated layout: %v", err)
	}
}

// The production shape of the regression: a windowed request whose ordinal is
// evidence-bound serves without a drift probe. It therefore has no live-layout
// verdict to hand the warm, which must probe its own registration. Here that
// probe sees a rotated release whose subtitle class no longer matches, so the
// warm fails closed and commits nothing while the serve itself still answers.
func TestVirtualTextWindowEvidenceBoundServeWarmProbesAndFailsClosedOnRotation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	// A rotated release now exposes only a bitmap track, so the warm's own
	// probe cannot map the planned text track and must abandon.
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\nprintf 'probe\\n' >> '"+probeLog+"'\n"+
			"printf '%s' '{\"streams\":[{\"index\":0,\"codec_name\":\"pgs\",\"codec_type\":\"subtitle\"}]}'\n")

	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=600", nil)
	rec := httptest.NewRecorder()
	// driftSuspected=false and the session's evidence still describes this
	// row: exactly the state handleSubtitle derives for an uninterrupted
	// virtual release. The serve must not probe; the warm must, because the
	// serve's evidence is not a verdict on the warm's own registration.
	session.VirtualSubtitleEvidenceSet = true
	session.VirtualSubtitleEvidenceFileID = file.ID
	session.VirtualSubtitleTracks = file.SubtitleTracks
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("window serve: status=%d body=%q", rec.Code, rec.Body.String())
	}
	handler.waitForBackgroundSubtitleWarms()

	if data, err := os.ReadFile(probeLog); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("warm used the serve's evidence instead of probing its own registration (probe log %q, err %v)", data, err)
	}
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	if handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("warm committed bytes after its own probe found no matching track")
	}
	if _, warm := ffmpegLogLines(t, argsLog); warm != 0 {
		t.Fatalf("warm extracted after its own probe failed closed: %d warm invocations", warm)
	}
}

// A fresh inherited seed may steer the warm's own probe toward the ordinal the
// serve settled on, but the probe still runs and the extraction maps the
// ordinal the probe establishes. Here the live release rotated the text tracks
// and the requested language now sits at ordinal 1, so the warm re-keys the fill
// it admitted under the plan identity onto the identity its probe established.
func TestVirtualSubtitleWarmReKeysAfterOwnProbeRemap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")

	// The plan row has English at ordinal 0 and French at ordinal 1; the live
	// release rotated them, so the requested English track is now ordinal 1.
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 0, Codec: "subrip", Language: "eng"},
		{Index: 1, Codec: "subrip", Language: "fre"},
	}
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\nprintf 'probe\\n' >> '"+probeLog+"'\n"+
			"printf '%s' '{\"streams\":[{\"index\":0,\"codec_name\":\"subrip\",\"codec_type\":\"subtitle\",\"tags\":{\"language\":\"fre\"}},{\"index\":1,\"codec_name\":\"subrip\",\"codec_type\":\"subtitle\",\"tags\":{\"language\":\"eng\"}}]}'\n")

	planIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   planIdentity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, &virtualSubtitleLayoutSeed{
		cacheIdentity: planIdentity,
		trackIndex:    1,
		sourceCodec:   "subrip",
		verifiedAt:    time.Now(),
	}, true, "")
	serveIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 1)
	waitForCommittedTextEntry(t, handler.SubtitleCache, serveIdentity, 1, "subrip")
	if handler.SubtitleCache.HasCommittedTextEntry("unused", planIdentity, 0, "subrip", "") {
		t.Fatal("warm committed the remapped bytes under the plan-ordinal identity")
	}

	if data, err := os.ReadFile(probeLog); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("warm skipped its own probe (probe log %q, err %v)", data, err)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("warm never invoked ffmpeg: %v", err)
	}
	if !strings.Contains(string(args), "-map 0:s:1") {
		t.Fatalf("warm did not extract the probed ordinal: args = %q", string(args))
	}
}

// remapBlockState parks the test at a warm's drift-remap verdict: the probe has
// established a different effective ordinal, but the extract invocation has not
// returned its re-key decision yet. That is the window in which an admitted
// page can preempt the same invocation.
type remapBlockState struct {
	once    sync.Once
	blocked chan struct{}
	release chan struct{}
}

func (s *remapBlockState) wait() {
	s.once.Do(func() { close(s.blocked) })
	<-s.release
}

// remapBlockingHandler forwards records to a text handler and blocks the first
// remap verdict until the test releases it.
type remapBlockingHandler struct {
	inner slog.Handler
	gate  *remapBlockState
}

func (h *remapBlockingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *remapBlockingHandler) Handle(ctx context.Context, r slog.Record) error {
	if strings.Contains(r.Message, "remapped onto live layout") {
		h.gate.wait()
	}
	return h.inner.Handle(ctx, r)
}

func (h *remapBlockingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &remapBlockingHandler{inner: h.inner.WithAttrs(attrs), gate: h.gate}
}

func (h *remapBlockingHandler) WithGroup(name string) slog.Handler {
	return &remapBlockingHandler{inner: h.inner.WithGroup(name), gate: h.gate}
}

// An admitted page can restart the warm's extract after the invocation had
// already decided to re-key. The replay re-runs the probe under the same
// admitted identity, finds it authoritative, and commits. The cache may instead
// hand the fill to the serve's own coalescing warm; either way, that re-key
// decision belonged to the invocation that made it. A stale pointer left behind
// would re-key the warm after it had already settled, starting a second logical
// warm for an identity the committed fill did not use.
func TestVirtualSubtitleWarmPageReplayClearsStaleRekeyDecision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeCount := filepath.Join(dir, "ffprobe.count")

	warmLogs := &warmLogBuffer{}
	gate := &remapBlockState{blocked: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(gate.release) }) }
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(&remapBlockingHandler{
		inner: slog.NewTextHandler(warmLogs, &slog.HandlerOptions{Level: slog.LevelDebug}),
		gate:  gate,
	}))
	// Registered before the fixture, so it runs after the fixture's warm wait.
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	// Registered after the fixture, so LIFO releases a blocked warm before the
	// fixture waits on it.
	t.Cleanup(releaseGate)

	// The plan row has English at ordinal 0 and French at ordinal 1. The live
	// release the warm's own probe sees rotated them, so the requested English
	// track is ordinal 1 there; every later probe sees the plan layout again.
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 0, Codec: "subrip", Language: "eng"},
		{Index: 1, Codec: "subrip", Language: "fre"},
	}
	session.VirtualSubtitleTracks = file.SubtitleTracks
	probeLayout := func(language0, language1 string) string {
		return `{"streams":[{"index":0,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"` + language0 + `"}},` +
			`{"index":1,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"` + language1 + `"}}]}`
	}
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\n"+
		"n=$(cat '"+probeCount+"' 2>/dev/null || echo 0)\n"+
		"n=$((n+1))\n"+
		"printf '%s' \"$n\" > '"+probeCount+"'\n"+
		"if [ \"$n\" -eq 1 ]; then\n"+
		"  printf '%s' '"+probeLayout("fre", "eng")+"'\n"+
		"else\n"+
		"  printf '%s' '"+probeLayout("eng", "fre")+"'\n"+
		"fi\n")

	planIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	remappedIdentity := virtualSubtitleCacheIdentity(file, virtualURI, 1)

	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   planIdentity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     600,
		DurationSeconds: 600,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, true, "")

	// Wait until the warm's probe has decided the plan ordinal now lives at
	// ordinal 1, then admit a page before that invocation returns.
	select {
	case <-gate.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("warm's drift probe never reported its remap verdict")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=600", nil)
	// driftSuspected=false: this serve skips its own probe and goes straight to
	// the page admission that preempts the warm's in-flight invocation.
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("page window: status=%d body=%q", rec.Code, rec.Body.String())
	}
	releaseGate()

	handler.waitForBackgroundSubtitleWarms()

	// The replayed invocation's probe established the admitted identity, so the
	// committed fill must land there and nowhere else.
	waitForCommittedTextEntry(t, handler.SubtitleCache, planIdentity, 0, "subrip")
	if handler.SubtitleCache.HasCommittedTextEntry("unused", remappedIdentity, 1, "subrip", "") {
		t.Fatal("warm committed under the remapped identity the replay did not establish")
	}
	// One logical full-track read. The preempted invocation may resume and own
	// the fill, or the serve's own coalescing warm may take the key first; which
	// one starts is a fill-race detail, not the invariant. A second full-track
	// extract is what acting on the abandoned invocation's re-key decision
	// produces.
	window, warm := ffmpegLogLines(t, argsLog)
	if window != 1 {
		t.Fatalf("windowed extracts = %d, want exactly 1 (the admitted page)\n%s", window, warmLogs.String())
	}
	if warm != 1 {
		t.Fatalf("full-track warm extracts = %d, want exactly 1: a replayed invocation is the same warm\n%s",
			warm, warmLogs.String())
	}
	// The re-key decision belonged to the preempted invocation. The replay (or
	// the fill that replaced it) settled under the admitted identity, so no
	// re-key may start a second warm under the remapped identity.
	if got := warmLogs.count("virtual subtitle warm re-keyed after its drift probe"); got != 0 {
		t.Fatalf("redundant re-keys = %d, want 0: the replayed invocation committed under its probe's identity\n%s",
			got, warmLogs.String())
	}
}

// A param-less virtual fetch on a cold track is the native client's one-shot
// whole-track read, not an upgrade probe: it has no windowing contract and no
// probe cadence that would re-issue a shed read, so a saturated detached budget
// must not shed it. Here an unrelated track's detached warm holds the only
// detached slot while the fetch is served from the request-path pool.
func TestParamlessVirtualFetchAdmittedWhileDetachedBudgetSaturated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	gate := filepath.Join(dir, "warm.gate")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, warmArgsLogScript(argsLog, gate))
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("go"), 0o644) })

	serve := func(f *models.MediaFile, s *playback.Session, query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle"+query, nil)
		handler.streamEmbeddedSubtitle(rec, req, f, 0, s, false, "vtt")
		return rec
	}

	// A windowed miss starts the fixture track's detached full-track warm and
	// leaves it blocked on the gate, holding the source's only detached slot.
	if rec := serve(file, session, "?position=600&duration=600"); rec.Code != http.StatusOK {
		t.Fatalf("warm-triggering window = %d %q, want 200", rec.Code, rec.Body.String())
	}
	waitForWarmInvocation(t, argsLog)

	// Same virtual release and relay source, distinct cache identity (file ID),
	// so this request has no committed or warming track of its own: it is the
	// cold param-less shape a native client sends.
	otherFile := *file
	otherFile.ID = file.ID + 1
	otherSession := *session
	otherSession.MediaFileID = otherFile.ID

	probe := serve(&otherFile, &otherSession, "")
	if probe.Code != http.StatusOK {
		t.Fatalf("cold param-less fetch under detached saturation = %d %q, want 200", probe.Code, probe.Body.String())
	}
	if !strings.Contains(probe.Body.String(), "WEBVTT") {
		t.Fatalf("cold param-less fetch body = %q", probe.Body.String())
	}
}

// A param-less fetch against a track whose full-track warm is already in
// flight is the web player's upgrade probe. It stays on the detached budget: a
// saturated detached source skips this probe round (the probe cadence re-issues
// it) instead of stacking a second demux beside the warm.
func TestWarmingParamlessVirtualFetchSkipsDetachedBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	gate := filepath.Join(dir, "warm.gate")
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir, warmArgsLogScript(argsLog, gate))
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("go"), 0o644) })

	serve := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/subtitle"+query, nil)
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
		return rec
	}

	if rec := serve("?position=600&duration=600"); rec.Code != http.StatusOK {
		t.Fatalf("warm-triggering window = %d %q, want 200", rec.Code, rec.Body.String())
	}
	waitForWarmInvocation(t, argsLog)

	probe := serve("")
	if probe.Code != http.StatusServiceUnavailable {
		t.Fatalf("warming param-less probe = %d %q, want 503 retryable", probe.Code, probe.Body.String())
	}
	if probe.Header().Get("Retry-After") == "" {
		t.Fatal("skipped probe did not advertise Retry-After")
	}
	if window, warm := ffmpegLogLines(t, argsLog); window != 1 || warm != 1 {
		t.Fatalf("ffmpeg invocations after skipped probe = %d window, %d warm; want 1/1", window, warm)
	}
}

// A window miss from a mid-file playhead must prefetch bounded windows on the
// request's grid instead of starting a full-track warm, which would extract
// from source zero and publish a tail slice under the whole-track artifact key.
// A later request for the window the playhead fell in is served from the
// prefetched window cache.
func TestVirtualSubtitleWarmPrefetchesPlayheadWindowInsteadOfFullTrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	session.IsPaused = false
	session.Position = 300
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)

	opts := playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     0,
		DurationSeconds: 120,
		WindowRequested: true,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}
	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, opts, nil, true, "")
	handler.waitForBackgroundSubtitleWarms()

	window, warm := ffmpegLogLines(t, argsLog)
	if warm != 0 {
		t.Fatalf("mid-file warm ran %d full-track extracts, want 0", warm)
	}
	if window != 2 {
		t.Fatalf("window prefetch extracts = %d, want 2 (playhead window + next)", window)
	}
	if handler.SubtitleCache.HasCommittedTextEntry("unused", identity, 0, "subrip", "") {
		t.Fatal("mid-file prefetch committed a whole-track artifact")
	}

	// The playhead at 300 s names the [270, 390) window on the text request grid
	// (the web player's 30 s cell, pulled 2 s back), which is exactly the window
	// its next request asks for; it is served from the prefetched slice without
	// another ffmpeg run.
	reqOpts := opts
	reqOpts.SeekSeconds = 270
	reqOpts.DurationSeconds = 120
	reqOpts.DisableBackgroundWarm = true
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=270&duration=120", nil)
	if _, err := handler.SubtitleCache.ServeExtractWithResult(rec, req, reqOpts, func(context.Context, playback.StreamExtractOpts) error {
		t.Error("covering window request re-extracted a prefetched slice")
		return nil
	}); err != nil {
		t.Fatalf("serve prefetched covering window: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("prefetched window body = %q", rec.Body.String())
	}
	if after, _ := ffmpegLogLines(t, argsLog); after != 2 {
		t.Fatalf("ffmpeg window extracts after cached serve = %d, want 2", after)
	}
}

// A session still at the start keeps the full-track warm: that artifact is what
// the opening playback reads, and a window prefetch there would only duplicate
// the request path's own slice.
func TestVirtualSubtitleWarmStaysFullTrackAtStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	session.IsPaused = false
	session.Position = 0
	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)

	handler.warmVirtualSubtitleAfterWindowMiss(file, file.SubtitleTracks[0], session, playback.StreamExtractOpts{
		InputPath:       "unused",
		CacheIdentity:   identity,
		TrackIndex:      0,
		SourceCodec:     "subrip",
		SeekSeconds:     0,
		DurationSeconds: 120,
		WindowRequested: true,
		FFmpegPath:      filepath.Join(dir, "ffmpeg"),
	}, nil, true, "")
	handler.waitForBackgroundSubtitleWarms()

	window, warm := ffmpegLogLines(t, argsLog)
	if window != 0 || warm != 1 {
		t.Fatalf("from-start warm ffmpeg invocations = %d window, %d full-track; want 0/1", window, warm)
	}
	waitForCommittedTextEntry(t, handler.SubtitleCache, identity, 0, "subrip")
}

// A windowed text extract that fails after its muxer wrote the WebVTT header
// must not commit a 200 whose body is only "WEBVTT\n". FFmpeg writes the header
// before it reaches the window's first packet; a failed source read after that
// used to look like a successful empty subtitle document to the browser, which
// is why the measured SRT path showed a 200 with 7 bytes before the next
// request got a real window.
func TestVirtualWindowHeaderOnlyFailureAnswersRetryable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	handler, session, file, _ := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\n"+
			"printf 'WEBVTT\\n'\n"+
			"printf 'Server returned 500 Server Error reply\\n' >&2\n"+
			"exit 1\n")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=120", nil)
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("header-only failure committed a response instead of answering retryably: %v", recovered)
			}
		}()
		handler.streamEmbeddedSubtitle(rec, req, file, 0, session, false, "vtt")
	}()

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("header-only failure = %d %q, want a retryable 503", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("header-only failure body = %q, want no subtitle document", rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("retryable header-only failure carried no Retry-After")
	}
}

// A committed .sup artifact pins the release it was extracted from. A seek that
// re-requests the whole track must serve that artifact even when the live
// layout probe cannot reach the provider; re-probing first is what turned a
// completed artifact into a 503 after a seeking client came back.
func TestVirtualPGSCommittedArtifactSkipsDriftProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.log")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		pgsWindowStubScript(argsLog))
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
	session.VirtualSubtitleTracks = file.SubtitleTracks

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	done := handler.SubtitleCache.WarmTrackInBackground(playback.StreamExtractOpts{
		InputPath:     "unused",
		CacheIdentity: identity,
		TrackIndex:    0,
		SourceCodec:   "hdmv_pgs_subtitle",
		FFmpegPath:    filepath.Join(dir, "ffmpeg"),
	}, playback.StreamExtractSubtitle, "")
	<-done
	if !handler.SubtitleCache.HasCommittedEntry("unused", identity, 0, "hdmv_pgs_subtitle", "") {
		t.Fatal("pre-warm did not commit the .sup artifact")
	}
	// A provider that cannot answer ffprobe is the measured post-seek case: the
	// artifact is on disk, but the probe used to run first and fail closed.
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\necho probe >> '"+probeLog+"'\nexit 1\n")
	if err := os.Remove(argsLog); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("seek re-request = %d %q, want the committed .sup artifact", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("committed .sup artifact served an empty body")
	}
	if got := countLogLines(t, probeLog); got != 0 {
		t.Fatalf("drift probe ran %d times despite a committed .sup artifact, want 0", got)
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("ffmpeg ran for a committed .sup cache hit: %v", err)
	}
}

// A probe failure is answered retryably, but the retry can only succeed if the
// artifact it reads exists. The handler therefore starts a detached fill whose
// own probe gets another chance; production showed repeated probe-timeout 503s
// because nothing was ever warmed between them.
func TestVirtualProbeFailureWarmsSoRetryIsServed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg.args")
	probeLog := filepath.Join(dir, "ffprobe.log")
	marker := filepath.Join(dir, "first-probe")
	handler, session, file, virtualURI := newVirtualSubtitleWindowFixture(t, dir,
		"#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+argsLog+"'\ncat <<'VTT'\n"+warmSubtitleVTT+"VTT\n")
	// The first probe fails; every later probe reports the fixture's layout, so
	// the detached warm can validate its own registration and commit.
	writeExecutableScript(t, filepath.Join(dir, "ffprobe"),
		"#!/bin/sh\necho probe >> '"+probeLog+"'\n"+
			"if [ ! -f '"+marker+"' ]; then : > '"+marker+"'; exit 1; fi\n"+
			"printf '%s' '{\"streams\":[{\"index\":0,\"codec_name\":\"subrip\",\"codec_type\":\"subtitle\",\"tags\":{}}]}'\n")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=120", nil)
	handler.streamEmbeddedSubtitle(rec, req, file, 0, session, true)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed probe = %d %q, want a retryable 503", rec.Code, rec.Body.String())
	}

	identity := virtualSubtitleCacheIdentity(file, virtualURI, 0)
	waitForCommittedTextEntry(t, handler.SubtitleCache, identity, 0, "subrip")
	probesAfterWarm := countLogLines(t, probeLog)
	if probesAfterWarm < 2 {
		t.Fatalf("probes after the warm = %d, want the failed serve probe plus the warm's probe", probesAfterWarm)
	}

	retry := httptest.NewRecorder()
	retryReq := httptest.NewRequest(http.MethodGet, "/subtitle?position=600&duration=120", nil)
	handler.streamEmbeddedSubtitle(retry, retryReq, file, 0, session, true)
	if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), "WEBVTT") {
		t.Fatalf("retry after the warm = %d %q, want the committed artifact", retry.Code, retry.Body.String())
	}
	if got := countLogLines(t, probeLog); got != probesAfterWarm {
		t.Fatalf("retry probed the source again: probes %d -> %d, want a committed-artifact hit", probesAfterWarm, got)
	}
}
