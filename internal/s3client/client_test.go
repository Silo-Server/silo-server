package s3client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Body     string
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

// TestListPrefixAnchorsEmptyLogicalPrefixOnTheKeyPrefixBoundary pins the one
// case where a listing Prefix may not be prefixedKey. With an empty logical
// prefix, prefixedKey returns the bare key prefix, which as a listing Prefix
// also matches sibling key spaces that merely start with it — "assets" would
// list "assets-old/..." too. stripKeyPrefix then drops those foreign keys, so a
// truncated page can come back with no objects and an unchanged cursor, which
// callers proving authoritative emptiness or paging an inventory read as a
// listing that refuses to advance.
func TestListPrefixAnchorsEmptyLogicalPrefixOnTheKeyPrefixBoundary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		keyPrefix string
		prefix    string
		want      string
	}{
		{name: "key prefix with empty logical prefix", keyPrefix: "assets", prefix: "", want: "assets/"},
		{name: "key prefix with slash-only logical prefix", keyPrefix: "assets", prefix: "/", want: "assets/"},
		{name: "key prefix with logical prefix", keyPrefix: "assets", prefix: "artwork/v1/", want: "assets/artwork/v1/"},
		{name: "no key prefix with empty logical prefix", keyPrefix: "", prefix: "", want: ""},
		{name: "no key prefix with logical prefix", keyPrefix: "", prefix: "artwork/v1/", want: "artwork/v1/"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := NewClient(BucketConfig{
				Endpoint:  "https://s3.example.invalid",
				Region:    "us-east-1",
				Bucket:    "silo",
				KeyPrefix: testCase.keyPrefix,
				AccessKey: "test",
				SecretKey: "test",
				PathStyle: true,
			})
			if got := client.listPrefix(testCase.prefix); got != testCase.want {
				t.Fatalf("listPrefix(%q) = %q, want %q", testCase.prefix, got, testCase.want)
			}
		})
	}
}

// TestListObjectInfosPageScopesEmptyPrefixToTheKeyPrefixTree checks the wiring:
// the emitted S3 Prefix must carry the trailing separator so a neighboring
// "silo/development/..." key space cannot leak into this client's listing.
func TestListObjectInfosPageScopesEmptyPrefixToTheKeyPrefixTree(t *testing.T) {
	t.Parallel()

	srv := newS3TestServer(t)
	client := NewClient(BucketConfig{
		Endpoint:  srv.URL(),
		Region:    "us-east-1",
		Bucket:    "silo",
		KeyPrefix: "silo/dev",
		AccessKey: "test",
		SecretKey: "test",
		PathStyle: true,
	})

	if _, _, _, err := client.ListObjectInfosPage(context.Background(), client.Bucket(), "", "", 10); err != nil {
		t.Fatalf("ListObjectInfosPage() returned error: %v", err)
	}

	requests := srv.Requests()
	listReq := findRequest(requests, http.MethodGet, "/silo", "list-type=2")
	if got := parseQuery(listReq.RawQuery).Get("prefix"); got != "silo/dev/" {
		t.Fatalf("list prefix = %q, want silo/dev/", got)
	}
}
