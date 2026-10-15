package nodeauth

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testNodeKey = "snk1_7_bm9kZS1rZXktZm9yLXRlc3RzLW9ubHktMzItYnl0ZXM"

// testKeys is a NodeKeys over a fixed map. reloaded, when set, is the map a
// reload switches to; reloads counts ReloadNodeKey calls.
type testKeys struct {
	keys, reloaded map[int]string
	reloads        *int
}

func testKeyFor(keys map[int]string) testKeys { return testKeys{keys: keys} }

func (k testKeys) NodeKey(id int) (string, error) {
	if key, ok := k.keys[id]; ok {
		return key, nil
	}
	return "", errors.New("unknown node")
}

func (k testKeys) ReloadNodeKey(id int) (string, error) {
	if k.reloads != nil {
		*k.reloads++
	}
	if k.reloaded != nil {
		k.keys = k.reloaded
	}
	return k.NodeKey(id)
}

// nodeSignedServerRequest signs a request as node 7 to a core at coreURL, then
// rebuilds it the way the core receives it.
func nodeSignedServerRequest(t *testing.T, method, coreURL, target string, body []byte, signedAt time.Time) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	client, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := signToCore(client, coreURL, 7, testNodeKey, signedAt); err != nil {
		t.Fatalf("sign: %v", err)
	}
	server := httptest.NewRequest(method, client.URL.RequestURI(), bytes.NewReader(body))
	server.Header.Set("Authorization", client.Header.Get("Authorization"))
	return server
}

func TestNodeToCoreRoundTrip(t *testing.T) {
	keys := testKeyFor(map[int]string{7: testNodeKey})
	for _, tc := range []struct {
		name, method, target string
		body                 []byte
	}{
		{"get", http.MethodGet, "http://core.test/api/internal/nodes/self/config", nil},
		{"query", http.MethodGet, "http://core.test/api/internal/nodes/sources/abc?x=a%20b", nil},
		{"body", http.MethodPost, "http://core.test/api/internal/nodes/self/report", []byte(`{"seq":1}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := nodeSignedServerRequest(t, tc.method, "http://core.test", tc.target, tc.body, testNow)
			if !strings.Contains(r.Header.Get("Authorization"), "n=7,") {
				t.Fatalf("Authorization %q does not name the node", r.Header.Get("Authorization"))
			}
			if strings.Contains(r.Header.Get("Authorization"), testNodeKey) {
				t.Fatal("Authorization carries the node key")
			}
			id, err := verifyFromNode(r, keys, testNow)
			if err != nil || id != 7 {
				t.Fatalf("verifyFromNode = %d, %v; want node 7", id, err)
			}
			got, _ := io.ReadAll(r.Body)
			if !bytes.Equal(got, tc.body) {
				t.Fatalf("handler body = %q, want %q", got, tc.body)
			}
		})
	}
}

// A request captured in one direction must never verify in the other, even
// though both directions use the same input key for a transcode node.
func TestSignatureDirectionsDoNotCrossVerify(t *testing.T) {
	keys := testKeyFor(map[int]string{7: testNodeKey})

	// Core to node, replayed at the core as if the node had sent it.
	coreSigned := signedRequestWithKey(t, testNodeKey)
	params := strings.TrimPrefix(coreSigned.Header.Get("Authorization"), Scheme+" ")
	coreSigned.Header.Set("Authorization", Scheme+" n=7,"+params)
	if _, err := verifyFromNode(coreSigned, keys, testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("core-to-node signature verified as node-to-core: %v", err)
	}

	// Node to core, replayed at the node.
	nodeSigned := nodeSignedServerRequest(t, http.MethodGet, "http://node.test", "http://node.test/status", nil, testNow)
	if err := verify(nodeSigned, testNodeKey, testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("node-to-core signature verified as core-to-node: %v", err)
	}
	if err := verifySignedAt(nodeSigned, testNodeKey, testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("node-to-core signature passed VerifySigned: %v", err)
	}
}

func signedRequestWithKey(t *testing.T, key string) *http.Request {
	t.Helper()
	client, err := http.NewRequest(http.MethodGet, "http://node.test/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sign(client, "http://node.test", key, testNow); err != nil {
		t.Fatal(err)
	}
	if err := verify(httptestRequestFrom(client), key, testNow); err != nil {
		t.Fatalf("control: core-to-node signature does not verify: %v", err)
	}
	return httptestRequestFrom(client)
}

func httptestRequestFrom(client *http.Request) *http.Request {
	server := httptest.NewRequest(client.Method, client.URL.RequestURI(), nil)
	server.Header.Set("Authorization", client.Header.Get("Authorization"))
	return server
}

func TestVerifyFromNodeRejectsBadRequests(t *testing.T) {
	keys := testKeyFor(map[int]string{7: testNodeKey, 8: "snk1_8_other-node-key"})
	fresh := func() *http.Request {
		return nodeSignedServerRequest(t, http.MethodPost, "http://core.test", "http://core.test/api/internal/nodes/self/report", []byte(`{"seq":1}`), testNow)
	}

	for name, mutate := range map[string]func(*http.Request){
		"unsigned": func(r *http.Request) { r.Header.Del("Authorization") },
		"bearer":   func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testNodeKey) },
		"no node": func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "n=7,", "", 1))
		},
		"unknown node": func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "n=7,", "n=9,", 1))
		},
		"another node": func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "n=7,", "n=8,", 1))
		},
		"padded node": func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "n=7,", "n=07,", 1))
		},
		"other path":    func(r *http.Request) { r.URL.Path = "/api/internal/nodes/self/logs" },
		"added query":   func(r *http.Request) { r.URL.RawQuery = "x=1" },
		"other method":  func(r *http.Request) { r.Method = http.MethodPut },
		"altered body":  func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"seq":2}`)) },
		"oversize body": func(r *http.Request) { r.ContentLength = maxBodyBytes + 1 },
	} {
		r := fresh()
		mutate(r)
		if id, err := verifyFromNode(r, keys, testNow); !errors.Is(err, ErrUnauthorized) || id != 0 {
			t.Errorf("%s: verifyFromNode = %d, %v; want ErrUnauthorized", name, id, err)
		}
	}

	// The same 30-second window as core-to-node requests.
	for _, offset := range []time.Duration{-31 * time.Second, 31 * time.Second} {
		r := nodeSignedServerRequest(t, http.MethodGet, "http://core.test", "http://core.test/api/internal/nodes/self/config", nil, testNow.Add(offset))
		if _, err := verifyFromNode(r, keys, testNow); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("signed %s from now: %v, want ErrUnauthorized", offset, err)
		}
	}
	for _, offset := range []time.Duration{-30 * time.Second, 30 * time.Second} {
		r := nodeSignedServerRequest(t, http.MethodGet, "http://core.test", "http://core.test/api/internal/nodes/self/config", nil, testNow.Add(offset))
		if _, err := verifyFromNode(r, keys, testNow); err != nil {
			t.Errorf("signed %s from now: %v, want accepted", offset, err)
		}
	}
}

