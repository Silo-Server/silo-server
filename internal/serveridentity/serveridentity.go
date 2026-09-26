// Package serveridentity owns the deployment's native server identity: one
// random ID, minted on first use and stored in server_settings, that every
// API process of a deployment answers with whatever address it was reached
// at. Clients use it to recognize one deployment across its public URL, LAN
// address and overlay-network origins instead of comparing hostnames.
//
// The ID is deliberately separate from the diagnostics installation ID (which
// keys upload manifests and import receipts) and from the Jellyfin-compat
// server ID (a configurable setting on the compatibility surface), so neither
// of those contracts constrains this one.
//
// Lifecycle: the value survives restarts, hostname and URL changes, and a
// database restore, all of which keep the deployment the same server. A
// cloned database carries the same ID to the clone; that is accepted for now
// because the ID authorizes nothing on its own. It is not encrypted: it is
// public discovery data, and an encrypted row would become unreadable after a
// SECRET_KEY rotation, which must not change a server's identity.
package serveridentity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Key is the server_settings row that holds the identity.
const Key = "server.identity_id"

// ErrUnavailable reports that no settings store is wired, so no identity can
// be read or minted.
var ErrUnavailable = errors.New("server identity: settings store unavailable")

// Store is the read/write surface over server_settings the identity needs.
// catalog.ServerSettingsRepo and catalog.EncryptedSettingsRepo satisfy it.
type Store interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// conditionalStore is the optional insert-if-absent surface. When the store
// offers it, concurrent processes minting at the same moment converge on one
// value instead of overwriting each other.
type conditionalStore interface {
	SetIfAbsent(ctx context.Context, key, value string) (bool, error)
}

// Service reads the identity once and caches it for the process lifetime; the
// value never changes while the deployment lives, so a cached answer is exact.
type Service struct {
	store Store

	mu sync.Mutex
	id string
}

// New returns a service over store. A nil store yields ErrUnavailable from
// every read.
func New(store Store) *Service { return &Service{store: store} }

// ServerID returns the deployment's identity, minting and persisting one on
// the first call of a fresh deployment.
func (s *Service) ServerID(ctx context.Context) (string, error) {
	if s == nil || s.store == nil {
		return "", ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id != "" {
		return s.id, nil
	}
	id, err := Ensure(ctx, s.store)
	if err != nil {
		return "", err
	}
	s.id = id
	return id, nil
}

// Ensure returns the stored identity, minting and persisting one when the row
// is absent or empty. With a conditional store the seed is single-writer:
// exactly one minted value lands and every process re-reads the winner.
func Ensure(ctx context.Context, store Store) (string, error) {
	if store == nil {
		return "", ErrUnavailable
	}
	existing, err := store.Get(ctx, Key)
	if err != nil {
		return "", fmt.Errorf("server identity: read: %w", err)
	}
	if id := strings.TrimSpace(existing); id != "" {
		return id, nil
	}
	minted := uuid.NewString()
	conditional, ok := store.(conditionalStore)
	if !ok {
		if err := store.Set(ctx, Key, minted); err != nil {
			return "", fmt.Errorf("server identity: seed: %w", err)
		}
		return minted, nil
	}
	if _, err := conditional.SetIfAbsent(ctx, Key, minted); err != nil {
		return "", fmt.Errorf("server identity: seed: %w", err)
	}
	winner, err := store.Get(ctx, Key)
	if err != nil {
		return "", fmt.Errorf("server identity: read: %w", err)
	}
	if id := strings.TrimSpace(winner); id != "" {
		return id, nil
	}
	return minted, nil
}
