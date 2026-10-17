package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func TestStandaloneRefusesCustomerTokenBeforeCoreRPCOrMemoization(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var coreCalls atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		coreCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer core.Close()
	runtime, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: public, NodeURL: "http://worker.example.test", NodePrivateKey: private, AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close(context.Background()) }()
	for _, token := range []string{"e30.e30.customer-signature", "legacy-bearer", "", "one.two.three.four"} {
		for range 2 {
			if _, err := runtime.verifyToken(t.Context(), token); err == nil {
				t.Fatal("unscoped customer token accepted")
			}
		}
	}
	if coreCalls.Load() != 0 {
		t.Fatal("customer token was forwarded to core resolution")
	}
	runtime.mu.Lock()
	cached := len(runtime.tokens)
	runtime.mu.Unlock()
	if cached != 0 {
		t.Fatal("refused customer token was memoized")
	}
}
