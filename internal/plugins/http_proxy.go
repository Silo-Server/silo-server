package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type httpRouteClient interface {
	Handle(ctx context.Context, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error)
}

type httpProxyService interface {
	RouteDescriptors(ctx context.Context, installationID int) ([]*pluginv1.HttpRouteDescriptor, error)
	ResolveAssetPath(ctx context.Context, installationID int, assetPath string) (string, error)
	HTTPRoutesClient(ctx context.Context, installationID int, capabilityID string) (httpRouteClient, error)
}

// UserThemeLookup resolves the active UI theme for a silo user. The
// proxy uses it to inject X-Silo-Theme on every plugin request so
// plugin SPAs can paint in the user's theme on first byte without relying
// on the URL ?theme= parameter (which is fragile under refresh, direct
// links, and cross-tab sharing).
type UserThemeLookup interface {
	LookupUITheme(ctx context.Context, userID int) (string, error)
}

type HTTPProxy struct {
	service       httpProxyService
	installations taskInstallationStore
	themes        UserThemeLookup
	identity      UserIdentityLookup
}

func NewHTTPProxy(service httpProxyService, installations taskInstallationStore) *HTTPProxy {
	return &HTTPProxy{
		service:       service,
		installations: installations,
	}
}

// WithUserThemeLookup attaches a theme resolver. When set, ServeRoute injects
// X-Silo-Theme on the upstream plugin request for authenticated users.
// Pass nil to disable.
func (p *HTTPProxy) WithUserThemeLookup(t UserThemeLookup) *HTTPProxy {
	p.themes = t
	return p
}

// WithUserIdentityLookup attaches a username/profile-name resolver. When set,
// ServeRoute injects X-Silo-User-Name, X-Silo-Profile-Name, and
// X-Silo-Profile-Primary headers so plugins can render "user#profile"
// strings without reaching back into browser localStorage.
func (p *HTTPProxy) WithUserIdentityLookup(l UserIdentityLookup) *HTTPProxy {
	p.identity = l
	return p
}

func NewHTTPProxyWithTypedResolver(
	service interface {
		RouteDescriptors(ctx context.Context, installationID int) ([]*pluginv1.HttpRouteDescriptor, error)
		ResolveAssetPath(ctx context.Context, installationID int, assetPath string) (string, error)
		HTTPRoutesClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.HTTPRoutesClient, error)
	},
	installations taskInstallationStore,
) *HTTPProxy {
	return NewHTTPProxy(httpProxyServiceFunc{
		routeDescriptors: service.RouteDescriptors,
		resolveAssetPath: service.ResolveAssetPath,
		httpRoutesClient: func(ctx context.Context, installationID int, capabilityID string) (httpRouteClient, error) {
			return service.HTTPRoutesClient(ctx, installationID, capabilityID)
		},
	}, installations)
}

func (p *HTTPProxy) ServeRoute(w http.ResponseWriter, r *http.Request, installationID int, authenticated bool, admin bool) {
	descriptors, err := p.service.RouteDescriptors(r.Context(), installationID)
	if err != nil {
		if errors.Is(err, ErrInstallationDisabled) || errors.Is(err, ErrInstallationNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "plugin routes unavailable", http.StatusBadGateway)
		return
	}

	routePath := requestPluginSubpath(r.URL.Path)
	descriptor := matchRouteDescriptor(descriptors, r.Method, routePath)
	if descriptor == nil {
		http.NotFound(w, r)
		return
	}
	if status := routeAccessStatus(descriptor.GetAccess(), authenticated, admin); status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if descriptor.GetStaticAsset() {
		p.serveResolvedAsset(w, r, installationID, strings.TrimPrefix(routePath, "/"))
		return
	}

	capabilityID, err := p.resolveHTTPRoutesCapability(r.Context(), installationID)
	if err != nil {
		http.Error(w, "plugin routes unavailable", http.StatusBadGateway)
		return
	}

	client, err := p.service.HTTPRoutesClient(r.Context(), installationID, capabilityID)
	if err != nil {
		slog.ErrorContext(r.Context(), "plugin HTTPRoutesClient failed", "component", "plugins", "installation_id", installationID, "capability_id", capabilityID, "error", err)
		http.Error(w, "plugin routes unavailable", http.StatusBadGateway)
		return
	}

	body, _ := io.ReadAll(r.Body)
	headers := forwardedRequestHeaders(r.Header)
	if _, _, userID := pluginAccessUserFromContext(r.Context()); userID > 0 {
		headers["X-Silo-User-Id"] = strconv.Itoa(userID)
		if admin {
			headers["X-Silo-User-Role"] = "admin"
		} else {
			headers["X-Silo-User-Role"] = "user"
		}
		if p.themes != nil {
			if theme, err := p.themes.LookupUITheme(r.Context(), userID); err == nil && theme != "" {
				headers["X-Silo-Theme"] = theme
			}
		}
		if p.identity != nil {
			// The browser already sends X-Profile-Id for its own silo
			// API calls; reuse that as the active profile. Empty value just
			// means "no profile selected" — the lookup returns username
			// only, primary-profile path.
			profileID := r.Header.Get("X-Profile-Id")
			if ident, err := p.identity.LookupIdentity(r.Context(), userID, profileID); err == nil {
				if ident.Username != "" {
					headers["X-Silo-User-Name"] = ident.Username
				}
				if ident.ProfileName != "" {
					headers["X-Silo-Profile-Name"] = ident.ProfileName
				}
				if ident.ProfileIsPrimary {
					headers["X-Silo-Profile-Primary"] = "true"
				}
			}
		}
	}
	response, err := client.Handle(r.Context(), &pluginv1.HandleHTTPRequest{
		Method:  r.Method,
		Path:    routePath,
		Headers: headers,
		Body:    body,
		Query:   queryToStruct(r.URL.Query()),
	})
	if err != nil {
		http.Error(w, "plugin route failed", http.StatusBadGateway)
		return
	}

	for key, value := range filteredResponseHeaders(response.GetHeaders()) {
		w.Header().Set(key, value)
	}
	if response.GetStatusCode() == 0 {
		response.StatusCode = http.StatusOK
	}
	w.WriteHeader(int(response.GetStatusCode()))
	_, _ = w.Write(response.GetBody())
}

