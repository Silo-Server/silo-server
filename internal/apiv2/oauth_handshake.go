package apiv2

import (
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// The browser OAuth handshake: the SPA posts the browser to init, which
// redirects it to the provider; the provider redirects it back to callback,
// which redirects it to the SPA's completion page with a one-time code the
// SPA redeems through completeOAuthLogin. Both legs are 302 redirects with
// no JSON body, so they are raw handshakes, not Huma operations. The v2 init
// hands the provider the v2 callback as its redirect URI, so a provider
// configured for v2 must have that URI registered.

const (
	oauthHandshakeTag = "auth"
	inPath            = "path"
	inQuery           = "query"
)

var installIDParam = &huma.Param{
	Name: "install_id", In: inPath, Required: true,
	Description: "The auth plugin installation providing the OAuth flow",
	Schema:      &huma.Schema{Type: huma.TypeString, Pattern: "^[1-9][0-9]*$"},
}

var oauthInitHandshake = RawHandshake{
	Method: http.MethodPost, Path: Prefix + "/auth/oauth/{install_id}/init",
	OperationID: "startOAuthLogin", Tag: oauthHandshakeTag,
	Summary:     "Start a browser OAuth login: redirect the browser to the provider.",
	Description: "A browser form POST. The response is a 302 to the provider's authorize URL; the OAuth state is bound to this server's callback under the same API prefix. A failure is a plain-text status.",
	Protocol:    "redirect", Reason: "browser redirect handshake; no JSON exchange",
	Parameters: []*huma.Param{installIDParam, {
		Name: "next", In: inQuery,
		Description: "Site-relative path to return the browser to after login; / when absent or not site-relative",
		Schema:      &huma.Schema{Type: huma.TypeString},
	}},
	Responses: map[int]string{
		http.StatusFound:               "Redirect to the provider's authorize URL",
		http.StatusBadRequest:          "install_id is not a positive integer (text/plain)",
		http.StatusBadGateway:          "The auth plugin is unavailable or refused to start the flow (text/plain)",
		http.StatusInternalServerError: "The OAuth session could not be recorded (text/plain)",
		http.StatusServiceUnavailable:  "OAuth login is not configured on this server",
	},
	handler: oauthInit,
}

var oauthCallbackHandshake = RawHandshake{
	Method: http.MethodGet, Path: Prefix + "/auth/oauth/{install_id}/callback",
	OperationID: "completeOAuthCallback", Tag: oauthHandshakeTag,
	Summary:     "Provider redirect target: finish the OAuth login and send the browser to the completion page.",
	Description: "The provider redirects the browser here with code and state. The response is a 302 to the SPA's completion page carrying a one-time code for completeOAuthLogin, or to /login?error=oauth_failed&reason=<reason> when the handshake fails. Tokens never appear in the redirect.",
	Protocol:    "redirect", Reason: "browser redirect handshake; no JSON exchange",
	Parameters: []*huma.Param{installIDParam, {
		Name: "code", In: inQuery, Required: true,
		Description: "The provider's authorization code",
		Schema:      &huma.Schema{Type: huma.TypeString},
	}, {
		Name: "state", In: inQuery, Required: true,
		Description: "The signed state init issued",
		Schema:      &huma.Schema{Type: huma.TypeString},
	}},
	Responses: map[int]string{
		http.StatusFound:              "Redirect to the completion page (success) or to the login page with a reason (failure)",
		http.StatusBadRequest:         "install_id is not a positive integer, or code or state is missing (text/plain)",
		http.StatusServiceUnavailable: "OAuth login is not configured on this server",
	},
	handler: oauthCallback,
}

func oauthInstallID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(chi.URLParam(r, "install_id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid install_id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func oauthInit(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.OAuth == nil {
			writeProblem(w, r, unavailable("oauth login"))
			return
		}
		id, ok := oauthInstallID(w, r)
		if !ok {
			return
		}
		authorizeURL, err := deps.OAuth.Init(r.Context(), id, r.URL.Query().Get("next"), deps.OAuth.CallbackURL(Prefix, id))
		if err != nil {
			status, msg := http.StatusInternalServerError, "internal error"
			if he, ok := err.(*auth.OAuthHandshakeError); ok { //nolint:errorlint // Init returns the value directly
				status, msg = he.Status, he.Message
			}
			http.Error(w, msg, status)
			return
		}
		http.Redirect(w, r, authorizeURL, http.StatusFound)
	}
}

func oauthCallback(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.OAuth == nil {
			writeProblem(w, r, unavailable("oauth login"))
			return
		}
		id, ok := oauthInstallID(w, r)
		if !ok {
			return
		}
		state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
		if state == "" || code == "" {
			http.Error(w, "missing code or state", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, deps.OAuth.Callback(r.Context(), auth.OAuthCallbackInput{
			InstallID: id, State: state, Code: code, UserAgent: r.UserAgent(), IP: clientip.FromContext(r.Context()),
		}), http.StatusFound)
	}
}
