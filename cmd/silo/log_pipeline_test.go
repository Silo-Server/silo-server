package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logstream"
)

// These tests drive the production log wiring (configureOperationalLogging,
// configureActivityLogging and the activity middleware) with a Redis-backed
// event bus, and check that logging never waits on Redis from the caller's
// goroutine. They swap the global slog default, so they must not run in
// parallel.

// TestOperationalLoggingReturnsWhileRedisIsDown times one slog.Info through the
// real handler chain while every Redis endpoint refuses connections, and
// checks that it touched no Redis client.
func TestOperationalLoggingReturnsWhileRedisIsDown(t *testing.T) {
	restoreDefaultLogger(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hub := logstream.NewHub("node-a", cache.NewEventBus("redis://"+refusedAddr(t)))
	t.Cleanup(hub.Close)
	_, _, stop := configureOperationalLogging(ctx, unreachablePool(t), newMemorySettings(), hub,
		slog.DiscardHandler, "node-a")
	t.Cleanup(stop)

	opsBefore := redisOperations(t)
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		slog.InfoContext(ctx, "probe: one info record", "component", "probe")
	}()
	var elapsed time.Duration
	returned := false
	select {
	case <-done:
		elapsed, returned = time.Since(start), true
	case <-time.After(2 * time.Second):
		elapsed = time.Since(start)
	}
	ops := redisOperations(t) - opsBefore
	// Unwind anything still logging through the pipeline before asserting.
	slog.SetDefault(slog.New(slog.DiscardHandler))
	<-done

	t.Logf("slog.Info returned=%v after %v; Redis operations meanwhile: %v", returned, elapsed, ops)
	// The window is generous so a loaded host cannot fail the test; a writer
	// that waits on Redis never returns at all while Redis refuses connections.
	if !returned {
		t.Fatalf("slog.Info with Redis down did not return within %v", elapsed)
	}
	if ops != 0 {
		t.Fatalf("slog.Info attempted %v Redis operations, want 0", ops)
	}
}

// TestLoggedRequestIssuesNoRedisCommands serves 100 requests that each log at
// info through the activity middleware and counts the Redis commands issued
// while they ran.
func TestLoggedRequestIssuesNoRedisCommands(t *testing.T) {
	restoreDefaultLogger(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fake := startFakeRedis(t)
	hub := logstream.NewHub("node-a", cache.NewEventBus("redis://"+fake.addr))
	t.Cleanup(hub.Close)
	pool := unreachablePool(t)
	_, _, stopOperational := configureOperationalLogging(ctx, pool, newMemorySettings(), hub,
		slog.DiscardHandler, "node-a")
	t.Cleanup(stopOperational)
	activityWriter, stopActivity := configureActivityLogging(pool, hub)
	t.Cleanup(stopActivity)

	handler := activitylog.NewMiddleware(activityWriter, "node-a")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slog.InfoContext(r.Context(), "probe: handled request", "component", "probe")
			w.WriteHeader(http.StatusNoContent)
		}))
	serve := func() {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/probe", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d", rec.Code)
		}
	}

	// The first request may dial; measure the steady state.
	serve()
	const requests = 100
	before := fake.snapshot()
	for range requests {
		serve()
	}
	delta := fake.snapshot().minus(before)

	t.Logf("Redis commands during %d logged requests: %d %v", requests, delta.total(), delta)
	if delta.total() != 0 {
		t.Fatalf("%d logged requests issued %d Redis commands (%v), want 0", requests, delta.total(), delta)
	}
}

// BenchmarkLoggedInfo measures one captured slog.Info through the production
// operational log wiring, with Redis configured and healthy on loopback.
func BenchmarkLoggedInfo(b *testing.B) {
	restoreDefaultLogger(b)
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)

	fake := startFakeRedis(b)
	hub := logstream.NewHub("node-a", cache.NewEventBus("redis://"+fake.addr))
	b.Cleanup(hub.Close)
	_, _, stop := configureOperationalLogging(ctx, unreachablePool(b), newMemorySettings(), hub,
		slog.DiscardHandler, "node-a")
	b.Cleanup(stop)

	for b.Loop() {
		slog.InfoContext(ctx, "probe: handled request", "component", "probe", "status", 200)
	}
}

// redisOperations sums silo_dependency_operations_total for Redis across
// commands, roles and outcomes. Every client built by the cache package reports
// there, including failed attempts.
func redisOperations(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, family := range families {
		if family.GetName() != "silo_dependency_operations_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["dependency"] == "redis" {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}

func restoreDefaultLogger(t testing.TB) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// refusedAddr returns a loopback address with nothing listening on it.
func refusedAddr(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// unreachablePool is a lazily connecting pool whose queries fail fast, so the
// consumers run without a database.
func unreachablePool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://silo@"+refusedAddr(t)+"/silo?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

func newMemorySettings() *memorySettings { return &memorySettings{values: map[string]string{}} }

func (s *memorySettings) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *memorySettings) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *memorySettings) GetAll(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, nil
}

// fakeRedis speaks just enough RESP2 for go-redis clients to connect and run
// RPUSH, PUBLISH and script calls. It counts every command it receives.
type fakeRedis struct {
	addr   string
	mu     sync.Mutex
	counts commandCounts
}

type commandCounts map[string]int

func (c commandCounts) total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

func (c commandCounts) minus(before commandCounts) commandCounts {
	out := commandCounts{}
	for k, v := range c {
		if d := v - before[k]; d != 0 {
			out[k] = d
		}
	}
	return out
}

func startFakeRedis(t testing.TB) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{addr: ln.Addr().String(), counts: commandCounts{}}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	var conns sync.Map
	t.Cleanup(func() {
		conns.Range(func(k, _ any) bool {
			_ = k.(net.Conn).Close()
			return true
		})
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Store(conn, struct{}{})
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				f.serve(conn)
			}()
		}
	}()
	return f
}

func (f *fakeRedis) snapshot() commandCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(commandCounts, len(f.counts))
	for k, v := range f.counts {
		out[k] = v
	}
	return out
}

func (f *fakeRedis) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		args, err := readRESPArray(r)
		if err != nil || len(args) == 0 {
			return
		}
		name := strings.ToUpper(args[0])
		f.mu.Lock()
		f.counts[name]++
		f.mu.Unlock()
		var reply string
		switch name {
		case "RPUSH", "PUBLISH":
			reply = ":1\r\n"
		case "EVALSHA", "EVAL":
			reply = "*0\r\n"
		case "PING":
			reply = "+PONG\r\n"
		default:
			// HELLO and CLIENT SETINFO: a Redis error keeps go-redis on RESP2.
			reply = "-ERR unknown command\r\n"
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func readRESPArray(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected RESP line %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "$")))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}