func (p *HTTPProxy) ServeAsset(w http.ResponseWriter, r *http.Request, installationID int, assetPath string) {
	descriptors, err := p.service.RouteDescriptors(r.Context(), installationID)
	if err != nil {
		if errors.Is(err, ErrInstallationDisabled) || errors.Is(err, ErrInstallationNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "plugin routes unavailable", http.StatusBadGateway)
		return
	}
	routePath := "/" + strings.TrimPrefix(assetPath, "/")
	descriptor := matchRouteDescriptor(descriptors, http.MethodGet, routePath)
	if descriptor == nil || !descriptor.GetStaticAsset() {
		http.NotFound(w, r)
		return
	}
	authenticated, admin := pluginAccessFromContext(r.Context())
	if status := routeAccessStatus(descriptor.GetAccess(), authenticated, admin); status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	p.serveResolvedAsset(w, r, installationID, strings.TrimPrefix(routePath, "/"))
}

func (p *HTTPProxy) serveResolvedAsset(w http.ResponseWriter, r *http.Request, installationID int, assetPath string) {
	resolvedPath, err := p.service.ResolveAssetPath(r.Context(), installationID, assetPath)
	if err != nil {
		if errors.Is(err, ErrInstallationDisabled) || errors.Is(err, ErrInstallationNotFound) {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, resolvedPath)
}

func (p *HTTPProxy) resolveHTTPRoutesCapability(ctx context.Context, installationID int) (string, error) {
	capabilities, err := p.installations.ListCapabilities(ctx, installationID)
	if err != nil {
		return "", err
	}
	for _, capability := range capabilities {
		if capability != nil && capability.Type == "http_routes.v1" {
			return capability.ID, nil
		}
	}
	return "", fmt.Errorf("http_routes.v1 capability not found")
}

func requestPluginSubpath(path string) string {
	if idx := strings.Index(path, "/plugins/"); idx >= 0 {
		path = path[idx+len("/plugins/"):]
		if slash := strings.Index(path, "/"); slash >= 0 {
			return path[slash:]
		}
	}
	return path
}

func matchRouteDescriptor(
	descriptors []*pluginv1.HttpRouteDescriptor,
	method string,
	path string,
) *pluginv1.HttpRouteDescriptor {
	// Two passes: prefer exact matches, fall back to wildcard prefixes so an
	// exact "/api/me/quota" wins over a generic "/*" catch-all.
	var wildcardMatch *pluginv1.HttpRouteDescriptor
	for _, descriptor := range descriptors {
		if !methodMatches(descriptor.GetMethod(), method) {
			continue
		}
		dPath := descriptor.GetPath()
		if dPath == path {
			return descriptor
		}
		if strings.HasSuffix(dPath, "/*") {
			prefix := strings.TrimSuffix(dPath, "/*")
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				if wildcardMatch == nil || len(dPath) > len(wildcardMatch.GetPath()) {
					wildcardMatch = descriptor
				}
			}
		}
	}
	return wildcardMatch
}

