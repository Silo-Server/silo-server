package apiv2

import (
	"context"
	"net/http"
)

// AppKeyService is the app key storage behind the /auth/keys operations
// (docs/architecture/app-keys.md).
type AppKeyService interface {
	// AppKeySignInEnabled reads the auth.app_key_sign_in server setting.
	AppKeySignInEnabled(context.Context) (bool, error)
}

// Suites the app key operations speak. Signed requests use ES256 over
// P-256; replies that carry session tokens are sealed with an ECDH P-256
// key agreement, HKDF-SHA256 and AES-256-GCM.
const (
	appKeySignatureSuite   = "ES256"
	appKeySealedReplySuite = "P256-HKDF-SHA256-AES256GCM"
)

// AppKeyCapability is the app key capability document.
type AppKeyCapability struct {
	Capability
	Introduce         bool     `json:"introduce" doc:"Whether a person key can introduce a new device key and open its first session" example:"false"`
	SignIn            bool     `json:"sign_in" doc:"Whether an enrolled device key can open a login session" example:"false"`
	Enroll            bool     `json:"enroll" doc:"Whether a signed-in account can enroll a person key and a device key" example:"false"`
	AppCodes          bool     `json:"app_codes" doc:"Whether this server issues and redeems one-time app codes" example:"false"`
	Grants            bool     `json:"grants" doc:"Whether another app can enroll its own key with a grant signed by the person key" example:"false"`
	SignInLinks       bool     `json:"sign_in_links" doc:"Whether an administrator can send a single-use sign-in link" example:"false"`
	ProfileSet        bool     `json:"profile_set" doc:"Whether the account's profiles can be read and written as one versioned document" example:"false"`
	SignatureSuites   []string `json:"signature_suites" doc:"Signature suites signed requests may use" example:"[\"ES256\"]"`
	SealedReplySuites []string `json:"sealed_reply_suites" doc:"Suites a sealed reply may use" example:"[\"P256-HKDF-SHA256-AES256GCM\"]"`
}

// AppKeyCapabilityOutput is the getAppKeyCapability response.
type AppKeyCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AppKeyCapability
}

func registerAppKeys(reg *Registry) {
	op := humaOp(http.MethodGet, Prefix+"/auth/keys/capability", "getAppKeyCapability", "app-keys",
		"Describe app key sign-in support.")
	op.Description = "Public, so an app can ask before it signs in. state is available while the auth.app_key_sign_in server setting is on and disabled while it is off. Each feature field turns true once this server supports that operation."
	Register(reg, Operation{Operation: op, Class: ClassPublic, ServiceBacked: true}, reg.getAppKeyCapability)
}

func (reg *Registry) getAppKeyCapability(ctx context.Context, _ *CapabilityInput) (*AppKeyCapabilityOutput, error) {
	state := StateNotConfigured
	if svc := reg.deps.AppKeys; svc != nil {
		enabled, err := svc.AppKeySignInEnabled(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		state = enabledCapabilityState(enabled)
	}
	return &AppKeyCapabilityOutput{
		Body: AppKeyCapability{
			Capability:        Capability{State: state},
			SignatureSuites:   []string{appKeySignatureSuite},
			SealedReplySuites: []string{appKeySealedReplySuite},
		},
	}, nil
}
