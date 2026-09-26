package clientip

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
)

type orderedTrustStore struct {
	fakeStore
	mu      sync.Mutex
	calls   int
	first   chan struct{}
	release chan struct{}
}

func (s *orderedTrustStore) Get(context.Context, string) (string, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.first)
		<-s.release
		return "192.0.2.0/24", nil
	}
	return "198.51.100.0/24", nil
}
func TestReloadTrustedCIDRsSerializesReadAndApply(t *testing.T) {
	r := NewResolver(nil)
	store := &orderedTrustStore{first: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 2)
	go func() { done <- r.ReloadTrustedCIDRs(t.Context(), store) }()
	<-store.first
	if r.reloadMu.TryLock() {
		r.reloadMu.Unlock()
		close(store.release)
		<-done
		t.Fatal("reload lock not held across store read")
	}
	go func() { done <- r.ReloadTrustedCIDRs(t.Context(), store) }()
	close(store.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.2:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.1")
	if got := r.ClientIP(request); got != "192.0.2.2" {
		t.Fatal("older trust snapshot won", got)
	}
	request.RemoteAddr = "198.51.100.2:1234"
	if got := r.ClientIP(request); got != "203.0.113.1" {
		t.Fatal("latest trust snapshot missing", got)
	}
}

type failedTrustStore struct{ fakeStore }

func (*failedTrustStore) Get(context.Context, string) (string, error) {
	return "", errors.New("read unavailable")
}
func TestReloadTrustedCIDRsKeepsLastValidStateOnFailure(t *testing.T) {
	cidrs, _ := ParseCIDRs("192.0.2.0/24")
	r := NewResolver(cidrs)
	if r.ReloadTrustedCIDRs(t.Context(), &failedTrustStore{}) == nil {
		t.Fatal("failure hidden")
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.2:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.1")
	if got := r.ClientIP(request); got != "203.0.113.1" {
		t.Fatal("last valid trust lost", got)
	}
}
