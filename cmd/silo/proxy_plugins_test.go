package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

func TestProxyResidentGatePreservesConfirmedStateDuringReadErrors(t *testing.T) {
	for _, closed := range []string{"missing", "nil", "disabled", "retyped"} {
		t.Run(closed, func(t *testing.T) {
			id := 7
			var node *nodepool.Node
			readErr := errors.New("database unavailable")
			gate := newProxyResidentGate(func() (int, bool) { return id, id > 0 }, func(context.Context, int) (*nodepool.Node, error) {
				return node, readErr
			})
			if err := gate(t.Context()); err == nil {
				t.Fatal("gate opened before the first successful read")
			}
			readErr = nil
			node = &nodepool.Node{ID: id, Type: nodepool.NodeTypeProxy, Enabled: true}
			if err := gate(t.Context()); err != nil {
				t.Fatalf("enabled proxy: %v", err)
			}
			readErr = errors.New("database unavailable")
			if err := gate(t.Context()); err != nil {
				t.Errorf("transient read failure closed a confirmed enabled gate: %v", err)
			}
			readErr = nil
			switch closed {
			case "missing":
				node, readErr = nil, nodepool.ErrNodeNotFound
			case "nil":
				node = nil
			case "disabled":
				node.Enabled = false
			case "retyped":
				node.Type = nodepool.NodeTypeTranscode
			}
			if err := gate(t.Context()); !errors.Is(err, errProxyNodeDisabled) {
				t.Errorf("confirmed %s gate error = %v, want errProxyNodeDisabled", closed, err)
			}
			readErr = errors.New("database unavailable")
			if err := gate(t.Context()); !errors.Is(err, errProxyNodeDisabled) {
				t.Errorf("read failure lost the confirmed closed state: %v", err)
			}
		})
	}
}

func TestProxyResidentGateDoesNotReuseAnotherIdentity(t *testing.T) {
	id := 7
	var readErr error
	gate := newProxyResidentGate(func() (int, bool) { return id, id > 0 }, func(context.Context, int) (*nodepool.Node, error) {
		return &nodepool.Node{ID: id, Type: nodepool.NodeTypeProxy, Enabled: true}, readErr
	})
	if err := gate(t.Context()); err != nil {
		t.Fatal(err)
	}
	id = 8
	readErr = errors.New("database unavailable")
	if err := gate(t.Context()); err == nil {
		t.Fatal("new row reused the previous row's confirmed enabled state")
	}
	id = 0
	if err := gate(t.Context()); !errors.Is(err, errProxyNodeRowUnknown) {
		t.Fatalf("unknown identity error = %v", err)
	}
	id = 7
	if err := gate(t.Context()); err == nil {
		t.Fatal("forgotten identity reused its earlier enabled state")
	}
}