// Like Verify, the core never reads the body of a request whose MAC fails, so
// a caller without the node's key cannot make it buffer one.
func TestVerifyFromNodeChecksTheSignatureBeforeReadingTheBody(t *testing.T) {
	r := nodeSignedServerRequest(t, http.MethodPost, "http://core.test", "http://core.test/api/internal/nodes/self/logs", []byte(`[]`), testNow)
	body := &countingReader{}
	r.Body = io.NopCloser(body)
	if _, err := verifyFromNode(r, testKeyFor(map[int]string{7: "snk1_7_a-different-key"}), testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("verifyFromNode = %v, want ErrUnauthorized", err)
	}
	if body.read != 0 {
		t.Fatalf("read %d body bytes before checking the signature", body.read)
	}
}

// The node signs its path relative to the core URL it was given, so a core
// behind a prefix-stripping reverse proxy verifies the path it serves.
func TestNodeToCorePathIsRelativeToTheCoreURL(t *testing.T) {
	keys := testKeyFor(map[int]string{7: testNodeKey})
	for _, tc := range []struct{ coreURL, target string }{
		{"https://gateway.test/silo", "https://gateway.test/silo/api/internal/nodes/self/config"},
		{"https://gateway.test/silo/", "https://gateway.test/silo/api/internal/nodes/self/config"},
		{"https://gateway.test", "https://gateway.test/api/internal/nodes/self/config"},
	} {
		client, err := http.NewRequest(http.MethodGet, tc.target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := signToCore(client, tc.coreURL, 7, testNodeKey, testNow); err != nil {
			t.Fatal(err)
		}
		core := httptest.NewRequest(http.MethodGet, "/api/internal/nodes/self/config", nil)
		core.Header.Set("Authorization", client.Header.Get("Authorization"))
		if _, err := verifyFromNode(core, keys, testNow); err != nil {
			t.Errorf("core URL %s: %v", tc.coreURL, err)
		}
	}
}

func TestSignToCoreRequiresANodeIdentity(t *testing.T) {
	for _, tc := range []struct {
		id  int
		key string
	}{{0, testNodeKey}, {-1, testNodeKey}, {7, ""}} {
		req := httptest.NewRequest(http.MethodGet, "/api/internal/nodes/self/config", nil)
		if err := SignToCore(req, "http://core.test", tc.id, tc.key); !errors.Is(err, ErrNoNodeIdentity) || req.Header.Get("Authorization") != "" {
			t.Errorf("SignToCore(%d, %q) = %v", tc.id, tc.key, err)
		}
	}
}

type fixedKeyring struct {
	key string
	err error
}

func (k fixedKeyring) KeyFor(target Target, _ string) (string, error) {
	if target != TargetTranscode {
		return "", ErrUnknownNode
	}
	return k.key, k.err
}

func TestSignWithUsesTheKeyringKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	if err := SignTranscode(req, "http://node.test", fixedKeyring{key: testNodeKey}); err != nil {
		t.Fatal(err)
	}
	if err := VerifySigned(req, testNodeKey); err != nil {
		t.Fatalf("a request signed with the node key does not verify with it: %v", err)
	}
	if err := VerifySigned(req, testSecret); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a node-key signature verified with another key: %v", err)
	}

	for _, keyErr := range []error{ErrNoNodeKey, ErrUnknownNode} {
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		if err := SignTranscode(req, "http://node.test", fixedKeyring{err: keyErr}); !errors.Is(err, keyErr) || req.Header.Get("Authorization") != "" {
			t.Fatalf("SignWith with %v = %v, Authorization %q", keyErr, err, req.Header.Get("Authorization"))
		}
	}
	for _, keys := range []Keyring{nil, sharedSecret("")} {
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		if err := SignTranscode(req, "http://node.test", keys); !errors.Is(err, ErrNoSecret) || req.Header.Get("Authorization") != "" {
			t.Fatalf("SignTranscode(%v) = %v, Authorization %q; want ErrNoSecret and no signature", keys, err, req.Header.Get("Authorization"))
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	if err := SignProxy(req, "http://node.test", sharedSecret(testSecret)); err != nil || Verify(req, testSecret) != nil {
		t.Fatalf("a shared-secret keyring does not sign like sign: %v", err)
	}
}

// VerifySigned is Verify without the legacy bearer: a node verifying with its
// own key never accepts that key sent in clear.
func TestVerifySignedRefusesTheLegacyBearer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	r.Header.Set("Authorization", "Bearer "+testNodeKey)
	if err := VerifySigned(r, testNodeKey); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("VerifySigned accepted a bearer: %v", err)
	}
	if err := VerifySigned(r, ""); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("VerifySigned without a key = %v, want ErrNoSecret", err)
	}
}

