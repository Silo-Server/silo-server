package apiv2

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/danielgtaylor/huma/v2"
)

const autoscanAcceptedStatus = "accepted"

// AutoscanProviderEvent is the provider-defined JSON envelope. The existing
// Sonarr/Radarr parser owns event-specific fields and validation.
type AutoscanProviderEvent map[string]any

func (AutoscanProviderEvent) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Description: "Provider-defined autoscan event, validated after token resolution.", Extensions: map[string]any{extExtensionBag: "autoscan-provider-event"}}
}

// AutoscanDeliveryService owns capability validation and durable event admission.
type AutoscanDeliveryService interface {
	DeliverAutoscanWebhook(http.ResponseWriter, *http.Request, string) error
}
type AutoscanDeliveryInput struct {
	Token string `path:"token" doc:"Secret source delivery capability; never log or echo."`
	requestCapture
}

type AutoscanDeliveryOutput struct {
	Body struct {
		Status string `json:"status" enum:"accepted"`
	}
}
type AutoscanDeliveryCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         Capability
}

func registerAutoscanDelivery(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/autoscan/capabilities", "getAutoscanDeliveryCapabilities", "autoscan", "Discover autoscan delivery ingress availability."), Class: ClassPublic, ServiceBacked: true}, func(context.Context, *CapabilityInput) (*AutoscanDeliveryCapabilityOutput, error) {
		state := StateAvailable
		if reg.deps.AutoscanDelivery == nil {
			state = StateNotConfigured
		}
		return &AutoscanDeliveryCapabilityOutput{Body: Capability{State: state}}, nil
	})
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/autoscan/webhooks/{token}", "receiveAutoscanWebhook", "autoscan", "Accept a token-authenticated provider event through the durable autoscan ingest path."), Class: ClassPublic, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable, RateLimitBucket: "autoscan_webhook"}
	op.DefaultStatus = http.StatusAccepted
	op.MaxBodyBytes = 256 * 1024
	op.Errors = []int{400, 404, 408, 413, 415, 500}
	Register(reg, op, func(_ context.Context, in *AutoscanDeliveryInput) (*AutoscanDeliveryOutput, error) {
		if reg.deps.AutoscanDelivery == nil {
			return nil, unavailable("autoscan delivery")
		}
		if err := reg.deps.AutoscanDelivery.DeliverAutoscanWebhook(in.writer, in.request, in.Token); err != nil {
			if failure, ok := errors.AsType[*handlers.AutoscanDeliveryFailure](err); ok {
				return nil, NewProblem(TypeForStatus(failure.Status), failure.Message)
			}
			return nil, NewProblem(TypeInternalError, "Autoscan delivery failed.")
		}
		out := new(AutoscanDeliveryOutput)
		out.Body.Status = autoscanAcceptedStatus
		return out, nil
	})
	// The provider-specific envelope is validated by the existing parser after
	// token resolution. Do not pre-read or re-encode it through the Huma decoder.
	registeredOperation(reg.api.OpenAPI(), op).RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		mediaTypeJSON: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[AutoscanProviderEvent](), true, ""), Example: map[string]any{"eventType": "Test"}},
	}}
}
