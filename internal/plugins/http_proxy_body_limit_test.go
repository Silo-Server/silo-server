package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/proto"
)

type bodyLimitRouteClient struct {
	calls   int
	lastLen int
}

func (c *bodyLimitRouteClient) Handle(_ context.Context, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	c.calls++
	c.lastLen = len(req.GetBody())
	return &pluginv1.HandleHTTPResponse{StatusCode: http.StatusNoContent}, nil
}

type bodyLimitProxyService struct{ client *bodyLimitRouteClient }

func (bodyLimitProxyService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{{Method: "*", Path: "/api/*", Access: "public"}}, nil
}

func (bodyLimitProxyService) ResolveAssetPath(context.Context, int, string) (string, error) {
	return "", errors.New("no assets")
}

func (s bodyLimitProxyService) HTTPRoutesClient(context.Context, int, string) (httpRouteClient, error) {
	return s.client, nil
}

// chunkedBody hides its length so the request is sent without Content-Length.
type chunkedBody struct{ r io.Reader }

func (b chunkedBody) Read(p []byte) (int, error) { return b.r.Read(p) }

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestHTTPProxyRouteBodyLimit: dynamic plugin route bodies are capped before
// the plugin RPC, for every method and for fixed-length and chunked bodies;
// at-limit bodies still reach the plugin.
func TestHTTPProxyRouteBodyLimit(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		size       int
		chunked    bool
		wantStatus int
		wantCalled bool
	}{
		{"post at limit", http.MethodPost, maxPluginRouteBodyBytes, false, http.StatusNoContent, true},
		{"chunked at limit", http.MethodPut, maxPluginRouteBodyBytes, true, http.StatusNoContent, true},
		{"post over limit", http.MethodPost, maxPluginRouteBodyBytes + 1, false, http.StatusRequestEntityTooLarge, false},
		{"chunked over limit", http.MethodPost, maxPluginRouteBodyBytes + 1, true, http.StatusRequestEntityTooLarge, false},
		{"get with oversized body", http.MethodGet, maxPluginRouteBodyBytes + 1, true, http.StatusRequestEntityTooLarge, false},
		{"empty get", http.MethodGet, 0, false, http.StatusNoContent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &bodyLimitRouteClient{}
			proxy := NewHTTPProxy(bodyLimitProxyService{client: client}, profileTestInstallationStore{})
			var body io.Reader
			if tc.size > 0 {
				body = bytes.NewReader(bytes.Repeat([]byte("a"), tc.size))
				if tc.chunked {
					body = chunkedBody{body}
				}
			}
			req := httptest.NewRequest(tc.method, "/api/v2/plugins/1/api/upload", body)
			if tc.chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			proxy.ServeRoute(rec, req, 1, false, false)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if (client.calls > 0) != tc.wantCalled {
				t.Fatalf("plugin calls = %d, want called %v", client.calls, tc.wantCalled)
			}
			if tc.wantCalled && client.lastLen != tc.size {
				t.Fatalf("forwarded body length = %d, want %d", client.lastLen, tc.size)
			}
		})
	}
}

func TestHTTPProxyRouteBodyReadErrorSkipsPlugin(t *testing.T) {
	client := &bodyLimitRouteClient{}
	proxy := NewHTTPProxy(bodyLimitProxyService{client: client}, profileTestInstallationStore{})
	req := httptest.NewRequest(http.MethodPost, "/api/v2/plugins/1/api/upload", failingBody{})
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	proxy.ServeRoute(rec, req, 1, false, false)
	if rec.Code != http.StatusBadRequest || client.calls != 0 {
		t.Fatalf("status = %d calls = %d, want 400 and no plugin call", rec.Code, client.calls)
	}
}

// TestPluginRouteBodyLimitFitsRPCMessage: an at-limit body plus a realistic
// request envelope still fits the plugin gRPC receive limit.
func TestPluginRouteBodyLimitFitsRPCMessage(t *testing.T) {
	headers := map[string]string{}
	for _, key := range []string{"Accept", "Accept-Language", "Content-Type", "User-Agent", "Origin", "Referer", "X-Silo-User-Name", "X-Silo-Profile-Name"} {
		headers[key] = string(bytes.Repeat([]byte("h"), 8<<10))
	}
	msg := &pluginv1.HandleHTTPRequest{
		Method:  http.MethodPost,
		Path:    "/" + string(bytes.Repeat([]byte("p"), 16<<10)),
		Headers: headers,
		Body:    make([]byte, maxPluginRouteBodyBytes),
		Query:   queryToStruct(map[string][]string{"q": {string(bytes.Repeat([]byte("q"), 32<<10))}}),
	}
	if size := proto.Size(msg); size > pluginRPCMaxMessageBytes {
		t.Fatalf("at-limit request encodes to %d bytes, over the %d-byte RPC limit", size, pluginRPCMaxMessageBytes)
	}
}
