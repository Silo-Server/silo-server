package apiv2

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

const (
	oauthCallbackStep   = "callback"
	oauthStateParameter = "state"
	oauthQueryParameter = "query"
	oauthCodeParameter  = "code"
	oauthPlainText      = "text/plain"
	oauthLocationHeader = "Location"
)

type OAuthHandshakeCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         OAuthHandshakeCapabilitiesOutputBody
}

type OAuthHandshakeCapabilitiesOutputBody struct {
	Capability
	Available bool `json:"available"`
}

func registerOAuthHandshakes(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/auth/oauth/capabilities", "getOAuthHandshakeCapabilities", "auth", "Discover browser OAuth handshake availability."), Class: ClassPublic, ServiceBacked: true}, func(_ context.Context, _ *CapabilityInput) (*OAuthHandshakeCapabilitiesOutput, error) {
		out := new(OAuthHandshakeCapabilitiesOutput)
		out.Body.Available = reg.deps.OAuth != nil
		return out, nil
	})
	for _, route := range []struct{ method, path, id string }{
		{http.MethodPost, "init", "initOAuthLogin"}, {http.MethodGet, oauthCallbackStep, "finishOAuthCallback"},
	} {
		params := []*huma.Param{{Name: "install_id", In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Positive authentication-plugin installation ID."}}
		names := []string{"next"}
		if route.path == oauthCallbackStep {
			names = []string{oauthStateParameter, oauthCodeParameter}
		}
		for _, name := range names {
			params = append(params, &huma.Param{Name: name, In: oauthQueryParameter, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Browser OAuth handshake value; validated by the owning handshake."})
		}
		responses := map[string]*huma.Response{"302": {Description: "Provider authorization or local completion/error redirect.", Headers: map[string]*huma.Header{oauthLocationHeader: {Schema: &huma.Schema{Type: huma.TypeString}}}}}
		for _, status := range []string{"400", "500", "502"} {
			responses[status] = &huma.Response{Description: "Plain-text handshake failure.", Content: map[string]*huma.MediaType{oauthPlainText: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		}
		if route.path == oauthCallbackStep {
			delete(responses, "500")
			delete(responses, "502")
		}
		op := Operation{Operation: huma.Operation{Method: route.method, Path: Prefix + "/auth/oauth/{install_id}/" + route.path, OperationID: route.id, Summary: "Continue the browser OAuth login handshake.", Tags: []string{"auth"}, Parameters: params, Responses: responses}, Class: ClassPublic, ServiceBacked: true}
		if route.method == http.MethodPost {
			op.RetrySafety = RetrySafetyNonRetryable
		}
		RegisterRaw(reg, RawOperation{Operation: op, Protocol: "oauth-redirect", Reason: "Browser form submission and provider callback require HTTP redirects, without JSON negotiation."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			svc := reg.deps.OAuth
			if svc == nil {
				writeProblem(w, r, unavailable("OAuth handshake"))
				return
			}
			installID, err := intOfID(ID(chi.URLParam(r, "install_id")))
			if err != nil || installID <= 0 {
				http.Error(w, "invalid install_id", http.StatusBadRequest)
				return
			}
			if route.path == "init" {
				location, err := svc.Init(r.Context(), installID, r.URL.Query().Get("next"), svc.CallbackURL(Prefix, installID))
				if err != nil {
					if he, ok := errors.AsType[*auth.OAuthHandshakeError](err); ok {
						http.Error(w, he.Message, he.Status)
					} else {
						http.Error(w, "internal error", http.StatusInternalServerError)
					}
					return
				}
				http.Redirect(w, r, location, http.StatusFound)
				return
			}
			state, code := r.URL.Query().Get(oauthStateParameter), r.URL.Query().Get(oauthCodeParameter)
			if state == "" || code == "" {
				http.Error(w, "missing code or state", http.StatusBadRequest)
				return
			}
			ip := strings.TrimSpace(clientip.FromContext(r.Context()))
			if ip == "" {
				ip, _, err = net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					ip = strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
				}
			}
			location := svc.Callback(r.Context(), auth.OAuthCallbackInput{InstallID: installID, State: state, Code: code, UserAgent: r.UserAgent(), IP: ip})
			http.Redirect(w, r, location, http.StatusFound)
		}))
	}
}

func (c OAuthHandshakeCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
