package s3client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type recordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Body     string
}

type mutationFenceHTTPClient func(*http.Request) (*http.Response, error)

func (f mutationFenceHTTPClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

func newMutationFenceTestClient(httpClient mutationFenceHTTPClient) *Client {
	client := NewClient(BucketConfig{Endpoint: "https://storage.invalid", Region: "us-east-1", Bucket: "artwork", PathStyle: true, AccessKey: "test", SecretKey: "test"})
	client.s3Client = s3.New(client.s3Client.Options(), func(o *s3.Options) { o.HTTPClient = httpClient })
	return client
}

func TestBlockedMutationHonorsContextCancellation(t *testing.T) {
	client := newMutationFenceTestClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})
	synctest.Test(t, func(t *testing.T) {
		release, err := client.BeginMutationFence(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- client.PutObject(ctx, client.Bucket(), "blocked.webp", []byte("image")) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("PutObject() passed an active fence: %v", err)
		default:
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("PutObject() error = %v, want context.Canceled", err)
		}
	})
}

func TestMutationFenceAcquisitionHonorsContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		releaseRequest := make(chan struct{})
		releaseActiveWrite := sync.OnceFunc(func() { close(releaseRequest) })
		defer releaseActiveWrite()
		client := newMutationFenceTestClient(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPut {
				close(entered)
				<-releaseRequest
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		})
		writeDone := make(chan error, 1)
		go func() {
			writeDone <- client.PutObject(context.Background(), client.Bucket(), "active.webp", []byte("image"))
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("active mutation did not start")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type fenceResult struct {
			release func()
			err     error
		}
		fenceDone := make(chan fenceResult, 1)
		go func() {
			release, err := client.BeginMutationFence(ctx)
			fenceDone <- fenceResult{release: release, err: err}
		}()
		synctest.Wait()
		select {
		case result := <-fenceDone:
			if result.release != nil {
				result.release()
			}
			t.Fatalf("BeginMutationFence() passed an active write: %v", result.err)
		default:
		}
		cancel()
		select {
		case result := <-fenceDone:
			if result.release != nil {
				result.release()
				t.Fatal("BeginMutationFence() returned a release function after cancellation")
			}
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("BeginMutationFence() error = %v, want context.Canceled", result.err)
			}
		case <-time.After(time.Second):
			t.Fatal("BeginMutationFence() did not honor context cancellation")
		}
		releaseActiveWrite()
		if err := <-writeDone; err != nil {
			t.Fatal(err)
		}
	})
}

type s3TestServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

func newS3TestServer(t *testing.T) *s3TestServer {
	t.Helper()

	s := &s3TestServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Body:     string(body),
		})
		s.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			prefix := r.URL.Query().Get("prefix")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Contents><Key>%s/export.json.gz</Key><Size>123</Size></Contents>
</ListBucketResult>`, prefix)
		case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok")
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(s.server.Close)

	return s
}

func (s *s3TestServer) URL() string {
	return s.server.URL
}

func (s *s3TestServer) Requests() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]recordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func TestDeleteObjectsFallbackCountsMissingKeysOnRetry(t *testing.T) {
	var mu sync.Mutex
	objects := map[string]bool{"existing.webp": true}
	var batchCalls, objectCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
			mu.Lock()
			batchCalls++
			mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = io.WriteString(w, "<Error><Code>NotImplemented</Code></Error>")
		case r.Method == http.MethodDelete:
			key := strings.TrimPrefix(r.URL.Path, "/silo/")
			mu.Lock()
			objectCalls++
			exists := objects[key]
			delete(objects, key)
			missingCode := "NoSuchKey"
			if batchCalls == 2 {
				missingCode = "NotFound"
			}
			mu.Unlock()
			if exists {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", missingCode)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", PathStyle: true, AccessKey: "test", SecretKey: "test"})
	keys := []string{"missing.webp", "existing.webp"}
	for attempt := range 2 {
		deleted, err := client.DeleteObjects(t.Context(), client.Bucket(), keys)
		if err != nil || deleted != len(keys) {
			t.Fatalf("attempt %d: DeleteObjects() = %d, %v; want %d, nil", attempt+1, deleted, err, len(keys))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if batchCalls != 2 || objectCalls != 4 {
		t.Fatalf("batch requests=%d, per-key requests=%d; want 2 and 4", batchCalls, objectCalls)
	}
}

func TestDeleteObjectsCountsOnlyMissingObjectBatchErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !r.URL.Query().Has("delete") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<DeleteResult>
			<Error><Key>missing.webp</Key><Code>NoSuchKey</Code></Error>
			<Error><Key>denied.webp</Key><Code>AccessDenied</Code><Message>signed https://x/?X-Amz-Signature=SECRETSIG</Message></Error>
		</DeleteResult>`)
	}))
	t.Cleanup(server.Close)
	client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", PathStyle: true, AccessKey: "test", SecretKey: "test"})
	deleted, err := client.DeleteObjects(t.Context(), client.Bucket(), []string{"missing.webp", "existing.webp", "denied.webp"})
	if err == nil || deleted != 2 {
		t.Fatalf("DeleteObjects() = %d, %v; want 2 and an error", deleted, err)
	}
	if !strings.Contains(err.Error(), "denied.webp") {
		t.Fatal("DeleteObjects() error did not name denied.webp")
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatal("DeleteObjects() error did not carry the AccessDenied error code")
	}
	if strings.Contains(err.Error(), "SECRETSIG") {
		t.Fatal("DeleteObjects() error echoed the backend message")
	}
}

