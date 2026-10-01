package apiv2

import (
	"context"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

const pluginContentExtension = "x-silo-plugin-content"

type PluginContentService interface {
	http.Handler
	ContentAvailable() bool
}

type PluginContentCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         Capability
}

type pluginContentMount struct {
	Path          string   `json:"path"`
	Methods       []string `json:"methods"`
	Request       string   `json:"request"`
	Response      string   `json:"response"`
	Authorization string   `json:"authorization"`
}

type pluginContentDescription struct {
	Prefix      string               `json:"prefix"`
	Mounts      []pluginContentMount `json:"mounts"`
	Limitations []string             `json:"limitations"`
}

func describePluginContent() pluginContentDescription {
	methods := []string{http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace}
	mounts := []pluginContentMount{}
	for _, suffix := range []string{"/plugins/{installation_id}", "/plugins/{installation_id}/*"} {
		mounts = append(mounts, pluginContentMount{Path: plugins.ContentPrefix + suffix, Methods: methods, Request: "Plugin descriptor and implementation define the request body and query; existing proxy buffering and header filters apply.", Response: "Plugin-defined status, headers and bytes; missing trailing slash redirects308 before proxy dispatch. No finite response schema.", Authorization: "Existing optional session/launch-cookie/unscoped-key resolution followed by the plugin route descriptor's public/authenticated/admin policy."})
	}
	for _, suffix := range []string{"/plugin-assets/{installation_id}", "/plugin-assets/{installation_id}/*"} {
		mounts = append(mounts, pluginContentMount{Path: plugins.ContentPrefix + suffix, Methods: []string{http.MethodGet}, Request: "Static asset path; no asset path returns404.", Response: "Resolved file through http.ServeFile, including content-specific media, conditions and ranges.", Authorization: "Existing asset session/launch-cookie resolution followed by the static descriptor access policy; API keys are not accepted for this mount."})
	}
	return pluginContentDescription{Prefix: plugins.ContentPrefix, Mounts: mounts, Limitations: []string{
		"Dynamic plugin content is excluded from native finite operation schemas; this description is not a plugin-specific API contract.",
		"Missing proxy dependency returns plain-text503. Existing proxy error, buffering and resource limits remain unchanged; HTTPRoutes RPC is not an arbitrary streaming or websocket tunnel.",
		"Plugin-generated absolute links and redirects are not rewritten. Each affected plugin needs explicit compatibility evidence before browser adoption.",
		"V1 mounts remain unchanged. Launch-cookie/navigation and shared href adoption are separate reviewed dependencies; no cookie is broadened here.",
	}}
}

// registerPluginContent is deliberately specific to the documented plugin
// exclusion. It cannot exempt an ordinary operation or the finite raw registry.
// Both generation and runtime consume the same closed mount list.
func registerPluginContent(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, plugins.ContentPrefix+"/capabilities", "getPluginContentCapabilities", "plugins", "Discover whether the dynamic plugin-content proxy is configured."), Class: ClassPublic, ServiceBacked: true}, func(context.Context, *CapabilityInput) (*PluginContentCapabilitiesOutput, error) {
		state := StateNotConfigured
		if reg.deps.PluginContent != nil && reg.deps.PluginContent.ContentAvailable() {
			state = StateAvailable
		}
		return &PluginContentCapabilitiesOutput{Body: Capability{State: state}}, nil
	})
	for _, mount := range describePluginContent().Mounts {
		for _, method := range mount.Methods {
			op := &huma.Operation{Method: method, Path: mount.Path}
			reg.api.Adapter().Handle(op, func(ctx huma.Context) {
				r, w := humachi.Unwrap(ctx)
				if reg.deps.PluginContent == nil {
					http.Error(w, "plugin content unavailable", http.StatusServiceUnavailable)
					return
				}
				reg.deps.PluginContent.ServeHTTP(w, r)
			})
		}
	}
}

// pluginContentAllowedMethods covers only the closed dynamic mounts. It does
// not change finite operation matching or admit unrelated wildcard routes.
func pluginContentAllowedMethods(path string) []string {
	for _, mount := range describePluginContent().Mounts {
		pattern := strings.TrimSuffix(mount.Path, "/*")
		candidate := path
		if strings.HasSuffix(mount.Path, "/*") {
			prefix := strings.Split(pattern, "{")[0]
			rest, ok := strings.CutPrefix(path, prefix)
			if !ok {
				continue
			}
			id, _, slash := strings.Cut(rest, "/")
			if !slash {
				continue
			}
			candidate = prefix + id
		}
		if pathMatches(pattern, candidate) {
			return mount.Methods
		}
	}
	return nil
}
