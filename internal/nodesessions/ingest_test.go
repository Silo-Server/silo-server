package nodesessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testNodeIDs hands each test its own node ids, so no two tests, and no two
// runs a second apart, share session keys.
var testNodeIDs atomic.Int64

func init() { testNodeIDs.Store(time.Now().UnixNano() % 1_000_000_000 * 100) }

// uniqueNode returns a node id and URL no other test run shares the keys of.
func uniqueNode(t *testing.T, rdb *redis.Client) (int, string) {
	t.Helper()
	id := int(testNodeIDs.Add(1))
	url := "http://ingest-" + strconv.Itoa(id) + ".test"
	cleanupNode(t, rdb, id, url)
	return id, url
}

// cleanupNode deletes, after the test, the session keys node id may have
// written under its id or under the hash of url, and its index.
func cleanupNode(t *testing.T, rdb *redis.Client, id int, url string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		keys, _ := rdb.Keys(ctx, keyPrefix+nodeHash(url)+":*").Result()
		byID, _ := rdb.Keys(ctx, keyPrefix+"n"+strconv.Itoa(id)+":*").Result()
		keys = append(append(keys, byID...), nodeIndexPrefix+strconv.Itoa(id))
		rdb.Del(ctx, keys...)
	})
}

// recordsOf lists the session ids ListAll finds that name node url.
func recordsOf(t *testing.T, rdb *redis.Client, url string) []string {
	t.Helper()
	listed, err := ListAll(context.Background(), rdb, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range listed.Sessions {
		if row.NodeURL == url {
			ids = append(ids, row.SessionID)
		}
	}
	slices.Sort(ids)
	return ids
}

// The core writes a node's reported sessions in the record shape the Redis
// tracker writes, byte for byte, under the node's id, so every reader of
// silo:sessions:* finds them.
func TestIngestNodeSessionsWritesTheTrackersRecords(t *testing.T) {
	rdb := redisTestClient(t)
	ctx := context.Background()
	id, url := uniqueNode(t, rdb)
	info := SessionInfo{SessionID: "s1", NodeURL: url, NodeName: "gpu-1", Type: "transcode", CodecVideo: "h264",
		StartedAt: "2026-10-14T10:00:00Z", AuthUserID: 3, ProfileID: "p1", MediaFileID: 9}

	NewTracker(rdb, url, "gpu-1", "transcode").Track(ctx, info)
	trackerKey := keyPrefix + nodeHash(url) + ":s1"
	want, err := rdb.Get(ctx, trackerKey).Result()
	if err != nil {
		t.Fatalf("tracker record: %v", err)
	}
	rdb.Del(ctx, trackerKey)

	reported := info
	reported.NodeURL, reported.NodeName = "http://somebody-else", "somebody-else"
	if err := IngestNodeSessions(ctx, rdb, id, url, "gpu-1", []SessionInfo{reported}, time.Now().Add(30*time.Second)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	key := keyPrefix + "n" + strconv.Itoa(id) + ":s1"
	got, err := rdb.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("ingested record: %v", err)
	}
	if got != want {
		t.Fatalf("ingested record\n%s\nwant the tracker's\n%s", got, want)
	}
	if ttl := rdb.TTL(ctx, key).Val(); ttl <= 50*time.Second || ttl > sessionTTL {
		t.Fatalf("record TTL = %v, want the tracker's %v", ttl, sessionTTL)
	}
	if ids := recordsOf(t, rdb, url); fmt.Sprint(ids) != "[s1]" {
		t.Fatalf("ListAll finds %v, want the ingested record", ids)
	}
}

// Session keys used to carry only 32 bits of the row URL's hash. These two
// URLs share it; a node at one could then overwrite the other's session and
// its next empty report delete it. Keys under the node id keep them apart.
func TestIngestNodeSessionsKeepsNodesWithCollidingURLHashesApart(t *testing.T) {
	rdb := redisTestClient(t)
	ctx := context.Background()
	const urlA, urlB = "http://review-node-71586:8080", "http://review-node-92286:8080"
	if nodeHash(urlA) != nodeHash(urlB) {
		t.Fatal("the fixture URLs no longer share a hash")
	}
	idA, _ := uniqueNode(t, rdb)
	idB, _ := uniqueNode(t, rdb)
	cleanupNode(t, rdb, idA, urlA)
	cleanupNode(t, rdb, idB, urlB)
	session := "shared-" + strconv.Itoa(idA)
	latest := time.Now().Add(30 * time.Second)

	if err := IngestNodeSessions(ctx, rdb, idB, urlB, "b", []SessionInfo{{SessionID: session}}, latest); err != nil {
		t.Fatal(err)
	}
	if err := IngestNodeSessions(ctx, rdb, idA, urlA, "a", []SessionInfo{{SessionID: session}}, latest); err != nil {
		t.Fatal(err)
	}
	if got := recordsOf(t, rdb, urlB); fmt.Sprint(got) != "["+session+"]" {
		t.Fatalf("node B's records after node A reported the same session = %v", got)
	}
	if err := IngestNodeSessions(ctx, rdb, idA, urlA, "a", nil, latest); err != nil {
		t.Fatal(err)
	}
	if got := recordsOf(t, rdb, urlB); fmt.Sprint(got) != "["+session+"]" {
		t.Fatalf("node B's records after node A's empty report = %v", got)
	}
	if got := recordsOf(t, rdb, urlA); len(got) != 0 {
		t.Fatalf("node A's empty report left %v", got)
	}
}

// afterIndexRead runs fn once, right after the first command that names
// index completes: the point where an ingest that read the index before
// writing could be overtaken.
type afterIndexRead struct {
	index string
	once  sync.Once
	fn    func()
}

func (h *afterIndexRead) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *afterIndexRead) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err == nil && slices.Contains(cmd.Args(), any(h.index)) {
			h.once.Do(h.fn)
		}
		return err
	}
}

