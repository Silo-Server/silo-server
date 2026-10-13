package workeridentity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, key
}

func testRequest(t *testing.T, key ed25519.PrivateKey) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "https://core.example.test/source/a%2Fb?q=one%20two", strings.NewReader("payload"))
	r.Header.Set("Range", "bytes=4-9")
	r.Header.Set("If-Match", `"revision"`)
	r.Header.Set("Accept-Encoding", "identity")
	if err := Sign(r, "https://core.example.test", DomainNodeProof, key, 17, false); err != nil {
		t.Fatal(err)
	}
	return r
}

func testVerifier(pub ed25519.PublicKey) *Verifier {
	return &Verifier{Origin: "https://core.example.test", Domain: DomainNodeProof, ResolveKey: func(id int) (ed25519.PublicKey, error) { return pub, nil }}
}

func TestRequestProofRestoresBodyAndRejectsReplay(t *testing.T) {
	pub, key := testKeys(t)
	r := testRequest(t, key)
	body, _ := io.ReadAll(r.Body)
	if string(body) != "payload" {
		t.Fatalf("sign consumed body: %q", body)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	v := testVerifier(pub)
	if err := v.Verify(r, 17); err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(r.Body)
	if string(body) != "payload" {
		t.Fatalf("verify consumed body: %q", body)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := v.Verify(r, 17); err == nil {
		t.Fatal("replayed request accepted")
	}
}

func TestRequestProofBindsEveryAuthorityField(t *testing.T) {
	pub, key := testKeys(t)
	mutations := map[string]func(*http.Request, *Verifier){
		"method":          func(r *http.Request, v *Verifier) { r.Method = http.MethodPut },
		"escaped_path":    func(r *http.Request, v *Verifier) { r.URL.RawPath = "" },
		"query":           func(r *http.Request, v *Verifier) { r.URL.RawQuery = "q=one+two" },
		"body":            func(r *http.Request, v *Verifier) { r.Body = io.NopCloser(strings.NewReader("changed")) },
		"range":           func(r *http.Request, v *Verifier) { r.Header.Set("Range", "bytes=5-9") },
		"conditional":     func(r *http.Request, v *Verifier) { r.Header.Set("If-Match", `"other"`) },
		"encoding":        func(r *http.Request, v *Verifier) { r.Header.Set("Accept-Encoding", "gzip") },
		"audience":        func(r *http.Request, v *Verifier) { v.Origin = "https://other.example.test" },
		"domain":          func(r *http.Request, v *Verifier) { v.Domain = DomainCoreCommand },
		"node":            func(r *http.Request, v *Verifier) { r.Header.Set(HeaderNodeID, "18") },
		"timestamp":       func(r *http.Request, v *Verifier) { r.Header.Set(HeaderTimestamp, "1") },
		"nonce":           func(r *http.Request, v *Verifier) { r.Header.Set(HeaderNonce, "00000000000000000000000000000000") },
		"duplicate_range": func(r *http.Request, v *Verifier) { r.Header.Add("Range", "bytes=0-1") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r := testRequest(t, key)
			v := testVerifier(pub)
			mutate(r, v)
			if err := v.Verify(r, 17); err == nil {
				t.Fatal("altered authority accepted")
			}
		})
	}
}

func TestProofExpiryReplayCapacityAndConcurrentReplay(t *testing.T) {
	pub, key := testKeys(t)
	v := testVerifier(pub)
	v.Now = func() time.Time { return time.Now().Add(31 * time.Second) }
	if err := v.Verify(testRequest(t, key), 17); err == nil {
		t.Fatal("expired proof accepted")
	}
	v = testVerifier(pub)
	v.MaxNonces = 1
	if err := v.Verify(testRequest(t, key), 17); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(testRequest(t, key), 17); err == nil {
		t.Fatal("live replay cache overflow evicted a nonce")
	}
	v = testVerifier(pub)
	r := testRequest(t, key)
	payload, _ := io.ReadAll(r.Body)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := r.Clone(r.Context())
			copy.Body = io.NopCloser(bytes.NewReader(payload))
			if v.Verify(copy, 17) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("concurrent replay accepted %d times", accepted.Load())
	}
}

func TestRequestBodyLimitRestoresRejectedInput(t *testing.T) {
	_, key := testKeys(t)
	body := strings.Repeat("x", MaxBodyBytes+10)
	r := httptest.NewRequest(http.MethodPost, "https://core.example.test/", strings.NewReader(body))
	if err := Sign(r, "https://core.example.test", DomainNodeProof, key, 17, false); err == nil {
		t.Fatal("oversized body signed")
	}
	got, err := io.ReadAll(r.Body)
	if err != nil || string(got) != body {
		t.Fatalf("rejected body not restored: bytes=%d err=%v", len(got), err)
	}
}

