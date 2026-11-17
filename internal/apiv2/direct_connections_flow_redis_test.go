//go:build redislab

package apiv2

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/directconn"
)

// labDB is the one Redis database index these tests may flush.
const labDB = 15

// newDirectRedis registers one presence on client for the test's lifetime. A
// restart in the same test keeps it, so the census still counts one replica.
func newDirectRedis(t *testing.T, client *redis.Client) directRedis {
	t.Helper()
	presence := cache.NewAPIReplicaPresence(client, "direct-test")
	if err := presence.Register(t.Context()); err != nil {
		t.Fatal(err)
	}
	return directRedis{client: client, presence: presence}
}

// store returns a Redis coordination store fenced by the presence's ID.
func (r directRedis) store() directconn.CoordinationStore {
	return directconn.NewCoordinationStore(r.client, r.presence.ID())
}

// TestDirectSignalingRedisRestart runs through scripts/direct-redis-lab.sh against
// a disposable Redis. After the service restarts on the same Redis, the next
// netmap, read with the same ticket, carries the API host's new disco key, and
// discovery recovers without enrolling again. It opens UDP sockets.
func TestDirectSignalingRedisRestart(t *testing.T) {
	rawURL := os.Getenv("SILO_DIRECT_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("set SILO_DIRECT_TEST_REDIS_URL to a disposable Redis database (index 15)")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatal("invalid SILO_DIRECT_TEST_REDIS_URL")
	}
	if opts.DB != labDB {
		t.Fatalf("SILO_DIRECT_TEST_REDIS_URL must use disposable database index %d", labDB)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("flush disposable database: %v", err)
	}

	clock := newDirectLiveClock()
	secret := newDirectSecret(t)
	// The engine lease fences the store's host and confirmation writes, so both
	// services take it with one presence, as one process restarting its engine.
	rds := newDirectRedis(t, client)
	ds, f := startFlow(t, rds.store(), clock, secret, rds)
	api := ds.host.NodeKey
	d := f.newDevice("tv")
	d.enroll()
	d.start()
	d.ready(api, 30*time.Second)
	apiDisco := func() string {
		t.Helper()
		rec := directGetNetmap(t, f.handler(), d.currentTicket(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("netmap = %d: %s", rec.Code, rec.Body.String())
		}
		var doc directNetmapDoc
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc.Hosts) != 1 {
			t.Fatalf("netmap hosts = %+v, %v", doc.Hosts, err)
		}
		return doc.Hosts[0].DiscoKey
	}
	before := apiDisco()

	d.stopClient()
	ds.stop()
	f.srv.CloseClientConnections()
	restarted := startDirectService(t, rds.store(), clock.Now, secret, rds)
	if restarted.host.NodeKey != api {
		t.Fatalf("restarted API node key = %s, want %s", restarted.host.NodeKey, api)
	}
	f.env.coord = restarted.coord
	f.env.deps.DirectCoordination = restarted.coord
	f.rebuild()
	var after string
	directWait(t, "the new API disco key in the netmap", 15*time.Second, func() bool {
		after = apiDisco()
		return after != before
	})
	d.startClient()
	d.ready(api, 30*time.Second)
}
