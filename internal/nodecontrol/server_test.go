package nodecontrol

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// Anything that is not a valid signature by a transcode node with a key gets
// 401 before a handler runs; a disabled node keeps its access.
func TestRequireNodeRefusesAllButAKeyedTranscodeNode(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(1, "gpu-1")
	f.nodes.update(1, func(n *nodepool.Node) { n.Enabled = false })
	f.nodes.add(t, nodepool.Node{ID: 2, Name: "proxy-2", Type: nodepool.NodeTypeProxy, URL: "http://proxy-2"})
	keyless := f.addNode(3, "gpu-3")
	f.nodes.keys[3] = ""
	deleted := f.addNode(4, "gpu-4")
	f.nodes.remove(4)
	other := f.addNode(5, "gpu-5")

	if got := f.serve(1, key, http.MethodGet, PathConfig, nil, nil).Code; got != http.StatusOK {
		t.Fatalf("disabled keyed node: %d, want 200", got)
	}
	unsigned := httptest.NewRecorder()
	f.handler.ServeHTTP(unsigned, httptest.NewRequest(http.MethodGet, PathConfig, nil))
	for name, code := range map[string]int{
		"unsigned":             unsigned.Code,
		"another node's key":   f.serve(1, other, http.MethodGet, PathConfig, nil, nil).Code,
		"a proxy row":          f.serve(2, key, http.MethodGet, PathConfig, nil, nil).Code,
		"a keyless node":       f.serve(3, keyless, http.MethodGet, PathConfig, nil, nil).Code,
		"a deleted node":       f.serve(4, deleted, http.MethodGet, PathConfig, nil, nil).Code,
		"a deleted node write": f.serve(4, deleted, http.MethodPost, PathHealthChanged, nil, nil).Code,
	} {
		if code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, code)
		}
	}
	if got := f.bus.published(); len(got) != 0 {
		t.Fatalf("a refused request published %v", got)
	}
}

func TestConfigServesTheNodesDocument(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	doc, etag := f.config(7, key)
	if etag != `"`+doc.Revision+`"` {
		t.Fatalf("ETag %q does not name revision %q", etag, doc.Revision)
	}
	if recomputed := newNodeConfig(doc.NodeConfigBody); recomputed.Revision != doc.Revision {
		t.Fatalf("revision %q is not the hash of the body (%q)", doc.Revision, recomputed.Revision)
	}
	want := NodeConfigBody{
		Node:         NodeIdentity{ID: 7, Name: "gpu-7", URL: "http://gpu-7:8080"},
		SourceAccess: nodesource.ModeLocal,
		Playback: PlaybackConfig{FFmpegPath: "/opt/ffmpeg/bin/ffmpeg", HWAccel: "qsv", HWDevice: "/dev/dri/renderD128",
			TranscodeDir: "/var/silo/transcode", SegmentRetentionSeconds: 900, TrickplayNodeCapacity: 4},
		Download:  DownloadConfig{ArtifactDir: "/var/silo/artifacts", ArtifactMaxBytes: 1000},
		Subtitles: SubtitlesConfig{SyncNodeCapacity: 3},
		Logging:   LoggingConfig{Level: "debug", Format: "json", Quiet: "scanner", CaptureLevel: "warn"},
	}
	if doc.NodeConfigBody != want {
		t.Fatalf("document\n%+v\nwant\n%+v", doc.NodeConfigBody, want)
	}
	if bytes.Contains(f.serve(7, key, http.MethodGet, PathConfig, nil, nil).Body.Bytes(), []byte("must-never-reach-a-node")) {
		t.Fatal("the configuration carries the JWT secret")
	}

	// Stable while nothing changes, and 304 for the current revision.
	if again, _ := f.config(7, key); again.Revision != doc.Revision {
		t.Fatalf("revision changed from %q to %q with nothing changed", doc.Revision, again.Revision)
	}
	notModified := f.serve(7, key, http.MethodGet, PathConfig, nil, http.Header{"If-None-Match": {etag}})
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != etag {
		t.Fatalf("If-None-Match current revision: %d %q", notModified.Code, notModified.Body.String())
	}

	// The node's own row: overrides and the drain request move the revision.
	accel, device, dir, budget := "vaapi", "/dev/dri/renderD129", "/srv/node-artifacts", int64(5000)
	f.nodes.update(7, func(n *nodepool.Node) {
		n.HWAccelOverride, n.HWDeviceOverride = &accel, &device
		n.DownloadArtifactDirOverride, n.DownloadArtifactMaxBytesOverride = &dir, &budget
		n.DrainRequested = true
		n.SourceAccess = nodepool.SourceAccessCore
	})
	overridden, _ := f.config(7, key)
	if overridden.Playback.HWAccel != accel || overridden.Playback.HWDevice != device ||
		overridden.Download.ArtifactDir != dir || overridden.Download.ArtifactMaxBytes != budget || !overridden.DrainRequested ||
		overridden.SourceAccess != nodesource.ModeCore {
		t.Fatalf("row not applied: %+v", overridden.NodeConfigBody)
	}
	if overridden.Revision == doc.Revision {
		t.Fatal("revision did not move with the row")
	}
	if got := f.serve(7, key, http.MethodGet, PathConfig, nil, http.Header{"If-None-Match": {etag}}); got.Code != http.StatusOK {
		t.Fatalf("stale If-None-Match: %d, want 200", got.Code)
	}
	// A server-wide setting moves it too.
	f.settings["playback.trickplay_node_capacity"] = "2"
	if changed, _ := f.config(7, key); changed.Revision == overridden.Revision || changed.Playback.TrickplayNodeCapacity != 2 {
		t.Fatalf("setting change: revision moved %v, capacity %d", changed.Revision != overridden.Revision, changed.Playback.TrickplayNodeCapacity)
	}

	// The row is gone but the registry has not dropped the key yet.
	f.server.opts.Nodes = goneNodes{}
	if got := f.serve(7, key, http.MethodGet, PathConfig, nil, nil).Code; got != http.StatusNotFound {
		t.Fatalf("config of a deleted row: %d, want 404", got)
	}
}

