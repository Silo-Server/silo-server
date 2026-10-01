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
			<Error><Key>denied.webp</Key><Code>AccessDenied</Code></Error>
		</DeleteResult>`)
	}))
	t.Cleanup(server.Close)
	client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", PathStyle: true, AccessKey: "test", SecretKey: "test"})
	deleted, err := client.DeleteObjects(t.Context(), client.Bucket(), []string{"missing.webp", "existing.webp", "denied.webp"})
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteObjects() = %d, %v; want 2, nil", deleted, err)
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

func TestClientWithoutKeyPrefixUsesLogicalKeys(t *testing.T) {
	t.Parallel()

	srv := newS3TestServer(t)
	client := NewClient(BucketConfig{
		Endpoint:       srv.URL(),
		Region:         "us-east-1",
		Bucket:         "silo",
		AccessKey:      "test",
		SecretKey:      "test",
		PathStyle:      true,
		PublicEndpoint: "",
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
	if err := client.DeleteObject(ctx, client.Bucket(), "poster.jpg"); err != nil {
		t.Fatalf("DeleteObject() returned error: %v", err)
	}
	if err := client.HeadBucket(ctx, client.Bucket()); err != nil {
		t.Fatalf("HeadBucket() returned error: %v", err)
	}
	if err := client.SetBucketCORS(ctx, client.Bucket(), []string{"*"}); err != nil {
		t.Fatalf("SetBucketCORS() returned error: %v", err)
	}

	requests := srv.Requests()
	paths := make([]string, 0, len(requests))
	for _, req := range requests {
		paths = append(paths, req.Path)
	}

	if !containsRequest(requests, http.MethodPut, "/silo/poster.jpg", "") {
		t.Fatalf("requests = %#v, want PutObject path /silo/poster.jpg", paths)
	}
	if !containsRequest(requests, http.MethodGet, "/silo/poster.jpg", "") {
		t.Fatalf("requests = %#v, want GetObject path /silo/poster.jpg", paths)
	}
	if !containsRequest(requests, http.MethodHead, "/silo/poster.jpg", "") {
		t.Fatalf("requests = %#v, want HeadObject path /silo/poster.jpg", paths)
	}
	if !containsRequest(requests, http.MethodDelete, "/silo/poster.jpg", "") {
		t.Fatalf("requests = %#v, want DeleteObject path /silo/poster.jpg", paths)
	}
	if !containsRequest(requests, http.MethodHead, "/silo", "") {
		t.Fatalf("requests = %#v, want HeadBucket path /silo", paths)
	}
	if !containsRequest(requests, http.MethodPut, "/silo", "cors=") {
		t.Fatalf("requests = %#v, want PutBucketCors path /silo?cors=", requests)
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

func TestClientWithKeyPrefixPrefixesCloudflareTokenURL(t *testing.T) {
	t.Parallel()

	client := NewClient(BucketConfig{
		Endpoint:       "https://s3.example.test",
		PublicEndpoint: "https://cdn.example.test",
		Region:         "us-east-1",
		Bucket:         "silo",
		KeyPrefix:      "silo/dev",
		AccessKey:      "test",
		SecretKey:      "test",
		PathStyle:      true,
		URLAuth:        URLAuthCloudflareToken,
		TokenSecret:    "secret",
	})

	u, err := client.PresignGetURL(context.Background(), client.Bucket(), "poster.jpg", time.Minute)
	if err != nil {
		t.Fatalf("PresignGetURL() returned error: %v", err)
	}
	if !strings.HasPrefix(u, "https://cdn.example.test/silo/dev/poster.jpg?verify=") {
		t.Fatalf("PresignGetURL() = %q, want prefixed Cloudflare token URL", u)
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

func TestClientObjectAvailableUsesStorageWithoutExternalDelivery(t *testing.T) {
	t.Parallel()

	for _, authMode := range []string{URLAuthPresigned, URLAuthPublic, URLAuthCloudflareToken} {
		t.Run(authMode, func(t *testing.T) {
			srv := newS3TestServer(t)
			client := NewClient(BucketConfig{
				Endpoint:  srv.URL(),
				Region:    "us-east-1",
				Bucket:    "silo",
				AccessKey: "test",
				SecretKey: "test",
				PathStyle: true,
				URLAuth:   authMode,
			})

			if client.UsesExternalDelivery() {
				t.Fatal("UsesExternalDelivery() = true without a public endpoint")
			}
			available, err := client.ObjectAvailable(t.Context(), client.Bucket(), "poster.webp")
			if err != nil || !available {
				t.Fatalf("ObjectAvailable() = %v, %v, want true, nil", available, err)
			}
			requests := srv.Requests()
			if !containsRequest(requests, http.MethodHead, "/silo/poster.webp", "") {
				t.Fatalf("requests = %#v, want storage HEAD", requests)
			}
			if containsRequest(requests, http.MethodGet, "/silo/poster.webp", "") {
				t.Fatalf("requests = %#v, do not want a delivery GET without external delivery", requests)
			}
		})
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

func TestClientEffectivePresignTTLPreservesNonTokenAuth(t *testing.T) {
	t.Parallel()

	client := NewClient(BucketConfig{
		Endpoint:  "https://s3.example.test",
		Region:    "us-east-1",
		Bucket:    "silo",
		AccessKey: "test",
		SecretKey: "test",
	})

	if got := client.EffectivePresignTTL(4 * time.Hour); got != 4*time.Hour {
		t.Fatalf("EffectivePresignTTL(4h) = %s, want 4h", got)
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