func (h *afterIndexRead) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// Two replicas ingesting reports of one node at once must not both act on
// the same index: the one that writes second would leave the other's record
// listed nowhere, and it would outlive every later report. Here ingest B runs
// whole between ingest A's first touch of the index and the rest of A; an
// empty report must then still remove everything.
func TestIngestNodeSessionsConcurrentReportsLeaveNothingBehind(t *testing.T) {
	rdb := redisTestClient(t)
	ctx := context.Background()
	id, url := uniqueNode(t, rdb)
	latest := time.Now().Add(30 * time.Second)
	if err := IngestNodeSessions(ctx, rdb, id, url, "n", []SessionInfo{{SessionID: "x"}}, latest); err != nil {
		t.Fatal(err)
	}

	replicaA := redis.NewClient(rdb.Options())
	t.Cleanup(func() { _ = replicaA.Close() })
	var errB error
	replicaA.AddHook(&afterIndexRead{index: nodeIndexPrefix + strconv.Itoa(id), fn: func() {
		errB = IngestNodeSessions(ctx, rdb, id, url, "n", []SessionInfo{{SessionID: "b"}}, latest)
	}})
	if err := IngestNodeSessions(ctx, replicaA, id, url, "n", []SessionInfo{{SessionID: "a"}}, latest); err != nil {
		t.Fatal(err)
	}
	if errB != nil {
		t.Fatal(errB)
	}
	if err := IngestNodeSessions(ctx, rdb, id, url, "n", nil, latest); err != nil {
		t.Fatal(err)
	}
	if left := recordsOf(t, rdb, url); len(left) != 0 {
		key := keyPrefix + "n" + strconv.Itoa(id) + ":" + left[0]
		t.Fatalf("records %v survived an empty report (TTL=%v; index=%v)", left,
			rdb.TTL(ctx, key).Val(), rdb.SMembers(ctx, nodeIndexPrefix+strconv.Itoa(id)).Val())
	}
}