func TestDeletePrefixReturnsErrorWhenBatchDeleteFails(t *testing.T) {
	// Neither failure is about how the batch was sent, so retrying each key on
	// its own would fail the same way: no per-key fallback.
	for name, rejection := range map[string]struct {
		status int
		code   string
	}{
		"access denied": {http.StatusForbidden, "AccessDenied"},
		"wrong region":  {http.StatusBadRequest, "AuthorizationHeaderMalformed"},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var batchCalls, objectCalls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
					_, _ = io.WriteString(w, `<ListBucketResult><Name>silo</Name><IsTruncated>false</IsTruncated>
						<Contents><Key>local/ebooks/1/a.webp</Key></Contents>
						<Contents><Key>local/ebooks/1/b.webp</Key></Contents>
					</ListBucketResult>`)
				case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
					mu.Lock()
					batchCalls++
					mu.Unlock()
					w.WriteHeader(rejection.status)
					_, _ = io.WriteString(w, "<Error><Code>"+rejection.code+"</Code><Message>signed ?X-Amz-Signature=SECRETSIG</Message></Error>")
				case r.Method == http.MethodDelete:
					mu.Lock()
					objectCalls++
					mu.Unlock()
					w.WriteHeader(rejection.status)
					_, _ = io.WriteString(w, "<Error><Code>"+rejection.code+"</Code></Error>")
				default:
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			t.Cleanup(server.Close)
			client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", PathStyle: true, AccessKey: "test", SecretKey: "test"})
			deleted, err := client.DeletePrefix(t.Context(), client.Bucket(), "local/ebooks/1/")
			if err == nil || deleted != 0 {
				t.Fatalf("DeletePrefix() = %d, %v; want 0 and an error", deleted, err)
			}
			if !strings.Contains(err.Error(), rejection.code) || strings.Contains(err.Error(), "SECRETSIG") {
				t.Fatal("DeletePrefix() error must carry the code without the backend message")
			}
			mu.Lock()
			defer mu.Unlock()
			if batchCalls != 1 || objectCalls != 0 {
				t.Fatalf("batch requests=%d, per-key requests=%d; want 1 and 0", batchCalls, objectCalls)
			}
		})
	}
}

