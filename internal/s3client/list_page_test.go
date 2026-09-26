package s3client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestListObjectInfosPagePreservesStorageBoundary(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("prefix") != "tenant/catalog-seeds/" || q.Get("max-keys") != "2" {
			t.Errorf("unexpected listing query: %v", q)
		}
		w.Header().Set("Content-Type", "application/xml")
		switch q.Get("continuation-token") {
		case "":
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>opaque+next</NextContinuationToken><Contents><Key>tenant/catalog-seeds/a.json.gz</Key><Size>12</Size></Contents><Contents><Key>another-tenant/hidden.json.gz</Key><Size>99</Size></Contents></ListBucketResult>`)
		case "opaque+next":
			_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>tenant/catalog-seeds/b.json.gz</Key><Size>24</Size></Contents></ListBucketResult>`)
		default:
			t.Errorf("unexpected continuation: %q", q.Get("continuation-token"))
		}
	}))
	defer server.Close()
	client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", KeyPrefix: "tenant", AccessKey: "test", SecretKey: "test", PathStyle: true})
	items, next, err := client.ListObjectInfosPage(t.Context(), "silo", "catalog-seeds/", "", 2)
	if err != nil || len(items) != 1 || items[0].Key != "catalog-seeds/a.json.gz" || items[0].SizeBytes != 12 || next != "opaque+next" || calls.Load() != 1 {
		t.Fatalf("first page = %#v, %q, %v; calls=%d", items, next, err, calls.Load())
	}
	items, next, err = client.ListObjectInfosPage(t.Context(), "silo", "catalog-seeds/", next, 2)
	if err != nil || len(items) != 1 || items[0].Key != "catalog-seeds/b.json.gz" || next != "" || calls.Load() != 2 {
		t.Fatalf("second page = %#v, %q, %v; calls=%d", items, next, err, calls.Load())
	}
	for _, limit := range []int{0, -1, 1001} {
		if _, _, err := client.ListObjectInfosPage(t.Context(), "silo", "catalog-seeds/", "", limit); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid limits reached storage")
	}
}

func TestListObjectInfosPageRejectsNonadvancingToken(t *testing.T) {
	for _, next := range []string{"", "same"} {
		t.Run(next, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>%s</NextContinuationToken></ListBucketResult>`, next)
			}))
			defer server.Close()
			client := NewClient(BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "silo", AccessKey: "test", SecretKey: "test", PathStyle: true})
			if _, _, err := client.ListObjectInfosPage(t.Context(), "silo", "catalog-seeds/", "same", 2); err == nil {
				t.Fatal("accepted nonadvancing token")
			}
		})
	}
}
