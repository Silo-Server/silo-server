package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// fakeCore is a core whose handler sees only requests RequireNode admitted.
type fakeCore struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*recordedRequest
	answer   func(r *recordedRequest) int
}

type recordedRequest struct {
	method, path string
	nodeID       int
	header       http.Header
	body         []byte
}

func newFakeCore(t *testing.T, nodeID int, key string, answer func(r *recordedRequest) int) *fakeCore {
	t.Helper()
	core := &fakeCore{answer: answer}
	core.Server = httptest.NewServer(RequireNode(staticKeys{nodeID: key})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := NodeID(r.Context())
		body, _ := io.ReadAll(r.Body)
		rec := &recordedRequest{method: r.Method, path: r.URL.Path, nodeID: id, header: r.Header.Clone(), body: body}
		core.mu.Lock()
		core.requests = append(core.requests, rec)
		core.mu.Unlock()
		status := http.StatusNoContent
		if core.answer != nil {
			status = core.answer(rec)
		}
		if status == http.StatusOK {
			doc, _ := newNodeConfig(NodeConfigBody{Node: NodeIdentity{ID: id, Name: "gpu-1"}})
			w.Header().Set("ETag", `"`+doc.Revision+`"`)
			_ = json.NewEncoder(w).Encode(doc)
			return
		}
		w.WriteHeader(status)
	})))
	t.Cleanup(core.Close)
	return core
}

func (c *fakeCore) recorded() []*recordedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*recordedRequest(nil), c.requests...)
}

