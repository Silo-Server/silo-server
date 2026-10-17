package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

type registryRefreshTrace struct {
	shared chan struct{}
	once   sync.Once
	reads  atomic.Int32
}

func (o *registryRefreshTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "pg_advisory_xact_lock_shared") {
		o.reads.Add(1)
		o.once.Do(func() { close(o.shared) })
	}
	return ctx
}
func (*registryRefreshTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Done is observed after refresh joins the shared flight, so cancellation can
// be scheduled after both callers are waiting without relying on a delay.
type registryRefreshWaiterContext struct {
	//nolint:containedctx // This admitted-reader fixture retains the owning context so reads observe cancellation.
	context.Context
	selected chan struct{}
	once     sync.Once
}

func (c *registryRefreshWaiterContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.selected) })
	return c.Context.Done()
}

func TestWorkerRegistryCanceledRefreshDoesNotCancelAnotherWaiter(t *testing.T) {
	_, writer, _, _, origin := registryCacheFixture(t)
	tx, err := writer.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = nodepool.LockConfiguration(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	observer := &registryRefreshTrace{shared: make(chan struct{})}
	cfg := writer.Config().Copy()
	cfg.ConnConfig.Tracer = observer
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	r := &coreWorkerRegistry{repo: workerregistration.New(pool, nil)}
	first, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() { firstDone <- r.refresh(first) }()
	select {
	case <-observer.shared:
	case <-time.After(10 * time.Second):
		t.Fatal("first refresh did not begin its lock read")
	}
	second := &registryRefreshWaiterContext{Context: t.Context(), selected: make(chan struct{})}
	secondDone := make(chan error, 1)
	go func() { secondDone <- r.refresh(second) }()
	select {
	case <-second.selected:
	case <-time.After(10 * time.Second):
		t.Fatal("second waiter did not join the flight")
	}
	cancelFirst()
	select {
	case err = <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("canceled waiter error:", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter remained blocked")
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-secondDone:
		if err != nil {
			t.Fatal("first waiter cancellation killed another caller:", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second waiter never completed")
	}
	if observer.reads.Load() != 1 {
		t.Fatalf("one shared refresh became %d database snapshots", observer.reads.Load())
	}
	if _, found, err := r.ByURL(t.Context(), origin); err != nil || found {
		t.Fatalf("completed refresh not usable: found=%v error=%v", found, err)
	}
}

func TestWorkerRegistryCanonicalAliasCollisionsRemainDenied(t *testing.T) {
	r, writer, _, first, origin := registryCacheFixture(t)
	ids := []int{first}
	t.Cleanup(func() {
		if _, err := writer.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=ANY($1)", ids); err != nil {
			t.Error(err)
		}
	})
	for _, raw := range []string{strings.ToUpper(origin) + ":443/", origin + ":000443"} {
		var id int
		if err := writer.QueryRow(t.Context(), "INSERT INTO stream_nodes(name,type,url,enabled) VALUES('Registry canonical alias','transcode',$1,TRUE) RETURNING id", raw).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key,allow_http) SELECT unnest($1::int[]),$2,FALSE", ids, []byte(key)); err != nil {
		t.Fatal(err)
	}
	if err = r.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{origin, strings.ToUpper(origin) + ":443", origin + ":0443/"} {
		node, found, lookupErr := r.ByURL(t.Context(), raw)
		if lookupErr == nil && (!found || node.Enabled) {
			t.Fatalf("ambiguous origin became legacy or enabled: origin=%s found=%v node=%+v", raw, found, node)
		}
	}
}
