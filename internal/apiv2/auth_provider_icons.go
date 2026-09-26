package apiv2

import (
	"context"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// AuthProviderIconPublic checks the proxy's selected GET descriptor without
// dispatching plugin content. Exact/wildcard precedence belongs to the proxy.
type AuthProviderIconPublic func(context.Context, int, string) (bool, error)

// legacyPluginAssets is the alpha-era plugin asset namespace. Runtime config
// mints the versioned form, but a capability manifest's icon_url may still
// spell this one; it is projected onto the versioned mount so the icon keeps
// resolving after the /api/v1 tombstone.
const legacyPluginAssets = "/api/v1/plugins/"

// authProviderIcon validates a plugin-served icon before a pre-login client is
// told to fetch it: the URL has to address this installation's own asset route
// under the versioned plugin-content mount (or its legacy alias, which is
// projected there), and the proxy has to report that route as public.
// Anything else is dropped; a non-local URL is passed through untouched.
func (reg *Registry) authProviderIcon(ctx context.Context, provider auth.LoginProviderInfo) string {
	pluginAssets := plugins.ContentPrefix + "/plugins/"
	prefix := pluginAssets
	switch {
	case strings.HasPrefix(provider.IconURL, pluginAssets):
	case strings.HasPrefix(provider.IconURL, legacyPluginAssets):
		prefix = legacyPluginAssets
	default:
		return provider.IconURL
	}
	u, err := url.Parse(provider.IconURL)
	if err != nil || u.IsAbs() || u.Host != "" || strings.Contains(u.Path, "\\") || path.Clean(u.Path) != u.Path {
		return ""
	}
	rest, ok := strings.CutPrefix(u.Path, prefix)
	if !ok {
		return ""
	}
	id, asset, ok := strings.Cut(rest, "/")
	if !ok || provider.InstallationID <= 0 || id != strconv.Itoa(provider.InstallationID) || !strings.HasPrefix(asset, "assets/") || asset == "assets/" {
		return ""
	}
	if reg.deps.AuthProviderIconPublic == nil || reg.deps.PluginContent == nil || !reg.deps.PluginContent.ContentAvailable() {
		return ""
	}
	public, err := reg.deps.AuthProviderIconPublic(ctx, provider.InstallationID, "/"+asset)
	if err != nil || !public {
		return ""
	}
	if prefix == legacyPluginAssets {
		u.Path = pluginAssets + rest
		return u.String()
	}
	return provider.IconURL
}