// SignWith asks the keyring for the key of the kind of worker the caller
// names, so a proxy credential is never requested for a transcode node.
func TestSignWithAsksForTheTargetKind(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	if err := SignProxy(req, "http://node.test", fixedKeyring{key: testNodeKey}); !errors.Is(err, ErrUnknownNode) || req.Header.Get("Authorization") != "" {
		t.Fatalf("SignProxy with a transcode-only keyring = %v, Authorization %q", err, req.Header.Get("Authorization"))
	}
}

// A known node whose signature fails against the loaded key is checked once
// more after a reload: it may hold a key rotated since the last load.
func TestVerifyFromNodeRetriesOnceAfterAReload(t *testing.T) {
	r := nodeSignedServerRequest(t, http.MethodPost, "http://core.test", "http://core.test/api/internal/nodes/self/report", []byte(`{"seq":1}`), testNow)
	reloads := 0
	keys := testKeys{keys: map[int]string{7: "snk1_7_the-key-before-rotation"}, reloaded: map[int]string{7: testNodeKey}, reloads: &reloads}
	if id, err := verifyFromNode(r, keys, testNow); err != nil || id != 7 {
		t.Fatalf("verifyFromNode with a rotated key = %d, %v; want node 7 after a reload", id, err)
	}
	if reloads != 1 {
		t.Fatalf("reloaded %d times, want 1", reloads)
	}
	if body, _ := io.ReadAll(r.Body); string(body) != `{"seq":1}` {
		t.Fatalf("handler body = %q", body)
	}

	// A wrong key stays wrong after the one reload, and a matching key never
	// reloads.
	reloads = 0
	r = nodeSignedServerRequest(t, http.MethodGet, "http://core.test", "http://core.test/api/internal/nodes/self/config", nil, testNow)
	wrong := testKeys{keys: map[int]string{7: "snk1_7_wrong"}, reloads: &reloads}
	if _, err := verifyFromNode(r, wrong, testNow); !errors.Is(err, ErrUnauthorized) || reloads != 1 {
		t.Fatalf("wrong key: %v after %d reloads; want ErrUnauthorized after 1", err, reloads)
	}
	reloads = 0
	r = nodeSignedServerRequest(t, http.MethodGet, "http://core.test", "http://core.test/api/internal/nodes/self/config", nil, testNow)
	if _, err := verifyFromNode(r, testKeys{keys: map[int]string{7: testNodeKey}, reloads: &reloads}, testNow); err != nil || reloads != 0 {
		t.Fatalf("matching key: %v after %d reloads; want accepted without a reload", err, reloads)
	}
	// An unknown node never reloads: only a known node's key can have rotated.
	r = nodeSignedServerRequest(t, http.MethodGet, "http://core.test", "http://core.test/api/internal/nodes/self/config", nil, testNow)
	if _, err := verifyFromNode(r, testKeys{keys: map[int]string{}, reloads: &reloads}, testNow); !errors.Is(err, ErrUnauthorized) || reloads != 0 {
		t.Fatalf("unknown node: %v after %d reloads", err, reloads)
	}
}

// sharedSecret is a Keyring that signs for every worker with one key.
type sharedSecret string

func (s sharedSecret) KeyFor(Target, string) (string, error) { return string(s), nil }