// goneNodes is a node table that has lost every row.
type goneNodes struct{}

func (goneNodes) GetByID(context.Context, int) (*nodepool.Node, error) {
	return nil, nodepool.ErrNodeNotFound
}

func testReport(boot string, seq uint64, sessions ...string) Report {
	report := Report{BootID: boot, Seq: seq}
	for _, id := range sessions {
		report.Sessions = append(report.Sessions, nodesessions.SessionInfo{SessionID: id, Type: "transcode", NodeURL: "http://spoofed", NodeName: "spoofed"})
	}
	return report
}

// sessions lists the live session records as the admin readers see them,
// grouped by the node URL each record names.
func (f *controlFixture) sessions() map[string][]string {
	f.t.Helper()
	listed, err := nodesessions.ListAll(context.Background(), f.redis, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	byNode := map[string][]string{}
	for _, row := range listed.Sessions {
		byNode[row.NodeURL+" "+row.NodeName] = append(byNode[row.NodeURL+" "+row.NodeName], row.SessionID)
	}
	for _, ids := range byNode {
		slices.Sort(ids)
	}
	return byNode
}

// The fence takes a report only when (boot, seq) is newer: replays, reordered
// reports and an old boot after a new one are refused and change nothing.
func TestReportFenceRefusesStaleReports(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	post := func(report Report) int { return f.serve(7, key, http.MethodPost, PathReport, report, nil).Code }
	const boot, newerBoot = "1800000000000000000", "1800000000000000001"
	const self = "http://gpu-7:8080 gpu-7"

	if code := post(testReport(boot, 1, "a")); code != http.StatusNoContent {
		t.Fatalf("first report: %d", code)
	}
	if code := post(testReport(boot, 1, "replayed")); code != http.StatusConflict {
		t.Fatalf("replay: %d, want 409", code)
	}
	if code := post(testReport(boot, 3, "c")); code != http.StatusNoContent {
		t.Fatalf("seq 3: %d", code)
	}
	if code := post(testReport(boot, 2, "late")); code != http.StatusConflict {
		t.Fatalf("reordered seq 2: %d, want 409", code)
	}
	if got := f.sessions(); fmt.Sprint(got[self]) != "[c]" || len(got) != 1 {
		t.Fatalf("sessions after refused reports = %v, want only [c]", got)
	}
	if code := post(testReport(newerBoot, 1, "restarted")); code != http.StatusNoContent {
		t.Fatalf("new boot: %d", code)
	}
	if code := post(testReport(boot, 100, "old-boot")); code != http.StatusConflict {
		t.Fatalf("old boot after a new one: %d, want 409", code)
	}
	if got := f.sessions(); fmt.Sprint(got[self]) != "[restarted]" {
		t.Fatalf("sessions = %v, want [restarted]", got)
	}
	for _, bad := range []string{"", "0", "-1", "01", "x", "18446744073709551615"} {
		if code := post(testReport(bad, 200)); code != http.StatusBadRequest {
			t.Fatalf("boot_id %q: %d, want 400", bad, code)
		}
	}
}

type notedStreamEnd struct {
	fileID  int
	seconds float64
}

type remoteTranscodes map[string]struct {
	nodeURL string
	fileID  int
}

// remoteTranscodeStart is when every remoteTranscodes session started.
var remoteTranscodeStart = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func (m remoteTranscodes) RemoteTranscode(transportID string) (string, int, time.Time, bool) {
	t, ok := m[transportID]
	return t.nodeURL, t.fileID, remoteTranscodeStart, ok
}

type streamEndNoterFunc func(ctx context.Context, fileID int, end playback.StreamEnd)

func (f streamEndNoterFunc) Note(ctx context.Context, fileID int, end playback.StreamEnd) {
	f(ctx, fileID, end)
}

// A node reports where its transcodes found their media ends, and the core
// passes each to the stream-end store, so a file's stored duration follows
// whichever server or proxy relayed the segments. It takes one only for a
// transcode this server's playback session runs on that node for the file
// named: a node cannot shorten another file.
func TestReportPassesStreamEndsOn(t *testing.T) {
	f := newControlFixture(t)
	var noted []notedStreamEnd
	f.server.opts.StreamEnds = streamEndNoterFunc(func(_ context.Context, fileID int, end playback.StreamEnd) {
		if end.PlannedSeconds != 160 {
			t.Errorf("planned %v s, want the reported 160", end.PlannedSeconds)
		}
		// The node's run started no earlier than the core's session, so
		// a probe after that may describe a replaced file.
		if !end.ReadSince.Equal(remoteTranscodeStart) {
			t.Errorf("read since %v, want the playback session's start %v", end.ReadSince, remoteTranscodeStart)
		}
		noted = append(noted, notedStreamEnd{fileID, end.Seconds})
	})
	f.server.opts.Transcodes = remoteTranscodes{
		"ended":      {"http://gpu-7:8080", 42},
		"other-node": {"http://gpu-8:8080", 42},
		"other-file": {"http://gpu-7:8080", 42},
	}
	key := f.addNode(7, "gpu-7")
	report := testReport("1800000000000000000", 1, "ended", "other-node", "other-file", "unknown", "playing")
	for i, fileID := range []int{42, 42, 99, 42, 43} {
		report.Sessions[i].MediaFileID, report.Sessions[i].StreamEndSeconds, report.Sessions[i].StreamPlannedSeconds = fileID, 149.5, 160
	}
	report.Sessions[4].StreamEndSeconds = 0
	if code := f.serve(7, key, http.MethodPost, PathReport, report, nil).Code; code != http.StatusNoContent {
		t.Fatalf("report: %d", code)
	}
	if fmt.Sprint(noted) != fmt.Sprint([]notedStreamEnd{{42, 149.5}}) {
		t.Fatalf("noted %v, want file 42 at 149.5 s from its own transcode only", noted)
	}
}

// The fence compares decimal strings by length, then lexically, since Lua
// numbers cannot hold a nanosecond boot id: a sequence or boot id gaining or
// losing a digit must still order numerically.
func TestReportFenceOrdersAcrossDigitLengths(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	type step struct {
		boot string
		seq  uint64
		want int
	}
	run := func(steps ...step) {
		t.Helper()
		f.redis.Del(context.Background(), reportFenceKey(7))
		for _, s := range steps {
			if code := f.serve(7, key, http.MethodPost, PathReport, testReport(s.boot, s.seq), nil).Code; code != s.want {
				t.Fatalf("boot %s seq %d: %d, want %d", s.boot, s.seq, code, s.want)
			}
		}
	}
	run(
		step{"9", 9, http.StatusNoContent},
		step{"9", 10, http.StatusNoContent}, // seq 9 -> 10
		step{"9", 9, http.StatusConflict},   // seq 10 -> 9
		step{"9", 100, http.StatusNoContent},
		step{"99", 1, http.StatusNoContent},
		step{"1000", 1, http.StatusNoContent}, // boot 99 -> 1000
		step{"999", 1000, http.StatusConflict},
		step{"99", 1, http.StatusConflict},
	)
	run(
		step{"999", 5, http.StatusNoContent},
		step{"1000", 1, http.StatusNoContent}, // boot 999 -> 1000
		step{"999", 6, http.StatusConflict},
	)
}

// Each node's report writes only that node's records, under the registered
// row's URL and name: a node cannot overwrite or delete another's sessions or
// telemetry, whatever its report claims.
func TestReportsStayWithinTheReportingNode(t *testing.T) {
	f := newControlFixture(t)
	keyA, keyB := f.addNode(7, "gpu-7"), f.addNode(8, "gpu-8")
	const boot = "1800000000000000000"
	now := time.Now()
	report := func(seq uint64, sessions ...string) Report {
		r := testReport(boot, seq, sessions...)
		snapshot := streamtelemetry.Snapshot{
			// A node names another publisher and dates everything a day ahead.
			PublisherID: PublisherID(8), NodeID: "gpu-8", CapturedAt: now.Add(24 * time.Hour),
		}
		for _, id := range sessions {
			snapshot.Sessions = append(snapshot.Sessions, streamtelemetry.SessionView{SessionID: id, Subject: streamtelemetry.UserSubject(3),
				StartedAt: now.Add(24 * time.Hour), LastByteAccepted: now.Add(24 * time.Hour)})
		}
		wire, _, err := streamtelemetry.EncodeNodeSnapshot(snapshot, MaxReportSessions, MaxReportTransfers)
		if err != nil {
			t.Fatal(err)
		}
		r.Telemetry = &wire
		return r
	}
	if got := f.serve(8, keyB, http.MethodPost, PathReport, report(1, "b1", "b2"), nil); got.Code != http.StatusNoContent {
		t.Fatalf("node 8 report: %d %s", got.Code, got.Body)
	}
	if got := f.serve(7, keyA, http.MethodPost, PathReport, report(1, "b1", "a1"), nil); got.Code != http.StatusNoContent {
		t.Fatalf("node 7 report: %d %s", got.Code, got.Body)
	}
	// Node 7 drops everything; node 8's records stay.
	if got := f.serve(7, keyA, http.MethodPost, PathReport, report(2), nil); got.Code != http.StatusNoContent {
		t.Fatalf("node 7 empty report: %d %s", got.Code, got.Body)
	}
	if got := fmt.Sprint(f.sessions()); got != "map[http://gpu-8:8080 gpu-8:[b1 b2]]" {
		t.Fatalf("session records = %s", got)
	}
	if got := f.serve(7, keyA, http.MethodPost, PathReport, report(3, "a1"), nil); got.Code != http.StatusNoContent {
		t.Fatalf("node 7 report: %d %s", got.Code, got.Body)
	}
	if got := f.sessions()["http://gpu-7:8080 gpu-7"]; fmt.Sprint(got) != "[a1]" {
		t.Fatalf("node 7 records = %v, want [a1] under its registered URL and name", got)
	}

	set, err := f.telemetry.LoadAll(context.Background())
	if err != nil || len(set.Errors) != 0 {
		t.Fatalf("telemetry readers: %+v, %v", set, err)
	}
	stored := map[string]streamtelemetry.Snapshot{}
	for _, snapshot := range set.Snapshots {
		stored[snapshot.PublisherID] = snapshot
	}
	a, b := stored[PublisherID(7)], stored[PublisherID(8)]
	if len(stored) != 2 || a.NodeID != "gpu-7" || b.NodeID != "gpu-8" || len(b.Sessions) != 2 || len(a.Sessions) != 1 {
		t.Fatalf("stored telemetry = %+v", stored)
	}
	latest := time.Now().Add(maxClockSkew)
	if a.CapturedAt.After(time.Now()) || a.Sessions[0].LastByteAccepted.After(latest) || a.Sessions[0].StartedAt.After(latest) {
		t.Fatalf("stored snapshot keeps the node's clock: %+v", a)
	}
}

func TestReportRefusesMoreThanItsBounds(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	ids := make([]string, MaxReportSessions+1)
	for i := range ids {
		ids[i] = strconv.Itoa(i)
	}
	if got := f.serve(7, key, http.MethodPost, PathReport, testReport("1800000000000000000", 1, ids...), nil); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the session bound: %d, want 413", got.Code)
	}
	if got := f.sessions(); len(got) != 0 {
		t.Fatalf("an over-bound report wrote %v", got)
	}
}