func testNodeKey(t *testing.T, id int) string {
	t.Helper()
	key, err := nodepool.GenerateNodeKey(id)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestNewClientTakesTheNodeIDFromItsKey(t *testing.T) {
	key := testNodeKey(t, 12)
	client, err := NewClient("http://core:8080/", key)
	if err != nil || client.NodeID() != 12 {
		t.Fatalf("NewClient = %v, %v", client, err)
	}
	for name, args := range map[string][2]string{
		"malformed key": {"http://core", "snk1_12_short"},
		"empty key":     {"http://core", ""},
		"relative URL":  {"core:8080", key},
		"ftp URL":       {"ftp://core", key},
	} {
		if _, err := NewClient(args[0], args[1]); err == nil {
			t.Fatalf("%s: NewClient accepted it", name)
		}
	}
}

// Every call is signed with the node's own key under the node-to-core label,
// so the core's RequireNode admits it as this node, and a core URL with a
// path prefix still signs the path the core sees.
func TestClientSignsEveryCallAsItsNode(t *testing.T) {
	key := testNodeKey(t, 4)
	core := newFakeCore(t, 4, key, func(r *recordedRequest) int {
		if r.path == PathConfig {
			return http.StatusOK
		}
		return http.StatusNoContent
	})
	client, err := NewClient(core.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	doc, notModified, err := client.FetchConfig(ctx, `"rev"`)
	if err != nil || notModified || doc.Node.ID != 4 {
		t.Fatalf("FetchConfig = %+v, %v, %v", doc, notModified, err)
	}
	if err := client.PostReport(ctx, []byte(`{"boot_id":"1","seq":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := client.NotifyHealthChanged(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.ShipLogs(ctx, []opslog.Entry{{Message: "hello"}}); err != nil {
		t.Fatal(err)
	}
	got := core.recorded()
	want := []string{"GET " + PathConfig, "POST " + PathReport, "POST " + PathHealthChanged, "POST " + PathLogs}
	if len(got) != len(want) {
		t.Fatalf("core saw %d requests, want %d", len(got), len(want))
	}
	for i, r := range got {
		if r.method+" "+r.path != want[i] || r.nodeID != 4 {
			t.Fatalf("request %d = %s %s as node %d, want %s as node 4", i, r.method, r.path, r.nodeID, want[i])
		}
	}
	if got[0].header.Get("If-None-Match") != `"rev"` {
		t.Fatalf("If-None-Match = %q", got[0].header.Get("If-None-Match"))
	}

	// A key for another node, or none, gets nothing.
	other, _ := NewClient(core.URL, testNodeKey(t, 5))
	if _, _, err := other.FetchConfig(ctx, ""); Status(err) != http.StatusUnauthorized {
		t.Fatalf("another node's key = %v, want 401", err)
	}
}

func TestClientRefusesAConfigurationForAnotherNode(t *testing.T) {
	key := testNodeKey(t, 4)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		doc, _ := newNodeConfig(NodeConfigBody{Node: NodeIdentity{ID: 9}})
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer core.Close()
	client, _ := NewClient(core.URL, key)
	if _, _, err := client.FetchConfig(context.Background(), ""); err == nil {
		t.Fatal("FetchConfig accepted node 9's configuration as node 4")
	}
}

// The log drain retries what a retry can fix and drops what it cannot.
func TestShipLogsClassifiesFailuresForTheDrain(t *testing.T) {
	key := testNodeKey(t, 4)
	status := http.StatusServiceUnavailable
	core := newFakeCore(t, 4, key, func(*recordedRequest) int { return status })
	client, _ := NewClient(core.URL, key)
	entries := []opslog.Entry{{Message: "m"}}
	for _, tc := range []struct {
		status    int
		retryable bool
	}{{http.StatusServiceUnavailable, true}, {http.StatusTooManyRequests, true}, {http.StatusBadRequest, false}, {http.StatusRequestEntityTooLarge, false}} {
		status = tc.status
		err := client.ShipLogs(context.Background(), entries)
		if err == nil || errors.Is(err, logstream.ErrRetryable) != tc.retryable {
			t.Fatalf("status %d: %v, want retryable=%v", tc.status, err, tc.retryable)
		}
	}
	core.Close()
	if err := client.ShipLogs(context.Background(), entries); !errors.Is(err, logstream.ErrRetryable) {
		t.Fatalf("unreachable core = %v, want retryable", err)
	}
}

// A batch over the body bound goes in halves, every entry arriving once.
func TestShipLogsSplitsABatchOverTheBodyBound(t *testing.T) {
	key := testNodeKey(t, 4)
	core := newFakeCore(t, 4, key, nil)
	client, _ := NewClient(core.URL, key)
	entries := make([]opslog.Entry, MaxLogBatchEntries)
	for i := range entries {
		entries[i] = opslog.Entry{Message: fmt.Sprintf("%03d %s", i, strings.Repeat("x", 20<<10))}
	}
	if err := client.ShipLogs(context.Background(), entries); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range core.recorded() {
		if len(r.body) > MaxBodyBytes {
			t.Fatalf("a batch of %d bytes was sent", len(r.body))
		}
		var batch LogBatch
		if err := json.Unmarshal(r.body, &batch); err != nil {
			t.Fatal(err)
		}
		for _, entry := range batch.Entries {
			seen[entry.Message[:3]] = true
		}
	}
	if len(core.recorded()) < 2 || len(seen) != MaxLogBatchEntries {
		t.Fatalf("%d requests carried %d distinct entries, want several carrying all %d", len(core.recorded()), len(seen), MaxLogBatchEntries)
	}
	if err := client.ShipLogs(context.Background(), []opslog.Entry{{Message: strings.Repeat("y", MaxBodyBytes)}}); err == nil || errors.Is(err, logstream.ErrRetryable) {
		t.Fatalf("an entry over the bound by itself = %v, want a final failure", err)
	}
}

// The reporter keeps going past a refused report: the next one carries a
// higher sequence.
func TestReporterAdvancesPastARefusedReport(t *testing.T) {
	key := testNodeKey(t, 4)
	var mu sync.Mutex
	answers := []int{http.StatusConflict, http.StatusNoContent, http.StatusInternalServerError, http.StatusNoContent}
	core := newFakeCore(t, 4, key, func(*recordedRequest) int {
		mu.Lock()
		defer mu.Unlock()
		status := answers[0]
		answers = answers[1:]
		return status
	})
	client, _ := NewClient(core.URL, key)
	tracker := nodesessions.NewLocalTracker(nil)
	tracker.Track(context.Background(), nodesessions.SessionInfo{SessionID: "s1", Type: "transcode"})
	reporter := NewReporter(client, tracker.Snapshot, nil)
	for range 4 {
		reporter.reportOnce(context.Background())
	}
	var seqs []uint64
	for _, r := range core.recorded() {
		var report Report
		if err := json.Unmarshal(r.body, &report); err != nil {
			t.Fatal(err)
		}
		if report.BootID != reporter.bootID || len(report.Sessions) != 1 || report.Telemetry != nil {
			t.Fatalf("report = %+v", report)
		}
		seqs = append(seqs, report.Seq)
	}
	if fmt.Sprint(seqs) != "[1 2 3 4]" {
		t.Fatalf("sequences = %v, want 1..4", seqs)
	}
}

// Over the session and transfer bounds, or the body bound, a report carries
// the newest entries and says it is truncated.
func TestReporterTruncatesToTheBounds(t *testing.T) {
	key := testNodeKey(t, 4)
	client, _ := NewClient("http://core.invalid", key)

	many := make([]nodesessions.SessionInfo, MaxReportSessions+500)
	for i := range many {
		many[i] = nodesessions.SessionInfo{SessionID: fmt.Sprintf("s%05d", i)}
	}
	reporter := NewReporter(client, func() []nodesessions.SessionInfo { return many }, func() streamtelemetry.Snapshot {
		return streamtelemetry.Snapshot{Transfers: make([]streamtelemetry.TransferView, 0)}
	})
	body, err := reporter.next()
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Truncated || len(report.Sessions) != MaxReportSessions || report.Sessions[0].SessionID != "s00000" {
		t.Fatalf("truncated=%v sessions=%d first=%s, want the first (newest) %d", report.Truncated, len(report.Sessions), report.Sessions[0].SessionID, MaxReportSessions)
	}

	big := make([]nodesessions.SessionInfo, 1000)
	for i := range big {
		big[i] = nodesessions.SessionInfo{SessionID: fmt.Sprintf("b%04d", i), MediaTitle: strings.Repeat("t", 2000)}
	}
	reporter = NewReporter(client, func() []nodesessions.SessionInfo { return big }, nil)
	body, err = reporter.next()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if len(body) > MaxBodyBytes || !report.Truncated || len(report.Sessions) == 0 || len(report.Sessions) >= len(big) {
		t.Fatalf("body %d bytes, truncated=%v, %d sessions", len(body), report.Truncated, len(report.Sessions))
	}
}

// warningCapture records the messages of the warnings logged through the
// default logger while a test runs.
type warningCapture struct {
	mu       sync.Mutex
	messages []string
}

func (c *warningCapture) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}
func (c *warningCapture) Handle(_ context.Context, record slog.Record) error {
	c.mu.Lock()
	c.messages = append(c.messages, record.Message)
	c.mu.Unlock()
	return nil
}
func (c *warningCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *warningCapture) WithGroup(string) slog.Handler      { return c }

func (c *warningCapture) count(substring string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, message := range c.messages {
		if strings.Contains(message, substring) {
			n++
		}
	}
	return n
}

func captureWarnings(t *testing.T) *warningCapture {
	t.Helper()
	capture := &warningCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return capture
}

// One refused report is routine; a run of them means no report gets through,
// and the node says so, once a minute rather than every second.
func TestReporterWarnsWhenTheCoreKeepsRefusingReports(t *testing.T) {
	warnings := captureWarnings(t)
	key := testNodeKey(t, 4)
	core := newFakeCore(t, 4, key, func(*recordedRequest) int { return http.StatusConflict })
	client, _ := NewClient(core.URL, key)
	reporter := NewReporter(client, nodesessions.NewLocalTracker(nil).Snapshot, nil)
	const refused = "keeps refusing this node's reports"
	for range conflictWarnAfter - 1 {
		reporter.reportOnce(context.Background())
	}
	if n := warnings.count(refused); n != 0 {
		t.Fatalf("%d warnings after %d refusals, want none yet", n, conflictWarnAfter-1)
	}
	reporter.reportOnce(context.Background())
	if n := warnings.count(refused); n != 1 {
		t.Fatalf("%d warnings after %d refusals, want 1", n, conflictWarnAfter)
	}
	for range 3 * conflictWarnAfter {
		reporter.reportOnce(context.Background())
	}
	if n := warnings.count(refused); n != 1 {
		t.Fatalf("%d warnings within a minute, want 1", n)
	}
}

// A 401 on any call but the configuration read asks the node to reread its
// configuration, which is where it records a rejected key. The read itself
// must not ask for another read, or a rejected node would poll in a loop.
func TestClientReportsUnauthorizedCallsButNotTheConfigRead(t *testing.T) {
	key := testNodeKey(t, 4)
	core := newFakeCore(t, 4, key, func(*recordedRequest) int { return http.StatusUnauthorized })
	client, _ := NewClient(core.URL, key)
	var calls int
	client.SetOnUnauthorized(func() { calls++ })

	if _, _, err := client.FetchConfig(context.Background(), ""); Status(err) != http.StatusUnauthorized {
		t.Fatalf("FetchConfig = %v, want a 401", err)
	}
	if calls != 0 {
		t.Fatalf("the configuration read reported its own 401 (%d calls)", calls)
	}
	if err := client.PostReport(context.Background(), []byte("{}")); Status(err) != http.StatusUnauthorized {
		t.Fatalf("PostReport = %v, want a 401", err)
	}
	if calls != 1 {
		t.Fatalf("a 401 report ran the hook %d times, want 1", calls)
	}
}

// The relay's open and renewal go through the client: signed as the node,
// with the core's refusals as the nodesource.StatusError the relay retries
// by, and a 401 asking for a configuration reread like any other call.
func TestOpenSourceIsSignedAndMapsRefusals(t *testing.T) {
	key := testNodeKey(t, 4)
	status := http.StatusGone
	core := newFakeCore(t, 4, key, func(*recordedRequest) int { return status })
	client, _ := NewClient(core.URL, key)
	var unauthorized int
	client.SetOnUnauthorized(func() { unauthorized++ })

	_, err := client.OpenSource(context.Background(), "grant-1")
	var refusal *nodesource.StatusError
	if !errors.As(err, &refusal) || refusal.Op != "open" || refusal.Status != http.StatusGone || !refusal.Fatal() {
		t.Fatalf("OpenSource on a 410 = %v, want a fatal nodesource.StatusError", err)
	}
	requests := core.recorded()
	if len(requests) != 1 || requests[0].path != nodesource.PathOpen || requests[0].nodeID != 4 || !strings.Contains(string(requests[0].body), `"grant-1"`) {
		t.Fatalf("core saw %+v, want one signed open of grant-1 by node 4", requests)
	}

	status = http.StatusUnauthorized
	if _, err := client.OpenSource(context.Background(), "grant-1"); !errors.As(err, &refusal) || refusal.Fatal() {
		t.Fatalf("OpenSource on a 401 = %v, want a retryable refusal", err)
	}
	if unauthorized != 1 {
		t.Fatalf("a 401 open ran the hook %d times, want 1", unauthorized)
	}
}