func TestTypedGrantRejectsCrossScopeAndExpiry(t *testing.T) {
	pub, key := testKeys(t)
	now := time.Now()
	g := Grant{Version: 1, Domain: DomainSourceGrant, Audience: "https://source.example.test", NodeID: 17, WorkID: "work", Generation: 4, ExpiresAt: now.Add(time.Minute).Unix(), Body: json.RawMessage(`{"source":"id"}`)}
	token, err := EncodeGrant(g, key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeGrant(token, DomainSourceGrant, g.Audience, pub, now)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.NodeID != 17 || decoded.Generation != 4 || decoded.WorkID != "work" {
		t.Fatalf("lost grant scope: %+v", decoded)
	}
	if _, err = DecodeGrant(token, DomainOutputGrant, g.Audience, pub, now); err == nil {
		t.Fatal("cross-domain grant accepted")
	}
	if _, err = DecodeGrant(token, DomainSourceGrant, "https://other.example.test", pub, now); err == nil {
		t.Fatal("wrong grant audience accepted")
	}
	if _, err = DecodeGrant(token, DomainSourceGrant, g.Audience, pub, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired grant accepted")
	}
	parts := strings.Split(token, ".")
	parts[0] = "e30"
	if _, err = DecodeGrant(strings.Join(parts, "."), DomainSourceGrant, g.Audience, pub, now); err == nil {
		t.Fatal("altered grant accepted")
	}
}

func TestStrictBindingAndPrivatePersistence(t *testing.T) {
	pub, key := testKeys(t)
	b := Binding{Version: 1, NodeID: 17, CoreURL: "https://core.example.test", CorePublicKey: pub, NodePrivateKey: key, NodeURL: "https://worker.example.test"}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.TrimSuffix(string(data), "}") + `,"database_url":"legacy"}`
	if _, err = DecodeBinding(strings.NewReader(bad)); err == nil {
		t.Fatal("credential-bearing binding accepted")
	}
	if _, err = DecodeBinding(strings.NewReader(string(data) + "{}")); err == nil {
		t.Fatal("trailing binding accepted")
	}
	for _, url := range []string{"http://core.example.test", "https://user:pass@core.example.test", "https://core.example.test/?token=x", "https://core.example.test/#fragment"} {
		changed := b
		changed.CoreURL = url
		if err := changed.Validate(); err == nil {
			t.Fatalf("invalid URL accepted: %s", url)
		}
	}
	changed := b
	changed.CorePublicKey = make([]byte, ed25519.PublicKeySize)
	if changed.Validate() == nil {
		t.Fatal("zero public key accepted")
	}
	changed = b
	changed.NodePrivateKey = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if changed.Validate() == nil {
		t.Fatal("zero private seed accepted")
	}
	path := filepath.Join(t.TempDir(), "binding.json")
	if err = SaveBinding(path, b); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadBinding(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadBinding(path); err == nil {
		t.Fatal("publicly readable binding accepted")
	}
}

func TestHTTPClientRejectsRedirectAndUntrustedTLS(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := NewHTTPClient(true, time.Second)
	if response, err := client.Get(redirect.URL); err == nil {
		_ = response.Body.Close()
		t.Fatal("redirect followed")
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect target reached")
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsServer.Close()
	if response, err := client.Get(tlsServer.URL); err == nil {
		_ = response.Body.Close()
		t.Fatal("untrusted TLS accepted")
	}
	if response, err := NewHTTPClient(false, time.Second).Get(target.URL); err == nil {
		_ = response.Body.Close()
		t.Fatal("implicit HTTP accepted")
	}
}

func TestSignedRequestSurvivesHTTPTransport(t *testing.T) {
	pub, key := testKeys(t)
	var verifier *Verifier
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := verifier.Verify(r, 17); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	verifier = &Verifier{Origin: server.URL, Domain: DomainNodeProof, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return pub, nil }}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/work?q=one%20two", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err = Sign(req, server.URL, DomainNodeProof, key, 17, true); err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("transport changed signed request: %d %s", response.StatusCode, data)
	}
}

func TestBindingRejectsSmallOrderKeysAndInconsistentPrivateKeys(t *testing.T) {
	public, key := testKeys(t)
	b := Binding{Version: 1, NodeID: 17, CoreURL: "https://core.example.test", CorePublicKey: public, NodePrivateKey: key, NodeURL: "https://worker.example.test"}
	for _, encoded := range []string{"0100000000000000000000000000000000000000000000000000000000000000", "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"} {
		point, _ := hex.DecodeString(encoded)
		for _, sign := range []byte{0, 0x80} {
			changed := b
			candidate := append(ed25519.PublicKey(nil), point...)
			candidate[31] |= sign
			changed.CorePublicKey = candidate
			if changed.Validate() == nil {
				t.Fatal("weak or noncanonical public key accepted")
			}
		}
	}
	changed := b
	changed.NodePrivateKey = append(ed25519.PrivateKey(nil), key...)
	changed.NodePrivateKey[63] ^= 1
	if changed.Validate() == nil {
		t.Fatal("private key with inconsistent public half accepted")
	}
}

func TestControlGrantAllowsEmptyWorkScopeButHasExclusiveExpiry(t *testing.T) {
	public, key := testKeys(t)
	now := time.Now()
	g := Grant{Version: 1, Domain: DomainSnapshotGrant, Audience: "https://worker.example.test", NodeID: 17, ExpiresAt: now.Add(time.Minute).Unix(), Body: json.RawMessage(`{"sequence":1}`)}
	token, err := EncodeGrant(g, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeGrant(token, g.Domain, g.Audience, public, now); err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeGrant(token, g.Domain, g.Audience, public, time.Unix(g.ExpiresAt, 0)); err == nil {
		t.Fatal("grant accepted at exclusive expiry boundary")
	}
}
