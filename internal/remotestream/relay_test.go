package remotestream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRelayRewritesNestedHLSReferencesAndPreservesRange(t *testing.T) {
	var mu sync.Mutex
	var upstreamRequests []*http.Request
	relay := NewRelay()
	relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		upstreamRequests = append(upstreamRequests, request.Clone(context.Background()))
		mu.Unlock()
		switch request.URL.String() {
		case "https://1.1.1.1/master.m3u8?token=top-secret":
			return relayResponse(request, http.StatusOK, "application/vnd.apple.mpegurl", strings.Join([]string{
				"#EXTM3U",
				`#EXT-X-KEY:METHOD=AES-128,URI="https://8.8.8.8/key.bin?key=secret"`,
				"variant/playlist.m3u8?auth=secret",
				"",
			}, "\n")), nil
		case "https://1.1.1.1/variant/playlist.m3u8?auth=secret":
			return relayResponse(request, http.StatusOK, "application/x-mpegURL", strings.Join([]string{
				"#EXTM3U",
				"#EXTINF:6,",
				"https://9.9.9.9/segment.ts?signature=secret",
				"",
			}, "\n")), nil
		case "https://9.9.9.9/segment.ts?signature=secret":
			if request.Header.Get("Range") != "bytes=2-5" {
				t.Errorf("upstream Range = %q", request.Header.Get("Range"))
			}
			response := relayResponse(request, http.StatusPartialContent, "video/mp2t", "2345")
			response.Header.Set("Accept-Ranges", "bytes")
			response.Header.Set("Content-Range", "bytes 2-5/10")
			response.Header.Set("Content-Length", "4")
			return response, nil
		default:
			t.Errorf("unexpected upstream URL %q", request.URL.String())
			return relayResponse(request, http.StatusNotFound, "text/plain", ""), nil
		}
	})}

	masterURL, cleanup := registerRelayForTest(t, relay, "root", "https://1.1.1.1/master.m3u8?token=top-secret")
	defer cleanup()

	master := fetchRelay(t, relay, masterURL, http.MethodGet, "")
	if master.status != http.StatusOK {
		t.Fatalf("master status = %d, body=%q", master.status, master.body)
	}
	for _, secret := range []string{"1.1.1.1", "8.8.8.8", "top-secret", "auth=secret", "key=secret"} {
		if strings.Contains(master.body, secret) {
			t.Fatalf("rewritten master leaked %q: %s", secret, master.body)
		}
	}
	keyPath := quotedURI(t, master.body)
	variantPath := firstMediaURI(t, master.body)
	if !strings.HasPrefix(keyPath, "/source/") || !strings.HasPrefix(variantPath, "/source/") {
		t.Fatalf("master references were not relayed: key=%q variant=%q", keyPath, variantPath)
	}

	variant := fetchRelay(t, relay, variantPath, http.MethodGet, "")
	if strings.Contains(variant.body, "9.9.9.9") || strings.Contains(variant.body, "signature=secret") {
		t.Fatalf("rewritten variant leaked provider URL: %s", variant.body)
	}
	segmentPath := firstMediaURI(t, variant.body)
	segment := fetchRelay(t, relay, segmentPath, http.MethodGet, "bytes=2-5")
	if segment.status != http.StatusPartialContent || segment.body != "2345" {
		t.Fatalf("segment response = status %d body %q", segment.status, segment.body)
	}
	if segment.header.Get("Content-Range") != "bytes 2-5/10" ||
		segment.header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("range headers = %+v", segment.header)
	}

	cleanup()
	if got := fetchRelay(t, relay, masterURL, http.MethodGet, ""); got.status != http.StatusNotFound {
		t.Fatalf("released master status = %d", got.status)
	}
	if got := fetchRelay(t, relay, segmentPath, http.MethodGet, ""); got.status != http.StatusNotFound {
		t.Fatalf("released child status = %d", got.status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(upstreamRequests) != 3 {
		t.Fatalf("upstream request count = %d, want 3", len(upstreamRequests))
	}
}

func TestRelayHEADAndMethodRestriction(t *testing.T) {
	relay := NewRelay()
	relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodHead {
			t.Errorf("upstream method = %s", request.Method)
		}
		response := relayResponse(request, http.StatusOK, "video/mp4", "must-not-be-forwarded")
		response.Header.Set("Content-Length", "99")
		return response, nil
	})}
	relayURL, cleanup := registerRelayForTest(t, relay, "head", "https://1.1.1.1/movie.mp4")
	defer cleanup()

	head := fetchRelay(t, relay, relayURL, http.MethodHead, "")
	if head.status != http.StatusOK || head.body != "" || head.header.Get("Content-Length") != "99" {
		t.Fatalf("HEAD response = status %d len=%q body=%q", head.status, head.header.Get("Content-Length"), head.body)
	}
	post := fetchRelay(t, relay, relayURL, http.MethodPost, "")
	if post.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", post.status)
	}
}