// methodMatches treats "" or "*" on the descriptor as "any HTTP method".
func methodMatches(descriptorMethod, requestMethod string) bool {
	if descriptorMethod == "" || descriptorMethod == "*" {
		return true
	}
	return descriptorMethod == requestMethod
}

func routeAccessStatus(access string, authenticated bool, admin bool) int {
	switch access {
	case "public":
		return http.StatusOK
	case "authenticated":
		if authenticated || admin {
			return http.StatusOK
		}
		return http.StatusUnauthorized
	case "admin":
		if admin {
			return http.StatusOK
		}
		if authenticated {
			return http.StatusForbidden
		}
		return http.StatusUnauthorized
	default:
		if authenticated || admin {
			return http.StatusForbidden
		}
		return http.StatusUnauthorized
	}
}

func forwardedRequestHeaders(headers http.Header) map[string]string {
	allowed := map[string]struct{}{
		"accept":            {},
		"accept-language":   {},
		"content-type":      {},
		"if-none-match":     {},
		"if-modified-since": {},
		"range":             {},
		"user-agent":        {},
		"origin":            {},
		"referer":           {},
	}
	values := make(map[string]string, len(headers))
	for key, headerValues := range headers {
		if _, ok := allowed[strings.ToLower(key)]; ok && len(headerValues) > 0 {
			values[key] = headerValues[0]
		}
	}
	return values
}

func filteredResponseHeaders(headers map[string]string) map[string]string {
	allowed := map[string]struct{}{
		"content-type":  {},
		"cache-control": {},
		"etag":          {},
		"last-modified": {},
		"location":      {},
	}
	filtered := make(map[string]string, len(headers))
	for key, value := range headers {
		if _, ok := allowed[strings.ToLower(key)]; ok {
			filtered[key] = value
		}
	}
	return filtered
}

type pluginAccessContextKey string

const pluginAccessKey pluginAccessContextKey = "plugin-http-access"

type pluginAccess struct {
	authenticated bool
	admin         bool
	userID        int
}

func WithPluginAccess(ctx context.Context, authenticated bool, admin bool) context.Context {
	return context.WithValue(ctx, pluginAccessKey, pluginAccess{
		authenticated: authenticated,
		admin:         admin,
	})
}

// WithPluginAccessUser is the same as WithPluginAccess but also stores the
// authenticated user's ID so the proxy can stamp identity headers on
// outgoing plugin requests.
func WithPluginAccessUser(ctx context.Context, authenticated bool, admin bool, userID int) context.Context {
	return context.WithValue(ctx, pluginAccessKey, pluginAccess{
		authenticated: authenticated,
		admin:         admin,
		userID:        userID,
	})
}

func pluginAccessFromContext(ctx context.Context) (bool, bool) {
	access, ok := ctx.Value(pluginAccessKey).(pluginAccess)
	if !ok {
		return false, false
	}
	return access.authenticated, access.admin
}

// pluginAccessUserFromContext returns (authenticated, admin, userID) for the
// plugin call. userID is 0 when the request is unauthenticated.
func pluginAccessUserFromContext(ctx context.Context) (bool, bool, int) {
	access, ok := ctx.Value(pluginAccessKey).(pluginAccess)
	if !ok {
		return false, false, 0
	}
	return access.authenticated, access.admin, access.userID
}

func queryToStruct(values url.Values) *structpb.Struct {
	payload := map[string]any{}
	for key, entries := range values {
		if len(entries) > 0 {
			payload[key] = entries[0]
		}
	}
	if len(payload) == 0 {
		return nil
	}
	result, err := structpb.NewStruct(payload)
	if err != nil {
		return nil
	}
	return result
}

type httpProxyServiceFunc struct {
	routeDescriptors func(ctx context.Context, installationID int) ([]*pluginv1.HttpRouteDescriptor, error)
	resolveAssetPath func(ctx context.Context, installationID int, assetPath string) (string, error)
	httpRoutesClient func(ctx context.Context, installationID int, capabilityID string) (httpRouteClient, error)
}

func (f httpProxyServiceFunc) RouteDescriptors(ctx context.Context, installationID int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return f.routeDescriptors(ctx, installationID)
}

func (f httpProxyServiceFunc) ResolveAssetPath(ctx context.Context, installationID int, assetPath string) (string, error) {
	return f.resolveAssetPath(ctx, installationID, assetPath)
}

func (f httpProxyServiceFunc) HTTPRoutesClient(ctx context.Context, installationID int, capabilityID string) (httpRouteClient, error) {
	return f.httpRoutesClient(ctx, installationID, capabilityID)
}
