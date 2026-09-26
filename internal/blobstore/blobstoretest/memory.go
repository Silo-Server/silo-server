// Package blobstoretest provides a small in-memory Store for package tests.
package blobstoretest

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
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
	if err := blobstore.ValidateKey(key); err != nil {
		return err
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.Objects[key] = append([]byte(nil), data...)
	m.Calls = append(m.Calls, Call{"put", key})
	return nil
}
func (m *Memory) PutStream(ctx context.Context, key string, r io.Reader, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return m.Put(ctx, key, data)
}
func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	data, ok := m.Objects[key]
	if !ok {
		return nil, blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	m.Calls = append(m.Calls, Call{"get", key})
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), blobstore.ObjectInfo{Key: key, Size: int64(len(data)), ModTime: time.Unix(0, 0), ETag: memoryETag}, nil
}
func (m *Memory) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	data, ok := m.Objects[key]
	if !ok {
		return blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return blobstore.ObjectInfo{Key: key, Size: int64(len(data)), ModTime: time.Unix(0, 0), ETag: memoryETag}, nil
}
func (m *Memory) Delete(_ context.Context, keys []string) (int, error) {
	for _, key := range keys {
		if err := blobstore.ValidateKey(key); err != nil {
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
	if err := blobstore.ValidateKey(prefix); err != nil {
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
func (m *Memory) List(_ context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	m.Mu.Lock()
	keys := make([]string, 0)
	for key := range m.Objects {
		if (prefix == "" || strings.HasPrefix(key, prefix+"/")) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]blobstore.ObjectInfo, 0)
	next := ""
	for _, key := range keys {
		if limit > 0 && len(out) >= limit {
			next = out[len(out)-1].Key
			break
		}
		out = append(out, blobstore.ObjectInfo{Key: key, Size: int64(len(m.Objects[key])), ModTime: time.Unix(0, 0), ETag: memoryETag})
	}
	m.Mu.Unlock()
	return out, next, nil
}
func (m *Memory) Probe(context.Context) error { return nil }
func (m *Memory) Identity() string            { return "memory" }

var _ blobstore.Store = (*Memory)(nil)