// A core without Redis has nowhere to put a report and nothing to read one;
// it takes it and drops it, so the node does not retry.
func TestReportWithoutRedisIsDropped(t *testing.T) {
	nodes := &fakeNodes{}
	key := nodes.add(t, nodepool.Node{ID: 7, Name: "gpu-7", Type: nodepool.NodeTypeTranscode})
	handler := mountForTest(NewServer(Options{Nodes: nodes}), nodes)
	body := []byte(`{"boot_id":"1","seq":1,"sessions":[]}`)
	r := httptest.NewRequest(http.MethodPost, PathReport, bytes.NewReader(body))
	nodeauthtest.SignFromNode(t, r, 7, key)
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, r)
	if got.Code != http.StatusNoContent {
		t.Fatalf("report without Redis: %d, want 204", got.Code)
	}
}

// Shipped entries are filed under the authenticated node, whatever node id
// they carry.
func TestLogsStampTheAuthenticatedNode(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	batch := LogBatch{Entries: []opslog.Entry{
		{Timestamp: time.Now(), Level: "INFO", Component: "transcodenode", Message: "one", NodeID: "gpu-8#8"},
		{Timestamp: time.Now(), Level: "WARN", Component: "ffmpeg", Message: "two"},
	}}
	if got := f.serve(7, key, http.MethodPost, PathLogs, batch, nil); got.Code != http.StatusNoContent {
		t.Fatalf("logs: %d %s", got.Code, got.Body.String())
	}
	f.logs.mu.Lock()
	stored := fmt.Sprint(len(f.logs.batches), len(f.logs.batches["gpu-7#7"]))
	f.logs.mu.Unlock()
	if stored != "1 2" {
		t.Fatalf("stored batches %v, want both entries under gpu-7#7 only", f.logs.batches)
	}
	tooMany := LogBatch{Entries: make([]opslog.Entry, MaxLogBatchEntries+1)}
	if got := f.serve(7, key, http.MethodPost, PathLogs, tooMany, nil); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the entry bound: %d, want 413", got.Code)
	}
}

