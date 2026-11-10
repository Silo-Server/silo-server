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
	"github.com/Silo-Server/silo-server/internal/clientip"
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

// UserThemeLookup resolves the UI theme for a silo user. The proxy uses it
// to inject X-Silo-Theme on every plugin request so plugin SPAs can paint in
// the host's theme on first byte without relying on the URL ?theme= parameter
// (which is fragile under refresh, direct links, and cross-tab sharing). The
// production lookup is FixedUserThemeLookup: the web client has one theme.
type UserThemeLookup interface {
	LookupUITheme(ctx context.Context, userID int, profileID string) (string, error)
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

// PublicGETRoute checks route metadata using the same matcher as ServeRoute.
// routePath is the decoded plugin-relative path, including its leading slash.
// This does not dispatch a plugin, resolve asset bytes, or prove availability;
// installation and metadata errors are returned unchanged to the caller.
func (p *HTTPProxy) PublicGETRoute(ctx context.Context, installationID int, routePath string) (bool, error) {
	const publicAccess = "public"
	descriptors, err := p.service.RouteDescriptors(ctx, installationID)
	if err != nil {
		return false, err
	}
	descriptor := matchRouteDescriptor(descriptors, http.MethodGet, routePath)
	return descriptor != nil && descriptor.GetAccess() == publicAccess, nil
}

// ServeRoute serves a dynamic plugin route on the frozen /api/v1 mount and
// relays the plugin's status and Location unchanged.
func (p *HTTPProxy) ServeRoute(w http.ResponseWriter, r *http.Request, installationID int, authenticated bool, admin bool) {
	p.serveRoute(w, r, installationID, authenticated, admin, false)
}

// serveRoute serves a dynamic plugin route. With sameOriginRedirects, a
// plugin redirect to another origin than the one the request arrived on is
// answered with 502 instead: a client following it would carry the
// credentials it attached for this server to that origin.
func (p *HTTPProxy) serveRoute(w http.ResponseWriter, r *http.Request, installationID int, authenticated bool, admin bool, sameOriginRedirects bool) {
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

	body, status := readPluginRouteBody(w, r)
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	headers := forwardedRequestHeaders(r.Header)
	if _, _, userID, contextProfileID := pluginAccessUserFromContext(r.Context()); userID > 0 {
		headers["X-Silo-User-Id"] = strconv.Itoa(userID)
		if admin {
			headers["X-Silo-User-Role"] = "admin"
		} else {
			headers["X-Silo-User-Role"] = "user"
		}
		// The access resolver verified this profile: the launch cookie's, or
		// a declared X-Profile-Id that passed the viewer gate's PIN check.
		// An unverified header never reaches here, so the plugin can key
		// per-profile data on the id.
		profileID := strings.TrimSpace(contextProfileID)
		if profileID != "" {
			headers["X-Silo-Profile-Id"] = profileID
		}
		if p.themes != nil {
			if theme, err := p.themes.LookupUITheme(r.Context(), userID, profileID); err == nil && theme != "" {
				headers["X-Silo-Theme"] = theme
			}
		}
		if p.identity != nil {
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
	writePluginRouteResponse(w, r, installationID, response, sameOriginRedirects)
}

// writePluginRouteResponse relays a plugin route's answer: its allowed
// headers, status and body. With sameOriginRedirects, a redirect that leaves
// the request's origin, or whose Location is ambiguous, is a 502 instead (see
// serveRoute).
func writePluginRouteResponse(w http.ResponseWriter, r *http.Request, installationID int, response *pluginv1.HandleHTTPResponse, sameOriginRedirects bool) {
	status := int(response.GetStatusCode())
	if status == 0 {
		status = http.StatusOK
	}
	headers, ambiguousLocation := filteredResponseHeaders(response.GetHeaders())
	if sameOriginRedirects && isRedirectStatus(status) && (ambiguousLocation || redirectLeavesOrigin(r, headers[locationHeader])) {
		slog.WarnContext(r.Context(), "plugin route redirected to another origin or sent several Location headers; not relayed", "component", "plugins", "installation_id", installationID, "status", status)
		http.Error(w, "plugin route redirected to another origin", http.StatusBadGateway)
		return
	}
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	// A plugin answers with its own Content-Type; the browser must not
	// reinterpret the body as something else (script, HTML) on Silo's origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(response.GetBody())
}

// pluginRPCMaxMessageBytes is the gRPC default receive limit (4 MiB). The
// plugin SDK serves plugins with go-plugin's default gRPC server and sets no
// larger limit, so a HandleHTTPRequest bigger than this cannot reach a plugin.
const pluginRPCMaxMessageBytes = 4 << 20

// maxPluginRouteBodyBytes caps a dynamic plugin route's request body. It
// leaves room under the plugin RPC limit for the method, path, forwarded
// headers, and query that travel in the same message.
const maxPluginRouteBodyBytes = pluginRPCMaxMessageBytes - 256<<10

// readPluginRouteBody buffers the request body for the plugin RPC under the
// host cap, whatever the method or transfer encoding. It returns
// http.StatusOK on success, 413 when the body exceeds the cap, and 400 for any
// other read failure; the plugin is never called in the error cases.
func readPluginRouteBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, http.StatusOK
	}
	if r.ContentLength > maxPluginRouteBodyBytes {
		return nil, http.StatusRequestEntityTooLarge
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPluginRouteBodyBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, http.StatusRequestEntityTooLarge
		}
		return nil, http.StatusBadRequest
	}
	return body, http.StatusOK
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
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

// isRedirectStatus reports whether status is a redirection a client follows:
// any 3xx except 304 Not Modified.
func isRedirectStatus(status int) bool {
	return status >= http.StatusMultipleChoices && status < http.StatusBadRequest && status != http.StatusNotModified
}

// redirectLeavesOrigin reports whether location, resolved against r, names
// another origin than the one r arrived on (clientip.RequestOrigin), a
// scheme downgrade included. A relative location stays on the origin; one
// that does not parse is treated as leaving it, and so are two spellings
// that url.Parse reads as a path but browsers and other WHATWG URL parsers
// read as an authority on another host: a backslash (`/\host`, as WHATWG
// reads `\` as `/`) and three or more leading slashes (`///host`, as WHATWG
// skips any number of slashes before the host). (url.Parse already refuses
// the tab, newline, and other control characters those parsers strip.)
func redirectLeavesOrigin(r *http.Request, location string) bool {
	location = strings.TrimSpace(location)
	if strings.Contains(location, `\`) || strings.HasPrefix(location, "///") {
		return true
	}
	origin := clientip.RequestOrigin(r)
	base, err := url.Parse(origin)
	if err != nil {
		return true
	}
	target, err := base.Parse(location)
	if err != nil {
		return true
	}
	return clientip.SerializeOrigin(target.Scheme, target.Host) != origin
}

const locationHeader = "Location"

// filteredResponseHeaders keeps the allowed plugin response headers under
// their canonical names, so each is written once with the value kept here.
// A plugin can send one header under several spellings, and which value
// survives is then unspecified; ambiguousLocation reports that for Location.
func filteredResponseHeaders(headers map[string]string) (filtered map[string]string, ambiguousLocation bool) {
	allowed := map[string]struct{}{
		"content-type":  {},
		"cache-control": {},
		"etag":          {},
		"last-modified": {},
		"location":      {},
	}
	filtered = make(map[string]string, len(headers))
	locations := 0
	for key, value := range headers {
		if _, ok := allowed[strings.ToLower(key)]; !ok {
			continue
		}
		key = http.CanonicalHeaderKey(key)
		if key == locationHeader {
			locations++
		}
		filtered[key] = value
	}
	return filtered, locations > 1
}

type pluginAccessContextKey string

const pluginAccessKey pluginAccessContextKey = "plugin-http-access"

type pluginAccess struct {
	authenticated bool
	admin         bool
	userID        int
	profileID     string
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
func WithPluginAccessUser(
	ctx context.Context, authenticated bool, admin bool, userID int, profileID string,
) context.Context {
	return context.WithValue(ctx, pluginAccessKey, pluginAccess{
		authenticated: authenticated,
		admin:         admin,
		userID:        userID,
		profileID:     profileID,
	})
}

func pluginAccessFromContext(ctx context.Context) (bool, bool) {
	access, ok := ctx.Value(pluginAccessKey).(pluginAccess)
	if !ok {
		return false, false
	}
	return access.authenticated, access.admin
}

// pluginAccessUserFromContext returns the authenticated identity for the
// plugin call. userID is 0 and profileID empty when unauthenticated.
func pluginAccessUserFromContext(ctx context.Context) (bool, bool, int, string) {
	access, ok := ctx.Value(pluginAccessKey).(pluginAccess)
	if !ok {
		return false, false, 0, ""
	}
	return access.authenticated, access.admin, access.userID, access.profileID
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