// A node's view reads its records under its id and the proxy-shaped records
// under its URL hash that name its URL, and nothing another node owns.
func TestListNodeReadsOnlyTheNodesOwnRecords(t *testing.T) {
	rdb := redisTestClient(t)
	ctx := context.Background()
	const urlA, urlB = "http://review-node-71586:8080", "http://review-node-92286:8080"
	idA, _ := uniqueNode(t, rdb)
	idB, _ := uniqueNode(t, rdb)
	cleanupNode(t, rdb, idA, urlA)
	cleanupNode(t, rdb, idB, urlB)
	suffix := strconv.Itoa(idA)
	latest := time.Now().Add(30 * time.Second)

	if err := IngestNodeSessions(ctx, rdb, idA, urlA, "a", []SessionInfo{{SessionID: "reported-" + suffix}}, latest); err != nil {
		t.Fatal(err)
	}
	if err := IngestNodeSessions(ctx, rdb, idB, urlB, "b", []SessionInfo{{SessionID: "other-node-" + suffix}}, latest); err != nil {
		t.Fatal(err)
	}
	NewTracker(rdb, urlA, "a", "proxy").Track(ctx, SessionInfo{SessionID: "tracked-" + suffix, NodeURL: urlA})
	// Same hash, other URL: a colliding proxy's record.
	NewTracker(rdb, urlB, "b", "proxy").Track(ctx, SessionInfo{SessionID: "colliding-" + suffix, NodeURL: urlB})

	result, err := ListNode(ctx, rdb, idA, urlA, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range result.Sessions {
		ids = append(ids, row.SessionID)
	}
	slices.Sort(ids)
	if want := fmt.Sprint([]string{"reported-" + suffix, "tracked-" + suffix}); fmt.Sprint(ids) != want {
		t.Fatalf("ListNode = %v, want %s", ids, want)
	}
	raw, err := NodeRecords(ctx, rdb, idA, urlA)
	if err != nil {
		t.Fatal(err)
	}
	var rawIDs []string
	for _, record := range raw {
		var info SessionInfo
		if err := json.Unmarshal(record, &info); err != nil {
			t.Fatal(err)
		}
		rawIDs = append(rawIDs, info.SessionID)
	}
	slices.Sort(rawIDs)
	if fmt.Sprint(rawIDs) != fmt.Sprint(ids) {
		t.Fatalf("NodeRecords = %v, want %v", rawIDs, ids)
	}
}

// Each report is the node's whole set: sessions it no longer lists are
// deleted, and an empty report deletes them all.
func TestIngestNodeSessionsReplacesTheWholeSet(t *testing.T) {
	rdb := redisTestClient(t)
	ctx := context.Background()
	id, url := uniqueNode(t, rdb)
	latest := time.Now().Add(30 * time.Second)
	key := func(session string) string { return keyPrefix + "n" + strconv.Itoa(id) + ":" + session }
	exists := func(session string) bool { return rdb.Exists(ctx, key(session)).Val() == 1 }

	if err := IngestNodeSessions(ctx, rdb, id, url, "n", []SessionInfo{{SessionID: "a"}, {SessionID: "b"}}, latest); err != nil {
		t.Fatal(err)
	}
	if !exists("a") || !exists("b") {
		t.Fatal("first report not written")
	}
	if err := IngestNodeSessions(ctx, rdb, id, url, "n", []SessionInfo{{SessionID: "b"}, {SessionID: "c"}}, latest); err != nil {
		t.Fatal(err)
	}
	if exists("a") || !exists("b") || !exists("c") {
		t.Fatalf("after the second report: a=%v b=%v c=%v, want only b and c", exists("a"), exists("b"), exists("c"))
	}
	if err := IngestNodeSessions(ctx, rdb, id, url, "n", nil, latest); err != nil {
		t.Fatal(err)
	}
	if exists("b") || exists("c") {
		t.Fatal("an empty report left records behind")
	}
}

// A node's clock cannot date a session into the future.
func TestIngestNodeSessionsClampsFutureStartTimes(t *testing.T) {
	rdb := redisTestClient(t)
	ctx := context.Background()
	id, url := uniqueNode(t, rdb)
	latest := time.Now().Add(30 * time.Second).Truncate(time.Second)
	future := latest.Add(time.Hour)
	reported := SessionInfo{SessionID: "f", StartedAt: future.UTC().Format(time.RFC3339), StartedAtUnixNano: future.UnixNano()}
	if err := IngestNodeSessions(ctx, rdb, id, url, "n", []SessionInfo{reported}, latest); err != nil {
		t.Fatal(err)
	}
	listed, err := ListAll(ctx, rdb, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range listed.Sessions {
		if row.SessionID != "f" || row.NodeURL != url {
			continue
		}
		if row.StartedAtUnixNano != latest.UnixNano() || row.StartedAt != latest.UTC().Format(time.RFC3339) {
			t.Fatalf("start = %s / %d, want clamped to %s", row.StartedAt, row.StartedAtUnixNano, latest)
		}
		return
	}
	t.Fatal("record not found")
}

func TestIngestNodeSessionsRefusesInvalidIDs(t *testing.T) {
	rdb := redisTestClient(t)
	id, url := uniqueNode(t, rdb)
	err := IngestNodeSessions(context.Background(), rdb, id, url, "n", []SessionInfo{{SessionID: ""}}, time.Now())
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("empty session id = %v, want ErrInvalidSession", err)
	}
}

func TestLocalTrackerKeepsSessionsInMemory(t *testing.T) {
	tracker := NewLocalTracker(func() (string, string) { return "http://gpu-1", "gpu-1" })
	ctx := context.Background()
	if tracker.NodeURL() != "http://gpu-1" || tracker.NodeName() != "gpu-1" {
		t.Fatalf("identity = %q %q", tracker.NodeURL(), tracker.NodeName())
	}
	tracker.Track(ctx, SessionInfo{SessionID: "old", StartedAt: "2026-10-14T10:00:00Z"})
	tracker.Track(ctx, SessionInfo{SessionID: "new", StartedAtUnixNano: time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC).UnixNano()})
	tracker.Track(ctx, SessionInfo{SessionID: "gone"})
	tracker.Remove(ctx, "gone")
	got := tracker.Snapshot()
	if len(got) != 2 || got[0].SessionID != "new" || got[1].SessionID != "old" {
		t.Fatalf("snapshot = %+v, want new then old", got)
	}
	tracker.Cleanup(ctx)
	if len(tracker.Snapshot()) != 0 {
		t.Fatal("Cleanup left sessions")
	}
}
