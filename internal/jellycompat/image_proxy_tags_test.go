package jellycompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompatImageProxyTagVariantMiddlewareSuffixesInfuseImageTags(t *testing.T) {
	codec := NewResourceIDCodec()
	handler := compatImageProxyTagVariantMiddleware(codec)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items: []baseItemDTO{{
				ID:                      "item-1",
				Name:                    "Movie",
				ImageTags:               map[string]string{"Primary": "primary-tag"},
				BackdropImageTags:       []string{"backdrop-tag"},
				SeriesPrimaryImageTag:   "series-tag",
				ParentBackdropImageTags: []string{"parent-backdrop-tag"},
				ParentThumbImageTag:     "parent-thumb-tag",
				ImageBlurHashes: map[string]map[string]string{
					"Primary": {"primary-tag": "thumbhash"},
				},
			}},
			TotalRecordCount: 1,
		})
	}))

	req := httptest.NewRequest(http.MethodGet, "/Items", nil)
	req.Header.Set("User-Agent", "Infuse-Direct/8.4.6")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	var got queryResultDTO
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	item := got.Items[0]
	if item.ImageTags["Primary"] != "primary-tag-p" {
		t.Fatalf("primary tag = %q, want primary-tag-p", item.ImageTags["Primary"])
	}
	if item.PrimaryImageItemID != compatImageProxyRouteID(codec, "item-1") {
		t.Fatalf("PrimaryImageItemId = %q, want proxy route id", item.PrimaryImageItemID)
	}
	if canonical, ok := canonicalCompatImageRouteID(codec, item.PrimaryImageItemID); !ok || canonical != "item-1" {
		t.Fatalf("canonical proxy route = %q, %v; want item-1, true", canonical, ok)
	}
	if item.BackdropImageTags[0] != "backdrop-tag-p" {
		t.Fatalf("backdrop tag = %q, want backdrop-tag-p", item.BackdropImageTags[0])
	}
	if item.SeriesPrimaryImageTag != "series-tag-p" {
		t.Fatalf("series tag = %q, want series-tag-p", item.SeriesPrimaryImageTag)
	}
	if item.ParentBackdropImageTags[0] != "parent-backdrop-tag-p" {
		t.Fatalf("parent backdrop tag = %q, want parent-backdrop-tag-p", item.ParentBackdropImageTags[0])
	}
	if item.ParentThumbImageTag != "parent-thumb-tag-p" {
		t.Fatalf("parent thumb tag = %q, want parent-thumb-tag-p", item.ParentThumbImageTag)
	}
	if _, ok := item.ImageBlurHashes["Primary"]["primary-tag-p"]; !ok {
		t.Fatalf("blurhash keys = %#v, want suffixed primary tag", item.ImageBlurHashes["Primary"])
	}
}

func TestCompatImageProxyTagVariantMiddlewareLeavesOtherClientsUnchanged(t *testing.T) {
	codec := NewResourceIDCodec()
	handler := compatImageProxyTagVariantMiddleware(codec)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items: []baseItemDTO{{
				ID:        "item-1",
				Name:      "Movie",
				ImageTags: map[string]string{"Primary": "primary-tag"},
			}},
			TotalRecordCount: 1,
		})
	}))

	req := httptest.NewRequest(http.MethodGet, "/Items", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	var got queryResultDTO
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Items[0].ImageTags["Primary"] != "primary-tag" {
		t.Fatalf("primary tag = %q, want primary-tag", got.Items[0].ImageTags["Primary"])
	}
	if got.Items[0].PrimaryImageItemID != "" {
		t.Fatalf("PrimaryImageItemId = %q, want empty", got.Items[0].PrimaryImageItemID)
	}
}

func TestCompatImageProxyTagVariantMiddlewareForwardsPreBodyFlushForStreamingResponse(t *testing.T) {
	codec := NewResourceIDCodec()
	handler := compatImageProxyTagVariantMiddleware(codec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapped response writer does not implement http.Flusher")
		}
		flusher.Flush()
		_, _ = w.Write([]byte("data: ready\n\n"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/Events", nil)
	req.Header.Set("User-Agent", "Infuse-Direct/8.4.6")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !rec.Flushed {
		t.Fatal("pre-body flush did not reach the underlying response writer")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "data: ready\n\n" {
		t.Fatalf("status = %d, body = %q; want streamed 200 response", rec.Code, rec.Body.String())
	}
}

func TestCompatImageProxyRouteIDCanonicalizesNumericRouteWithoutRegistration(t *testing.T) {
	routeID := EncodeNumericID(EncodedIDItem, 12345).String()
	proxyRouteID := compatImageProxyRouteID(NewResourceIDCodec(), routeID)

	canonical, ok := canonicalCompatImageRouteID(NewResourceIDCodec(), proxyRouteID)
	if !ok || canonical != routeID {
		t.Fatalf("canonical proxy route = %q, %v; want %q, true", canonical, ok, routeID)
	}
}
