// Package artworkstoretest provides a small in-memory Store for package tests.
package artworkstoretest

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const memoryETag = "\"memory\""

type Call struct{ Method, Key string }

type Memory struct {
	Mu      sync.Mutex
	Objects map[string][]byte
	Calls   []Call
}

func New() *Memory { return &Memory{Objects: make(map[string][]byte)} }
func (m *Memory) Put(_ context.Context, key string, data []byte) error {
	if err := artworkstore.ValidateKey(key); err != nil {
		return err
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.Objects[key] = append([]byte(nil), data...)
	m.Calls = append(m.Calls, Call{"put", key})
	return nil
}
func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, artworkstore.ObjectInfo, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	data, ok := m.Objects[key]
	if !ok {
		return nil, artworkstore.ObjectInfo{}, artworkstore.ErrNotFound
	}
	m.Calls = append(m.Calls, Call{"get", key})
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), artworkstore.ObjectInfo{Key: key, Size: int64(len(data)), ModTime: time.Unix(0, 0), ETag: memoryETag}, nil
}
func (m *Memory) Stat(_ context.Context, key string) (artworkstore.ObjectInfo, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	data, ok := m.Objects[key]
	if !ok {
		return artworkstore.ObjectInfo{}, artworkstore.ErrNotFound
	}
	return artworkstore.ObjectInfo{Key: key, Size: int64(len(data)), ModTime: time.Unix(0, 0), ETag: memoryETag}, nil
}
func (m *Memory) Delete(_ context.Context, keys []string) (int, error) {
	for _, key := range keys {
		if err := artworkstore.ValidateKey(key); err != nil {
			return 0, err
		}
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	n := 0
	for _, key := range keys {
		delete(m.Objects, key)
		n++
		m.Calls = append(m.Calls, Call{"delete", key})
	}
	return n, nil
}
func (m *Memory) DeletePrefix(_ context.Context, prefix string) (int, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if err := artworkstore.ValidateKey(prefix); err != nil {
		return 0, err
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	keys := make([]string, 0)
	for key := range m.Objects {
		if strings.HasPrefix(key, prefix+"/") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		delete(m.Objects, key)
		m.Calls = append(m.Calls, Call{"delete", key})
	}
	return len(keys), nil
}
func (m *Memory) List(_ context.Context, prefix, cursor string, limit int) ([]artworkstore.ObjectInfo, string, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	m.Mu.Lock()
	keys := make([]string, 0)
	for key := range m.Objects {
		if (prefix == "" || strings.HasPrefix(key, prefix+"/")) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]artworkstore.ObjectInfo, 0)
	next := ""
	for _, key := range keys {
		if limit > 0 && len(out) >= limit {
			next = out[len(out)-1].Key
			break
		}
		out = append(out, artworkstore.ObjectInfo{Key: key, Size: int64(len(m.Objects[key])), ModTime: time.Unix(0, 0), ETag: memoryETag})
	}
	m.Mu.Unlock()
	return out, next, nil
}
func (m *Memory) Probe(context.Context) error { return nil }
func (m *Memory) Identity() string            { return "memory" }

var _ artworkstore.Store = (*Memory)(nil)