func TestRelayRejectsUnsafeHLSReference(t *testing.T) {
	relay := NewRelay()
	relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return relayResponse(request, http.StatusOK, "application/vnd.apple.mpegurl",
			"#EXTM3U\nhttp://127.0.0.1/private\n"), nil
	})}
	relayURL, cleanup := registerRelayForTest(t, relay, "unsafe", "https://1.1.1.1/master.m3u8")
	defer cleanup()

	response := fetchRelay(t, relay, relayURL, http.MethodGet, "")
	if response.status != http.StatusBadGateway {
		t.Fatalf("unsafe playlist status = %d, body=%q", response.status, response.body)
	}
	if strings.Contains(response.body, "127.0.0.1") {
		t.Fatalf("unsafe target leaked in response: %q", response.body)
	}
}

func TestRelayProxyErrorDoesNotLeakProviderURL(t *testing.T) {
	relay := NewRelay()
	relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New(`Get "https://1.1.1.1/movie?token=secret": provider failed`)
	})}
	request := httptest.NewRequest(http.MethodGet, "http://silo/stream", nil)
	recorder := httptest.NewRecorder()
	err := relay.Proxy(recorder, request, "https://1.1.1.1/movie?token=secret")
	if err == nil || !RetryableBeforeResponse(err) {
		t.Fatalf("Proxy error = %v, want retryable pre-response failure", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "1.1.1.1") {
		t.Fatalf("Proxy error leaked provider URL: %v", err)
	}
}

func TestRelayProxyRejectsUnregisteredHLSBeforeResponse(t *testing.T) {
	relay := NewRelay()
	relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return relayResponse(request, http.StatusOK, "application/vnd.apple.mpegurl",
			"#EXTM3U\nhttps://9.9.9.9/segment.ts?token=secret\n"), nil
	})}
	request := httptest.NewRequest(http.MethodGet, "http://silo/stream", nil)
	recorder := httptest.NewRecorder()
	err := relay.Proxy(recorder, request, "https://1.1.1.1/master.m3u8?token=secret")
	if err == nil || !RetryableBeforeResponse(err) {
		t.Fatalf("Proxy error = %v, want retryable pre-response failure", err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("Proxy committed response status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "9.9.9.9") {
		t.Fatalf("Proxy error leaked provider URL: %v", err)
	}
}

func TestRelayStagesEmptyProviderBodyBeforeResponse(t *testing.T) {
	relay := NewRelay()
	relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return relayResponse(request, http.StatusOK, "video/mp4", ""), nil
	})}
	recorder := httptest.NewRecorder()
	err := relay.Proxy(recorder, httptest.NewRequest(http.MethodGet, "http://silo/stream", nil), "https://1.1.1.1/movie.mp4")
	if err == nil || !RetryableBeforeResponse(err) {
		t.Fatalf("Proxy error = %v, want retryable pre-response failure", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("provider failure committed body %q", recorder.Body.String())
	}
}

func TestRelayRejectsDASHBeforeResponse(t *testing.T) {
	for name, testCase := range map[string]struct {
		contentType string
		body        string
	}{
		"content-type": {"application/dash+xml", `<MPD></MPD>`},
		"body-sniff":   {"application/octet-stream", `<?xml version="1.0"?><MPD></MPD>`},
	} {
		t.Run(name, func(t *testing.T) {
			relay := NewRelay()
			relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return relayResponse(request, http.StatusOK, testCase.contentType, testCase.body), nil
			})}
			recorder := httptest.NewRecorder()
			err := relay.Proxy(recorder, httptest.NewRequest(http.MethodGet, "http://silo/stream", nil), "https://1.1.1.1/video")
			if err == nil || !RetryableBeforeResponse(err) {
				t.Fatalf("Proxy error = %v, want retryable DASH rejection", err)
			}
			if recorder.Body.Len() != 0 {
				t.Fatalf("DASH rejection committed body %q", recorder.Body.String())
			}
		})
	}
}

func TestRelaySniffsMislabeledHLS(t *testing.T) {
	relay := NewRelay()
	relay.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return relayResponse(request, http.StatusOK, "application/octet-stream", "#EXTM3U\nhttps://9.9.9.9/segment.ts\n"), nil
	})}
	relayURL, cleanup := registerRelayForTest(t, relay, "sniff", "https://1.1.1.1/video")
	defer cleanup()

	response := fetchRelay(t, relay, relayURL, http.MethodGet, "")
	if response.status != http.StatusOK || !strings.Contains(response.body, "/source/") || strings.Contains(response.body, "9.9.9.9") {
		t.Fatalf("mislabeled HLS response = status %d body %q", response.status, response.body)
	}
}