// The key in a partial-failure entry comes from the backend, so it is never
// echoed unless it is one the caller asked to delete.
func TestDeleteObjectsDoesNotEchoAnUnrequestedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<DeleteResult>
			<Error><Key>https://x/?X-Amz-Signature=SECRETSIG</Key><Code>AccessDenied</Code></Error>
		</DeleteResult>`)
	}))
	t.Cleanup(server.Close)
	client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", PathStyle: true, AccessKey: "test", SecretKey: "test"})
	deleted, err := client.DeleteObjects(t.Context(), client.Bucket(), []string{"a.webp"})
	if err == nil || deleted != 0 {
		t.Fatal("DeleteObjects() must report the failed entry")
	}
	if strings.Contains(err.Error(), "SECRETSIG") {
		t.Fatal("DeleteObjects() error echoed a key the caller never requested")
	}
}

func TestDeleteObjectsFallbackReturnsErrorForFailedKeys(t *testing.T) {
	for name, rejection := range map[string]struct {
		status int
		body   string
	}{
		"not implemented": {http.StatusNotImplemented, "<Error><Code>NotImplemented</Code></Error>"},
		// The SDK sends a CRC32 checksum on batch deletes; backends that still
		// require Content-MD5 reject the batch but accept single deletes.
		"missing content-md5": {http.StatusBadRequest, "<Error><Code>MissingContentMD5</Code></Error>"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
					w.WriteHeader(rejection.status)
					_, _ = io.WriteString(w, rejection.body)
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/denied.webp"):
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code></Error>")
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			t.Cleanup(server.Close)
			client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", PathStyle: true, AccessKey: "test", SecretKey: "test"})
			deleted, err := client.DeleteObjects(t.Context(), client.Bucket(), []string{"ok.webp", "denied.webp"})
			if err == nil || deleted != 1 {
				t.Fatalf("DeleteObjects() = %d, %v; want 1 and an error", deleted, err)
			}
		})
	}
}

func TestDeleteObjectDoesNotIgnoreMissingBucket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<Error><Code>NoSuchBucket</Code></Error>")
	}))
	t.Cleanup(server.Close)
	client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", PathStyle: true, AccessKey: "test", SecretKey: "test"})
	if err := client.DeleteObject(t.Context(), client.Bucket(), "missing.webp"); err == nil {
		t.Fatal("DeleteObject() ignored a missing bucket")
	}
}

func TestClientWithKeyPrefixPrefixesObjectOperationsAndStripsListedKeys(t *testing.T) {
	t.Parallel()

	srv := newS3TestServer(t)
	client := NewClient(BucketConfig{
		Endpoint:  srv.URL(),
		Region:    "us-east-1",
		Bucket:    "silo",
		KeyPrefix: " /silo/dev/ ",
		AccessKey: "test",
		SecretKey: "test",
		PathStyle: true,
	})

	ctx := context.Background()
	if err := client.PutObject(ctx, client.Bucket(), "poster.jpg", []byte("data")); err != nil {
		t.Fatalf("PutObject() returned error: %v", err)
	}
	if _, err := client.GetObject(ctx, client.Bucket(), "poster.jpg"); err != nil {
		t.Fatalf("GetObject() returned error: %v", err)
	}
	if ok, err := client.ObjectExists(ctx, client.Bucket(), "poster.jpg"); err != nil || !ok {
		t.Fatalf("ObjectExists() = %v, %v, want true, nil", ok, err)
	}
	infos, err := client.ListObjectInfos(ctx, client.Bucket(), "catalog-seeds")
	if err != nil {
		t.Fatalf("ListObjectInfos() returned error: %v", err)
	}
	if len(infos) != 1 || infos[0].Key != "catalog-seeds/export.json.gz" {
		t.Fatalf("ListObjectInfos() = %#v, want logical unprefixed key", infos)
	}
	if _, err := client.DeletePrefix(ctx, client.Bucket(), "catalog-seeds"); err != nil {
		t.Fatalf("DeletePrefix() returned error: %v", err)
	}
	if err := client.HeadBucket(ctx, client.Bucket()); err != nil {
		t.Fatalf("HeadBucket() returned error: %v", err)
	}
	if err := client.SetBucketCORS(ctx, client.Bucket(), []string{"*"}); err != nil {
		t.Fatalf("SetBucketCORS() returned error: %v", err)
	}

	requests := srv.Requests()
	if !containsRequest(requests, http.MethodPut, "/silo/silo/dev/poster.jpg", "") {
		t.Fatalf("requests = %#v, want prefixed PutObject path", requests)
	}
	if !containsRequest(requests, http.MethodGet, "/silo/silo/dev/poster.jpg", "") {
		t.Fatalf("requests = %#v, want prefixed GetObject path", requests)
	}
	if !containsRequest(requests, http.MethodHead, "/silo/silo/dev/poster.jpg", "") {
		t.Fatalf("requests = %#v, want prefixed HeadObject path", requests)
	}
	if !containsRequest(requests, http.MethodGet, "/silo", "list-type=2") {
		t.Fatalf("requests = %#v, want ListObjectsV2 bucket path", requests)
	}
	listReq := findRequest(requests, http.MethodGet, "/silo", "list-type=2")
	if got := parseQuery(listReq.RawQuery).Get("prefix"); got != "silo/dev/catalog-seeds" {
		t.Fatalf("list prefix = %q, want silo/dev/catalog-seeds", got)
	}
	deleteReq := findRequest(requests, http.MethodPost, "/silo", "delete=")
	if !strings.Contains(deleteReq.Body, "<Key>silo/dev/catalog-seeds/export.json.gz</Key>") {
		t.Fatalf("delete body = %q, want prefixed delete key", deleteReq.Body)
	}
	if !containsRequest(requests, http.MethodHead, "/silo", "") {
		t.Fatalf("requests = %#v, want raw HeadBucket path", requests)
	}
	if !containsRequest(requests, http.MethodPut, "/silo", "cors=") {
		t.Fatalf("requests = %#v, want raw PutBucketCors path", requests)
	}
}

func TestClientWithKeyPrefixPrefixesGeneratedURLs(t *testing.T) {
	t.Parallel()

	client := NewClient(BucketConfig{
		Endpoint:  "https://s3.example.test",
		Region:    "us-east-1",
		Bucket:    "silo",
		KeyPrefix: "silo/dev",
		AccessKey: "test",
		SecretKey: "test",
		PathStyle: true,
	})

	publicURL, err := client.PublicURL(client.Bucket(), "tmdb/movies/550/poster/original.jpg")
	if err != nil {
		t.Fatalf("PublicURL() returned error: %v", err)
	}
	if publicURL != "https://s3.example.test/silo/silo/dev/tmdb/movies/550/poster/original.jpg" {
		t.Fatalf("PublicURL() = %q", publicURL)
	}

	presignedURL, err := client.PresignGetURL(
		context.Background(),
		client.Bucket(),
		"tmdb/movies/550/poster/original.jpg",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("PresignGetURL() returned error: %v", err)
	}
	if !strings.Contains(presignedURL, "/silo/silo/dev/tmdb/movies/550/poster/original.jpg?") {
		t.Fatalf("PresignGetURL() = %q, want prefixed object path", presignedURL)
	}
}

func TestClientObjectAvailableUsesExternalDeliveryPath(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		requests []recordedRequest
	)
	delivery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, recordedRequest{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Body:     r.Header.Get("Range"),
		})
		mu.Unlock()

		if r.URL.Path == "/silo/dev/poster/w780.webp" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	}))
	t.Cleanup(delivery.Close)

	client := NewClient(BucketConfig{
		Endpoint:       "https://s3.example.invalid",
		PublicEndpoint: delivery.URL,
		Region:         "us-east-1",
		Bucket:         "silo",
		KeyPrefix:      "silo/dev",
		AccessKey:      "test",
		SecretKey:      "test",
		PathStyle:      true,
		URLAuth:        URLAuthCloudflareToken,
		TokenSecret:    "secret",
		Role:           "metadata",
	})
	dialsBefore := testutil.ToFloat64(s3Dials.WithLabelValues("metadata"))

	available, err := client.ObjectAvailable(t.Context(), client.Bucket(), "poster/w780.webp")
	if err != nil || available {
		t.Fatalf("large ObjectAvailable() = %v, %v, want false, nil", available, err)
	}
	available, err = client.ObjectAvailable(t.Context(), client.Bucket(), "poster/w500.webp")
	if err != nil || !available {
		t.Fatalf("medium ObjectAvailable() = %v, %v, want true, nil", available, err)
	}

	mu.Lock()
	gotRequests := append([]recordedRequest(nil), requests...)
	mu.Unlock()
	if len(gotRequests) != 2 {
		t.Fatalf("delivery requests = %#v, want two", gotRequests)
	}
	if testutil.ToFloat64(s3Dials.WithLabelValues("metadata")) < dialsBefore+1 {
		t.Fatal("external delivery probe dial was not counted")
	}
	for _, req := range gotRequests {
		if req.Method != http.MethodGet || req.Body != "bytes=0-0" {
			t.Errorf("delivery request = %#v, want one-byte GET", req)
		}
		if parseQuery(req.RawQuery).Get("verify") == "" {
			t.Errorf("delivery request = %#v, want Cloudflare auth token", req)
		}
	}
}

func TestClientObjectAvailableReportsExternalAuthFailureWithoutToken(t *testing.T) {
	t.Parallel()

	delivery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(delivery.Close)

	client := NewClient(BucketConfig{
		Endpoint:       "https://s3.example.invalid",
		PublicEndpoint: delivery.URL,
		Region:         "us-east-1",
		Bucket:         "silo",
		AccessKey:      "test",
		SecretKey:      "test",
		URLAuth:        URLAuthCloudflareToken,
		TokenSecret:    "do-not-log-this-secret",
	})

	available, err := client.ObjectAvailable(t.Context(), client.Bucket(), "poster.webp")
	if err == nil || available {
		t.Fatalf("ObjectAvailable() = %v, %v, want false and an auth-path error", available, err)
	}
	if strings.Contains(err.Error(), "verify=") || strings.Contains(err.Error(), "do-not-log-this-secret") {
		t.Fatalf("ObjectAvailable() error leaked delivery credentials: %v", err)
	}
}

func TestClientEffectivePresignTTLClampsCloudflareTokenTTL(t *testing.T) {
	t.Parallel()

	client := NewClient(BucketConfig{
		Endpoint:       "https://s3.example.test",
		PublicEndpoint: "https://cdn.example.test",
		Region:         "us-east-1",
		Bucket:         "silo",
		AccessKey:      "test",
		SecretKey:      "test",
		URLAuth:        URLAuthCloudflareToken,
		TokenSecret:    "secret",
		TokenTTL:       600,
	})

	if got := client.EffectivePresignTTL(4 * time.Hour); got != 10*time.Minute {
		t.Fatalf("EffectivePresignTTL(4h) = %s, want 10m", got)
	}
	if got := client.EffectivePresignTTL(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("EffectivePresignTTL(5m) = %s, want 5m", got)
	}
}

func containsRequest(requests []recordedRequest, method, path, rawQueryContains string) bool {
	for _, req := range requests {
		if req.Method == method && req.Path == path && strings.Contains(req.RawQuery, rawQueryContains) {
			return true
		}
	}
	return false
}

func findRequest(requests []recordedRequest, method, path, rawQueryContains string) recordedRequest {
	for _, req := range requests {
		if req.Method == method && req.Path == path && strings.Contains(req.RawQuery, rawQueryContains) {
			return req
		}
	}
	return recordedRequest{}
}

func parseQuery(raw string) url.Values {
	values, err := url.ParseQuery(raw)
	if err != nil {
		panic(err)
	}
	return values
}

func TestArtworkDeliveryScopeExcludesCredentials(t *testing.T) {
	client := &Client{endpoint: "https://storage.example", bucket: "artwork", publicEndpoint: "https://images.example", tokenSecret: "first-secret"}
	scope := client.ArtworkDeliveryScope()
	client.tokenSecret = "rotated-secret"
	if got := client.ArtworkDeliveryScope(); got != scope {
		t.Fatal("credential entered persisted scope digest")
	}
	client.publicEndpoint = "https://other-images.example"
	if got := client.ArtworkDeliveryScope(); got == scope {
		t.Fatal("delivery endpoint change did not change scope")
	}
}

func TestSafeErrorCodeAllowsOnlyIdentifierCodes(t *testing.T) {
	for code, want := range map[string]string{
		"AccessDenied":                      "AccessDenied",
		"Slow_Down-1.x":                     "Slow_Down-1.x",
		"":                                  unrecognizedErrorCode,
		"https://x/?X-Amz-Signature=SECRET": unrecognizedErrorCode,
		"Access Denied token":               unrecognizedErrorCode,
		strings.Repeat("A", 65):             unrecognizedErrorCode,
	} {
		if got := safeErrorCode(code); got != want {
			t.Errorf("safeErrorCode(%q) = %q, want %q", code, got, want)
		}
	}
}

// A HEAD response has no body, so a 403 cannot say whether the key is missing
// (AWS S3 answers 403 for a missing key when the credential lacks
// s3:ListBucket) or access is denied. ObjectMatches reports it as unverified so
// the caller rewrites the object and the PUT reports any real denial (#911).
func TestObjectMatchesStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
	}{
		{status: http.StatusNotFound},
		{status: http.StatusForbidden},
		{status: http.StatusBadRequest, wantErr: true},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(server.Close)
			client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "artwork", PathStyle: true, AccessKey: "test", SecretKey: "test"})

			matched, err := client.ObjectMatches(t.Context(), client.Bucket(), "poster/original.webp", []byte("image"))
			if matched {
				t.Fatal("ObjectMatches() = true for a failed HEAD")
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("ObjectMatches() error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

// r2RateLimitCode is the error code Cloudflare R2 sends with HTTP 429.
const r2RateLimitCode = "TooManyRequests"

// throttlingBackend answers the first throttled requests of each method with
// a provider rate-limit response, Cloudflare R2's 429 unless set otherwise,
// and every later request with success.
type throttlingBackend struct {
	mu        sync.Mutex
	status    int
	code      string
	throttled map[string]int
	calls     map[string]int
}

func newThrottlingBackend(throttled map[string]int) *throttlingBackend {
	return &throttlingBackend{status: http.StatusTooManyRequests, code: r2RateLimitCode, throttled: throttled, calls: map[string]int{}}
}

func (b *throttlingBackend) Do(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	b.mu.Lock()
	b.calls[r.Method]++
	throttle := b.throttled[r.Method] > 0
	if throttle {
		b.throttled[r.Method]--
	}
	b.mu.Unlock()
	if !throttle {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Length": {"2"}}, Body: io.NopCloser(strings.NewReader("ok")), ContentLength: 2, Request: r}, nil
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + b.code + `</Code><Message>Please reduce your request rate</Message></Error>`
	if r.Method == http.MethodHead {
		// HEAD responses carry no body, so only the status identifies the throttle.
		body = ""
	}
	return &http.Response{StatusCode: b.status, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
}

func (b *throttlingBackend) Calls(method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[method]
}

func TestClientRetriesProviderRateLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{"cloudflare r2", http.StatusTooManyRequests, r2RateLimitCode},
		{"aws s3", http.StatusServiceUnavailable, "SlowDown"},
		{"minio write quorum", http.StatusServiceUnavailable, "SlowDownWrite"},
		{"minio read quorum", http.StatusServiceUnavailable, "SlowDownRead"},
		{"minio busy", http.StatusServiceUnavailable, "ServerBusy"},
		{"wasabi", http.StatusForbidden, "RequestRateLimitExceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := newThrottlingBackend(map[string]int{http.MethodPut: 2})
				backend.status, backend.code = tc.status, tc.code
				client := newMutationFenceTestClient(backend.Do)
				if err := client.PutObject(t.Context(), client.Bucket(), "tmdb/poster.webp", []byte("poster")); err != nil {
					t.Fatalf("PutObject() after a short rate limit = %v, want success", err)
				}
				if got := backend.Calls(http.MethodPut); got != 3 {
					t.Fatalf("PUT attempts = %d, want 2 throttled and 1 successful", got)
				}
				backend.mu.Lock()
				backend.throttled[http.MethodPut] = retryMaxAttempts
				backend.mu.Unlock()
				if err := client.PutObject(t.Context(), client.Bucket(), "tmdb/poster.webp", []byte("poster")); !errors.Is(err, ErrThrottled) {
					t.Fatalf("PutObject() during a lasting rate limit = %v, want ErrThrottled", err)
				}
			})
		})
	}
}

func TestClientKeepsBackingOffAfterALongThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The SDK's default quota of 500 tokens covers 50 calls that each spend
		// two 5-token retries, so 60 such calls would empty it.
		const exhausted = 60
		backend := newThrottlingBackend(map[string]int{http.MethodPut: exhausted*retryMaxAttempts + 1})
		client := newMutationFenceTestClient(backend.Do)
		for range exhausted {
			if err := client.PutObject(t.Context(), client.Bucket(), "tmdb/poster.webp", []byte("poster")); err == nil {
				t.Fatal("PutObject() succeeded while every attempt was throttled")
			}
		}
		before := backend.Calls(http.MethodPut)
		if err := client.PutObject(t.Context(), client.Bucket(), "tmdb/poster.webp", []byte("poster")); err != nil {
			t.Fatalf("PutObject() after a long throttle = %v, want a retry once it clears", err)
		}
		if got := backend.Calls(http.MethodPut) - before; got != 2 {
			t.Fatalf("PUT attempts for the last call = %d, want 1 throttled and 1 successful", got)
		}
	})
}

func TestClientRetriesBodilessRateLimitOnHead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := newThrottlingBackend(map[string]int{http.MethodHead: 2})
		client := newMutationFenceTestClient(backend.Do)
		if _, err := client.HeadObject(t.Context(), client.Bucket(), "tmdb/poster.webp"); err != nil {
			t.Fatalf("HeadObject() after a bodiless 429 = %v, want success", err)
		}
		if got := backend.Calls(http.MethodHead); got != 3 {
			t.Fatalf("HEAD attempts = %d, want 2 throttled and 1 successful", got)
		}
	})
}
