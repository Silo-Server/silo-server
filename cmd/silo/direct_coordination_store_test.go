package main

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/server"

	"github.com/Silo-Server/silo-server/internal/config"
)

// The coordination store bounds each read with a context deadline, so its Redis
// client must end a read that the server never answers when that deadline
// passes, not when the URL's read timeout does.
func TestDirectCoordinationStoreEndsAStalledReadAtTheContextDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Read what the client sends and never answer.
			go func() { _, _ = io.Copy(io.Discard, conn) }()
		}
	}()
	store, closeStore, err := newDirectCoordinationStore(config.RedisConfig{URL: "redis://" + ln.Addr().String() + "/0?read_timeout=1m"}, "fence")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := store.Device(ctx, server.DeviceRef{Account: "1", Device: "tv"}, time.Now())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, server.ErrUnavailable) {
			t.Fatalf("read past its context deadline = %v, want server.ErrUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a read past its context deadline still waits on the socket")
	}
}
