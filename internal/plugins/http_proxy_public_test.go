package plugins

import (
	"context"
	"errors"
	"net/http"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestHTTPProxyPublicGETRoute(t *testing.T) {
	const asset = "/assets/brand icon.svg"
	route := func(method, path, access string) *pluginv1.HttpRouteDescriptor {
		return &pluginv1.HttpRouteDescriptor{Method: method, Path: path, Access: access}
	}
	for _, tc := range []struct {
		name   string
		routes []*pluginv1.HttpRouteDescriptor
		path   string
		want   bool
	}{
		{"decoded public asset", []*pluginv1.HttpRouteDescriptor{{Method: http.MethodGet, Path: asset, Access: "public", StaticAsset: true}}, asset, true},
		{"public dynamic route", []*pluginv1.HttpRouteDescriptor{route(http.MethodGet, asset, "public")}, asset, true},
		{"authenticated", []*pluginv1.HttpRouteDescriptor{route(http.MethodGet, asset, "authenticated")}, asset, false},
		{"admin", []*pluginv1.HttpRouteDescriptor{route(http.MethodGet, asset, "admin")}, asset, false},
		{"unknown access", []*pluginv1.HttpRouteDescriptor{route(http.MethodGet, asset, "")}, asset, false},
		{"POST only", []*pluginv1.HttpRouteDescriptor{route(http.MethodPost, asset, "public")}, asset, false},
		{"HEAD only", []*pluginv1.HttpRouteDescriptor{route(http.MethodHead, asset, "public")}, asset, false},
		{"any method", []*pluginv1.HttpRouteDescriptor{route("*", asset, "public")}, asset, true},
		{"empty method", []*pluginv1.HttpRouteDescriptor{route("", asset, "public")}, asset, true},
		{"private exact beats public wildcard", []*pluginv1.HttpRouteDescriptor{route("*", "/*", "public"), route(http.MethodGet, asset, "admin")}, asset, false},
		{"public exact beats private wildcard", []*pluginv1.HttpRouteDescriptor{route("*", "/*", "admin"), route(http.MethodGet, asset, "public")}, asset, true},
		{"longest private wildcard", []*pluginv1.HttpRouteDescriptor{route("*", "/*", "public"), route("*", "/assets/*", "authenticated")}, asset, false},
		{"longest public wildcard", []*pluginv1.HttpRouteDescriptor{route("*", "/assets/*", "public"), route("*", "/*", "admin")}, asset, true},
		{"method filtering before specificity", []*pluginv1.HttpRouteDescriptor{route(http.MethodGet, "/assets/*", "public"), route(http.MethodPost, asset, "admin")}, asset, true},
		{"prefix boundary", []*pluginv1.HttpRouteDescriptor{route("*", "/assets/*", "public")}, "/assets-other/icon.svg", false},
		{"wildcard base", []*pluginv1.HttpRouteDescriptor{route("*", "/assets/*", "public")}, "/assets", true},
		{"does not decode again", []*pluginv1.HttpRouteDescriptor{route(http.MethodGet, asset, "public")}, "/assets/brand%20icon.svg", false},
		{"missing", nil, asset, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			calls := 0
			proxy := NewHTTPProxy(httpProxyServiceFunc{routeDescriptors: func(got context.Context, id int) ([]*pluginv1.HttpRouteDescriptor, error) {
				calls++
				if got != ctx || id != 42 {
					t.Fatal("metadata authority changed")
				}
				return tc.routes, nil
			}}, nil)
			// Nil dispatch/asset callbacks deliberately fail if this proof touches bytes.
			proof := proxy.PublicGETRoute
			got, err := proof(ctx, 42, tc.path)
			if err != nil || got != tc.want || calls != 1 {
				t.Fatalf("public=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

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
