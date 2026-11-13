package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type retryRouteClient struct{ err error }

func (c retryRouteClient) Handle(context.Context, *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	return nil, c.err
}

type retryProxyService struct{ err error }

func (retryProxyService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{{Method: http.MethodPost, Path: "/report", Access: "authenticated"}}, nil
}

func (retryProxyService) ResolveAssetPath(context.Context, int, string) (string, error) {
	return "", errors.New("no assets")
}

func (s retryProxyService) HTTPRoutesClient(context.Context, int, string) (httpRouteClient, error) {
	return retryRouteClient{err: s.err}, nil
}

// blockWithDelay is the error a plugin reports a provider-wide block with: the
// host's typed retry contract, carrying the delay the provider asked for.
func blockWithDelay(delay time.Duration) error {
	st := status.New(codes.ResourceExhausted, "introdb: blocked")
	withDetail, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(delay)})
	if err != nil {
		panic(err)
	}
	return withDetail.Err()
}

// TestHTTPProxyRelaysAProviderBlocksDelay: a route that fails with the typed
// retry contract tells the viewer to come back rather than showing a failure of
// the request, and the delay the provider asked for reaches the client as
// Retry-After. A block with no delay is still a "not now": the caller is never
// told to retry immediately, which is what a zero-seconds header would say.
func TestHTTPProxyRelaysAProviderBlocksDelay(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantCode  int
		wantAfter string
	}{
		{"a delay is relayed", blockWithDelay(90 * time.Second), http.StatusTooManyRequests, "90"},
		{"a fraction of a second rounds up", blockWithDelay(500 * time.Millisecond), http.StatusTooManyRequests, "1"},
		{"a block without a delay still says not now", status.Error(codes.ResourceExhausted, "blocked"), http.StatusTooManyRequests, ""},
		{"any other failure is the route's own", status.Error(codes.Internal, "boom"), http.StatusBadGateway, ""},
		{"a plain error is the route's own", errors.New("no plugin"), http.StatusBadGateway, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := NewHTTPProxy(retryProxyService{err: tc.err}, profileTestInstallationStore{})
			req := httptest.NewRequest(http.MethodPost, "/api/v2/plugins/1/report", nil)
			req = req.WithContext(WithPluginAccess(req.Context(), true, false))
			rec := httptest.NewRecorder()

			proxy.serveRoute(rec, req, 1, true, false)

			if rec.Code != tc.wantCode || rec.Header().Get("Retry-After") != tc.wantAfter {
				t.Fatalf("status = %d Retry-After = %q, want %d and %q",
					rec.Code, rec.Header().Get("Retry-After"), tc.wantCode, tc.wantAfter)
			}
		})
	}
}
