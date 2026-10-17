package main

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// The protected DB-pins workflow comes from main, so its Redis setup cannot
// change until this PR merges. Keep these actual production pins runnable on
// that workflow without replacing Redis or skipping their assertions.
func startWorkerQueryRedis(t *testing.T) string {
	t.Helper()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("production query pins need SILO_TEST_REDIS_URL or Docker")
	}
	name := "silo-worker-query-" + uuid.NewString()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, docker, "rm", "-f", name).CombinedOutput()
		if err != nil && !strings.Contains(string(output), "No such container") {
			t.Errorf("remove query Redis fixture: %v: %s", err, output)
		}
	})
	startCtx, cancelStart := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancelStart()
	if output, err := exec.CommandContext(startCtx, docker, "run", "--rm", "-d", "--name", name, "-p", "127.0.0.1::6379", "redis:8.2-alpine").CombinedOutput(); err != nil {
		t.Fatalf("start query Redis fixture: %v: %s", err, output)
	}
	output, err := exec.CommandContext(startCtx, docker, "port", name, "6379/tcp").Output()
	if err != nil {
		t.Fatalf("locate query Redis fixture: %v", err)
	}
	address := strings.TrimSpace(string(output))
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("invalid query Redis loopback address: %q", address)
	}
	client := redis.NewClient(&redis.Options{Addr: address, DialTimeout: 200 * time.Millisecond, ReadTimeout: 200 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	readyCtx, cancelReady := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelReady()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := client.Ping(readyCtx).Err(); err == nil {
			return "redis://" + address + "/0"
		}
		select {
		case <-readyCtx.Done():
			t.Fatalf("query Redis fixture did not become ready: %v", readyCtx.Err())
		case <-tick.C:
		}
	}
}