// A node cannot fill the operational log at the request budget's pace: log
// batches also spend a byte budget, and a node past it is told to retry.
func TestLogsSpendAByteBudget(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	other := f.addNode(8, "gpu-8")
	now := time.Now()
	f.server.now = func() time.Time { return now }
	// One entry a little under the body bound.
	batch := LogBatch{Entries: []opslog.Entry{{Timestamp: now, Level: "INFO", Component: "ffmpeg",
		Message: strings.Repeat("x", MaxBodyBytes-4096)}}}
	for sent := 0; sent < nodeLogBytesBurst/MaxBodyBytes; sent++ {
		if got := f.serve(7, key, http.MethodPost, PathLogs, batch, nil); got.Code != http.StatusNoContent {
			t.Fatalf("batch %d within the budget: %d %s", sent, got.Code, got.Body.String())
		}
	}
	got := f.serve(7, key, http.MethodPost, PathLogs, batch, nil)
	if got.Code != http.StatusTooManyRequests || got.Header().Get("Retry-After") == "" {
		t.Fatalf("batch past the budget: %d, want 429 with Retry-After", got.Code)
	}
	if got := f.serve(8, other, http.MethodPost, PathLogs, batch, nil); got.Code != http.StatusNoContent {
		t.Fatalf("another node's batch: %d, want its own budget", got.Code)
	}
	// The budget refills with time.
	now = now.Add(time.Duration(float64(MaxBodyBytes)/float64(nodeLogBytesRate)*float64(time.Second)) + time.Second)
	if got := f.serve(7, key, http.MethodPost, PathLogs, batch, nil); got.Code != http.StatusNoContent {
		t.Fatalf("batch after the refill: %d %s", got.Code, got.Body.String())
	}
}

func TestHealthChangedAnnouncesTheNodesDrainChange(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	if got := f.serve(7, key, http.MethodPost, PathHealthChanged, []byte(`{"node_id":8}`), nil); got.Code != http.StatusNoContent {
		t.Fatalf("health-changed: %d %s", got.Code, got.Body.String())
	}
	want := cache.ChannelAdmin + " " + cache.EventNodeDrainChanged + " 7"
	if got := f.bus.published(); len(got) != 1 || got[0] != want {
		t.Fatalf("published %v, want [%s]", got, want)
	}
}
