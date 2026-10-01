package serveridentity

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type memStore struct {
	mu     sync.Mutex
	values map[string]string
	getErr error
	sets   int
}

func (m *memStore) Get(_ context.Context, key string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[key], nil
}

func (m *memStore) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[key] = value
	m.sets++
	return nil
}

// conditionalMemStore adds the insert-if-absent surface the Postgres repo has.
type conditionalMemStore struct{ memStore }

func (m *conditionalMemStore) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]string{}
	}
	if m.values[key] != "" {
		return false, nil
	}
	m.values[key] = value
	m.sets++
	return true, nil
}

func TestEnsureMintsOnceAndKeepsValue(t *testing.T) {
	store := &conditionalMemStore{}
	first, err := Ensure(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("minted id %q is not a UUID: %v", first, err)
	}
	second, err := Ensure(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if second != first || store.sets != 1 {
		t.Fatalf("second read = %q (sets %d), want %q once", second, store.sets, first)
	}
}

func TestEnsureConcurrentProcessesConverge(t *testing.T) {
	store := &conditionalMemStore{}
	const n = 32
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := Ensure(context.Background(), store)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("processes disagree: %q vs %q", id, ids[0])
		}
	}
	if store.sets != 1 {
		t.Fatalf("sets = %d, want exactly one winner", store.sets)
	}
}

func TestEnsureFallsBackToSetWithoutConditionalStore(t *testing.T) {
	store := &memStore{}
	id, err := Ensure(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if store.values[Key] != id || store.sets != 1 {
		t.Fatalf("stored %q after %d sets, want %q once", store.values[Key], store.sets, id)
	}
}

func TestEnsureTrimsStoredValue(t *testing.T) {
	store := &memStore{values: map[string]string{Key: "  existing-id \n"}}
	id, err := Ensure(context.Background(), store)
	if err != nil || id != "existing-id" || store.sets != 0 {
		t.Fatalf("id = %q err = %v sets = %d", id, err, store.sets)
	}
}

func TestServiceCachesAndReportsUnavailable(t *testing.T) {
	if _, err := New(nil).ServerID(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil store err = %v, want ErrUnavailable", err)
	}
	failing := &memStore{getErr: errors.New("db down")}
	svc := New(failing)
	if _, err := svc.ServerID(context.Background()); err == nil {
		t.Fatal("expected read error")
	}
	// A failed read caches nothing: the next call retries the store.
	failing.getErr = nil
	first, err := svc.ServerID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failing.getErr = errors.New("db down again")
	second, err := svc.ServerID(context.Background())
	if err != nil || second != first {
		t.Fatalf("cached read = %q err = %v, want %q", second, err, first)
	}
}
