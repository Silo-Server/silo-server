package plugins

import (
	"context"
	"errors"
	"net/http"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestHTTPProxyPublicGETRouteMetadataErrors(t *testing.T) {
	for _, expected := range []error{ErrInstallationDisabled, ErrInstallationNotFound, context.Canceled, errors.New("metadata unavailable")} {
		t.Run(expected.Error(), func(t *testing.T) {
			proxy := NewHTTPProxy(httpProxyServiceFunc{routeDescriptors: func(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
				return []*pluginv1.HttpRouteDescriptor{{Method: http.MethodGet, Path: "/assets/icon.svg", Access: "public"}}, expected
			}}, nil)
			public, err := proxy.PublicGETRoute(t.Context(), 42, "/assets/icon.svg")
			if public || !errors.Is(err, expected) {
				t.Fatalf("public=%v err=%v", public, err)
			}
		})
	}
}
