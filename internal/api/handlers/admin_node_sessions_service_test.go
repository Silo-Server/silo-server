package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

type adminNodeReadRepo struct {
	id  int
	url string
}

func (r adminNodeReadRepo) GetByID(_ context.Context, id int) (*nodepool.Node, error) {
	if id != r.id {
		return nil, nodepool.ErrNodeNotFound
	}
	return &nodepool.Node{URL: r.url}, nil
}

// nodeSessionsFixture writes, for a transcode node at urlA, one record the
// core ingested from its report (under its id) and, for proxies, one record
// under urlA's hash, one under the same hash for urlB (the URLs share their
// 32-bit hash) and one that does not decode.
type nodeSessionsFixture struct {
	client      *redis.Client
	nodeID      int
	urlA, urlB  string
	reported    string
	tracked     string
	otherURL    string
	undecodable string
}

func newNodeSessionsFixture(t *testing.T) *nodeSessionsFixture {
	t.Helper()
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	ctx := t.Context()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	f := &nodeSessionsFixture{
		client: client, nodeID: int(time.Now().UnixNano()%1_000_000_000) + 1,
		urlA: "http://review-node-71586:8080", urlB: "http://review-node-92286:8080",
		reported: "reported-" + suffix, tracked: "tracked-" + suffix, otherURL: "other-" + suffix, undecodable: "broken-" + suffix,
	}
	trackerA := nodesessions.NewTracker(client, f.urlA, "a", "proxy")
	trackerB := nodesessions.NewTracker(client, f.urlB, "b", "proxy")
	if trackerA.NodeHash() != trackerB.NodeHash() {
		t.Fatal("the fixture URLs no longer share a hash")
	}
	hashPrefix := "silo:sessions:" + trackerA.NodeHash() + ":"
	idPrefix := "silo:sessions:n" + strconv.Itoa(f.nodeID) + ":"
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_ = client.Del(ctx, hashPrefix+f.tracked, hashPrefix+f.otherURL, hashPrefix+f.undecodable,
			idPrefix+f.reported, "silo:nodesessions:"+strconv.Itoa(f.nodeID)).Err()
	})
	if err := nodesessions.IngestNodeSessions(ctx, client, f.nodeID, f.urlA, "a",
		[]nodesessions.SessionInfo{{SessionID: f.reported, AuthUserID: 7, ProfileID: "child"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	trackerA.Track(ctx, nodesessions.SessionInfo{SessionID: f.tracked, NodeURL: f.urlA})
	trackerB.Track(ctx, nodesessions.SessionInfo{SessionID: f.otherURL, NodeURL: f.urlB})
	if err := client.Set(ctx, hashPrefix+f.undecodable, "{", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *nodeSessionsFixture) want() string {
	want := []string{f.reported, f.tracked}
	slices.Sort(want)
	return fmt.Sprint(want)
}

func TestAdminNodeSessionsSharedRedisReader(t *testing.T) {
	f := newNodeSessionsFixture(t)
	svc := &AdminNodeSessionsService{Redis: f.client, Nodes: adminNodeReadRepo{id: f.nodeID, url: f.urlA}}
	result, err := svc.Read(t.Context(), f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range result.Sessions {
		ids = append(ids, row.SessionID)
		if row.SessionID == f.reported && row.ProfileID != "child" {
			t.Fatalf("reported record lost its fields: %+v", row)
		}
	}
	slices.Sort(ids)
	if fmt.Sprint(ids) != f.want() || result.Undecodable < 1 {
		t.Fatalf("node sessions = %v (undecodable %d), want %s and the broken record counted", ids, result.Undecodable, f.want())
	}
	_, err = svc.Read(t.Context(), f.nodeID+1)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatal(err)
	}
	if (&AdminNodeSessionsService{}).Available() {
		t.Fatal("unconfigured source available")
	}
}

// The bridge's node filter lists the node's records under its id and the
// records under its URL hash that name its URL, and not a colliding node's.
func TestHandleListSessionsFiltersByTheNodesOwnRecords(t *testing.T) {
	f := newNodeSessionsFixture(t)
	repo := &stubNodeRepository{node: &nodepool.Node{ID: f.nodeID, Type: nodepool.NodeTypeTranscode, URL: f.urlA}}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, f.client, nodeauthtest.SharedSecret("secret"))
	recorder := httptest.NewRecorder()
	handler.HandleListSessions(recorder, httptest.NewRequest(http.MethodGet, "/admin/nodes/sessions?node_id="+strconv.Itoa(f.nodeID), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Sessions []nodesessions.SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range body.Sessions {
		ids = append(ids, row.SessionID)
	}
	slices.Sort(ids)
	if fmt.Sprint(ids) != f.want() {
		t.Fatalf("listed %v, want %s", ids, f.want())
	}
}