func TestRelayHLSReferenceTokenIsSecretBoundAndExpiring(t *testing.T) {
	relay := NewRelay()
	now := time.Now()
	const source = "https://1.1.1.1/segment.ts?token=provider-secret"
	opaque, err := relay.sealReference("parent-a", source, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(opaque, "provider-secret") || strings.Contains(opaque, "1.1.1.1") {
		t.Fatalf("sealed reference leaked source URL: %q", opaque)
	}
	opened, err := relay.openReference("parent-a", opaque, now)
	if err != nil || opened != source {
		t.Fatalf("openReference = %q, %v", opened, err)
	}
	if _, err := relay.openReference("parent-b", opaque, now); err == nil {
		t.Fatal("reference token opened under a different parent")
	}
	tamperedBytes := []byte(opaque)
	tamperAt := len(tamperedBytes) / 2
	if tamperedBytes[tamperAt] == 'A' {
		tamperedBytes[tamperAt] = 'B'
	} else {
		tamperedBytes[tamperAt] = 'A'
	}
	tampered := string(tamperedBytes)
	if _, err := relay.openReference("parent-a", tampered, now); err == nil {
		t.Fatal("tampered reference token opened")
	}
	if _, err := relay.openReference("parent-a", opaque, now.Add(relayEntryLifetime)); err == nil {
		t.Fatal("expired reference token opened")
	}
}

func TestRelayCloseRevokesEntriesAndRejectsRegistrations(t *testing.T) {
	relay := NewRelay()
	relayURL, cleanup := registerRelayForTest(t, relay, "close", "https://1.1.1.1/movie.mp4")
	defer cleanup()

	if err := relay.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := fetchRelay(t, relay, relayURL, http.MethodGet, ""); got.status != http.StatusNotFound {
		t.Fatalf("closed relay response status = %d, want 404", got.status)
	}
	if _, _, err := relay.Register(context.Background(), "https://1.1.1.1/other.mp4"); err == nil {
		t.Fatal("Register succeeded after Close")
	}
	if err := relay.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func relayResponse(request *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

type relayFetch struct {
	status int
	header http.Header
	body   string
}

func fetchRelay(t *testing.T, relay *Relay, rawURL, method, byteRange string) relayFetch {
	t.Helper()
	request := httptest.NewRequest(method, "http://relay"+rawURL, nil)
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	recorder := httptest.NewRecorder()
	relay.handle(recorder, request)
	response := recorder.Result()
	return relayFetch{status: response.StatusCode, header: response.Header.Clone(), body: recorder.Body.String()}
}

func registerRelayForTest(t *testing.T, relay *Relay, token, rawURL string) (string, func()) {
	t.Helper()
	source, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	baseName := safeRelayBaseName(source)
	relay.mu.Lock()
	relay.entries[token] = &relayEntry{
		source: source, baseName: baseName, createdAt: time.Now(),
	}
	relay.mu.Unlock()
	cleanup := func() {
		relay.mu.Lock()
		relay.deleteEntryLocked(token)
		relay.mu.Unlock()
	}
	return "/source/" + token + "/" + url.PathEscape(baseName), cleanup
}

func TestSafeRelayBaseNameDoesNotExposeProviderPath(t *testing.T) {
	source, _ := url.Parse("https://1.1.1.1/provider-secret-token/movie-title.m3u8?token=query-secret")
	if got := safeRelayBaseName(source); got != "stream.m3u8" {
		t.Fatalf("safeRelayBaseName = %q", got)
	}
	source, _ = url.Parse("https://1.1.1.1/provider-secret-token")
	if got := safeRelayBaseName(source); got != "stream" {
		t.Fatalf("safeRelayBaseName without extension = %q", got)
	}
}

func quotedURI(t *testing.T, playlist string) string {
	t.Helper()
	const marker = `URI="`
	start := strings.Index(playlist, marker)
	if start < 0 {
		t.Fatalf("playlist has no URI attribute: %s", playlist)
	}
	start += len(marker)
	end := strings.IndexByte(playlist[start:], '"')
	if end < 0 {
		t.Fatalf("playlist has malformed URI attribute: %s", playlist)
	}
	return playlist[start : start+end]
}

func firstMediaURI(t *testing.T, playlist string) string {
	t.Helper()
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	t.Fatalf("playlist has no media URI: %s", playlist)
	return ""
}
