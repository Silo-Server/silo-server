package nodeauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSecret = "shared-secret-for-tests"

var testNow = time.Unix(1_760_000_000, 0)

// signedServerRequest signs a client request, then rebuilds it the way a
// server receives it: from the request line and body bytes on the wire.
func signedServerRequest(t *testing.T, method, target string, body []byte, signedAt time.Time) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	client, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := sign(client, "http://node.test", testSecret, signedAt); err != nil {
		t.Fatalf("sign: %v", err)
	}
	server := httptest.NewRequest(method, client.URL.RequestURI(), bytes.NewReader(body))
	server.Header.Set("Authorization", client.Header.Get("Authorization"))
	return server
}

func TestSignNeverSendsTheSecret(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://node.test/hw-capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sign(req, "http://node.test", testSecret); err != nil {
		t.Fatal(err)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, Scheme+" ") {
		t.Fatalf("Authorization = %q, want %s scheme", auth, Scheme)
	}
	if strings.Contains(auth, testSecret) {
		t.Fatal("Authorization carries the shared secret")
	}
}

func TestVerifyAcceptsSignedRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, target string
		body                 []byte
	}{
		{"get", http.MethodGet, "http://node.test/transcode/abc/segment/3.m4s", nil},
		{"query", http.MethodGet, "http://node.test/transcode/abc/master.m3u8?source_timeline=1&x=a%20b", nil},
		{"escaped path", http.MethodDelete, "http://node.test/downloads/artifacts/a%2Fb", nil},
		{"root", http.MethodGet, "http://node.test", nil},
		{"body", http.MethodPost, "http://node.test/transcode/start", []byte(`{"session_id":"abc"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := signedServerRequest(t, tc.method, tc.target, tc.body, testNow)
			if err := verify(r, testSecret, testNow); err != nil {
				t.Fatalf("verify: %v", err)
			}
			got, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.body) {
				t.Fatalf("handler body = %q, want %q", got, tc.body)
			}
		})
	}
}

func TestVerifyRejectsAlteredRequests(t *testing.T) {
	body := []byte(`{"session_id":"abc"}`)
	for _, tc := range []struct {
		name  string
		alter func(r *http.Request)
	}{
		{"method", func(r *http.Request) { r.Method = http.MethodPut }},
		{"path", func(r *http.Request) { r.URL.Path = "/transcode/other" }},
		{"query", func(r *http.Request) { r.URL.RawQuery = "x=1" }},
		{"body", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"session_id":"xyz"}`)) }},
		{"signature", func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "sig=", "sig=00", 1))
		}},
		{"timestamp", func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "t=1760000000", "t=1760000001", 1))
		}},
		{"claimed body hash", func(r *http.Request) {
			// A correct hash of a different body, with the original signature.
			other := sha256.Sum256([]byte(`{"session_id":"xyz"}`))
			auth := r.Header.Get("Authorization")
			start := strings.Index(auth, "b=") + len("b=")
			r.Header.Set("Authorization", auth[:start]+hex.EncodeToString(other[:])+auth[start+64:])
			r.Body = io.NopCloser(strings.NewReader(`{"session_id":"xyz"}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := signedServerRequest(t, http.MethodPost, "http://node.test/transcode/start", body, testNow)
			tc.alter(r)
			if err := verify(r, testSecret, testNow); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("verify = %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestVerifyEnforcesTheValidityWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset time.Duration
		ok     bool
	}{
		{"within past", -maxSkew, true},
		{"within future", maxSkew, true},
		{"too old", -maxSkew - time.Second, false},
		{"too far ahead", maxSkew + time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := signedServerRequest(t, http.MethodGet, "http://node.test/status", nil, testNow.Add(tc.offset))
			err := verify(r, testSecret, testNow)
			if tc.ok && err != nil {
				t.Fatalf("verify: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("verify = %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestVerifyRejectsAnotherSecret(t *testing.T) {
	r := signedServerRequest(t, http.MethodGet, "http://node.test/status", nil, testNow)
	if err := verify(r, "a-different-secret", testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("verify = %v, want ErrUnauthorized", err)
	}
}

func TestVerifyRejectsOversizedBodies(t *testing.T) {
	body := bytes.Repeat([]byte("x"), maxBodyBytes+1)
	r := signedServerRequest(t, http.MethodPost, "http://node.test/transcode/start", body, testNow)
	if err := verify(r, testSecret, testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("verify = %v, want ErrUnauthorized", err)
	}
}

// A caller without the secret must not be able to make the worker read, let
// alone buffer, a request body.
func TestVerifyChecksTheSignatureBeforeReadingTheBody(t *testing.T) {
	body := &countingReader{}
	r := httptest.NewRequest(http.MethodPost, "/admin/reload-config", body)
	r.Header.Set("Authorization", Scheme+" t=1760000000,b="+hex.EncodeToString(emptyBodyHash[:])+",sig="+strings.Repeat("00", 32))
	if err := verify(r, testSecret, testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("verify = %v, want ErrUnauthorized", err)
	}
	if body.read != 0 {
		t.Fatalf("read %d body bytes before the signature was checked", body.read)
	}
}

type countingReader struct{ read int }

func (c *countingReader) Read(p []byte) (int, error) {
	n := min(len(p), 1<<10)
	c.read += n
	return n, nil
}

func TestVerifyRequiresACredential(t *testing.T) {
	for _, auth := range []string{
		"",
		"Bearer",
		"Bearer ",
		"Bearer wrong",
		Scheme,
		Scheme + " t=1760000000",
		Scheme + " sig=00",
		Scheme + " t=1760000000,sig=" + strings.Repeat("00", 32),
		Scheme + " t=now,b=" + hex.EncodeToString(emptyBodyHash[:]) + ",sig=" + strings.Repeat("00", 32),
		Scheme + " t=1760000000,b=" + hex.EncodeToString(emptyBodyHash[:]) + ",sig=zz",
		Scheme + " t=1760000000,b=00,sig=" + strings.Repeat("00", 32),
		"Basic " + testSecret,
	} {
		r := httptest.NewRequest(http.MethodGet, "/status", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if err := verify(r, testSecret, testNow); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Authorization %q: verify = %v, want ErrUnauthorized", auth, err)
		}
	}
}

func TestNoSecretSignsAndVerifiesNothing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	if err := Sign(req, "http://node.test", ""); err != nil || req.Header.Get("Authorization") != "" {
		t.Fatalf("Sign = %v with Authorization %q, want an unsigned request", err, req.Header.Get("Authorization"))
	}
	req.Header.Set("Authorization", "Bearer ")
	if err := Verify(req, ""); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("Verify = %v, want ErrNoSecret", err)
	}
}

// The legacy bearer keeps older API servers and proxies working during a
// rolling upgrade. Remove with the legacy bearer in Verify.
func TestVerifyAcceptsTheLegacyBearerForOneRelease(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	r.Header.Set("Authorization", "Bearer "+testSecret)
	if err := verify(r, testSecret, testNow); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// The MAC key must not be the shared secret itself, or a signature would be
// an HMAC under the key that also signs login and stream tokens.
func TestMACKeyIsDerivedFromTheSecret(t *testing.T) {
	got, err := requestMAC(testSecret, "1760000000", http.MethodGet, "/status", "", hex.EncodeToString(emptyBodyHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	direct := hmac.New(sha256.New, []byte(testSecret))
	_, _ = io.WriteString(direct, strings.Join([]string{canonicalVersion, "1760000000", http.MethodGet, "/status", "", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}, "\n"))
	if hmac.Equal(got, direct.Sum(nil)) {
		t.Fatal("MAC uses the shared secret directly")
	}
}

func TestSignKeepsUnrewindableBodiesReadable(t *testing.T) {
	body := `{"artifact_id":"a"}`
	req, err := http.NewRequest(http.MethodPost, "http://node.test/downloads/prepare", io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("test needs a body without GetBody")
	}
	if err := sign(req, "http://node.test", testSecret, testNow); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body || req.ContentLength != int64(len(body)) {
		t.Fatalf("body = %q (length %d), want %q", got, req.ContentLength, body)
	}
}

// TestSignedRequestRoundTrip sends a signed request over a real connection, so
// the path, query, and body are the bytes a server actually receives.
func TestSignedRequestRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := Verify(r, testSecret); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/transcode/a%20b/segment/1.ts/downloaded?generation=x%2By", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Sign(req, server.URL, testSecret); err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(got) != "payload" {
		t.Fatalf("status %d body %q, want 200 payload", resp.StatusCode, got)
	}
}

func TestLogThrottleCountsSuppressedWarnings(t *testing.T) {
	var throttle logThrottle
	start := time.Hour
	if suppressed, ok := throttle.allowAt(start); !ok || suppressed != 0 {
		t.Fatalf("first warning: allow = %d, %v; want 0, true", suppressed, ok)
	}
	for range 3 {
		if _, ok := throttle.allowAt(start + time.Minute); ok {
			t.Fatal("a warning inside the interval was allowed")
		}
	}
	// A concurrent caller that sampled the time before the last warning was
	// recorded is inside the interval too, and must not move it backwards.
	if _, ok := throttle.allowAt(start - time.Millisecond); ok {
		t.Fatal("an out-of-order earlier sample was allowed")
	}
	if suppressed, ok := throttle.allowAt(start + logInterval); !ok || suppressed != 4 {
		t.Fatalf("after the interval: allow = %d, %v; want 4, true", suppressed, ok)
	}
}

// A worker registered under a path prefix that its reverse proxy strips
// serves, and verifies, the path without the prefix.
func TestSignedPathIsRelativeToTheNodeURL(t *testing.T) {
	for _, tc := range []struct {
		name, nodeURL, target, workerSees string
	}{
		{"no prefix", "http://gateway.test", "http://gateway.test/transcode/abc/master.m3u8?x=1", "/transcode/abc/master.m3u8?x=1"},
		{"prefix", "http://gateway.test/node1", "http://gateway.test/node1/transcode/abc/master.m3u8?x=1", "/transcode/abc/master.m3u8?x=1"},
		{"prefix with trailing slash", "http://gateway.test/node1/", "http://gateway.test/node1/status", "/status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := http.NewRequest(http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := sign(client, tc.nodeURL, testSecret, testNow); err != nil {
				t.Fatal(err)
			}
			worker := httptest.NewRequest(http.MethodGet, tc.workerSees, nil)
			worker.Header.Set("Authorization", client.Header.Get("Authorization"))
			if err := verify(worker, testSecret, testNow); err != nil {
				t.Fatalf("verify: %v", err)
			}
		})
	}
	// The prefix is not a wildcard: a different route still fails.
	client, err := http.NewRequest(http.MethodGet, "http://gateway.test/node1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sign(client, "http://gateway.test/node1", testSecret, testNow); err != nil {
		t.Fatal(err)
	}
	worker := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
	worker.Header.Set("Authorization", client.Header.Get("Authorization"))
	if err := verify(worker, testSecret, testNow); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("verify = %v, want ErrUnauthorized", err)
	}
}

// A worker behind a reverse proxy that strips the path prefix of its
// registered URL still verifies the request, over a real connection.
func TestSignedRequestThroughAPrefixStrippingProxy(t *testing.T) {
	worker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := Verify(r, testSecret); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, r.URL.Path)
	})
	server := httptest.NewServer(http.StripPrefix("/node1", worker))
	defer server.Close()

	nodeURL := server.URL + "/node1"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, nodeURL+"/transcode/start", strings.NewReader(`{"session_id":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Sign(req, nodeURL, testSecret); err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(got) != "/transcode/start" {
		t.Fatalf("status %d body %q, want 200 /transcode/start", resp.StatusCode, got)
	}
}
