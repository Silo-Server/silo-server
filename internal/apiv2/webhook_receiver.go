package apiv2

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

const (
	webhookProblemMedia        = "application/problem+json"
	webhookDeliveryLimit int64 = 10 << 20
	webhookPathParameter       = "path"
	webhookProblemSchema       = "#/components/schemas/Problem"
)

// WebhookReceiverService owns secret validation, provider parsing and delivery logging.
type WebhookReceiverService interface {
	ReceiveWebhook(*http.Request, string, int64) error
}

type WebhookReceiverCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         WebhookReceiverCapabilitiesOutputBody
}

type WebhookReceiverCapabilitiesOutputBody struct {
	Capability
	Available    bool  `json:"available"`
	MaxBodyBytes int64 `json:"max_body_bytes"`
}

func registerWebhookReceiver(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/webhook-sync/capabilities", "getWebhookReceiverCapabilities", "webhook-sync", "Discover external webhook receiver availability and body limit."), Class: ClassPublic, ServiceBacked: true}, func(_ context.Context, _ *CapabilityInput) (*WebhookReceiverCapabilitiesOutput, error) {
		out := new(WebhookReceiverCapabilitiesOutput)
		out.Body.Available = reg.deps.WebhookReceiver != nil
		out.Body.MaxBodyBytes = webhookDeliveryLimit
		return out, nil
	})
	responses := map[string]*huma.Response{"204": {Description: "Delivery processed synchronously, including ignored or unmatched events."}}
	for _, status := range []string{"400", "404", "413", "500"} {
		responses[status] = &huma.Response{Description: "Rejected delivery.", Content: map[string]*huma.MediaType{webhookProblemMedia: {Schema: &huma.Schema{Ref: webhookProblemSchema}}}}
	}
	RegisterRaw(reg, RawOperation{Operation: Operation{Operation: huma.Operation{
		Method: http.MethodPost, Path: Prefix + "/webhook-sync/webhooks/{secret}", OperationID: "receiveExternalWebhook", Tags: []string{"webhook-sync"}, Summary: "Receive a provider webhook using its connection secret.",
		Description: "Accepts provider JSON, form or multipart bytes up to 10485760 bytes. Secret is resolved before body processing. No login/profile or Origin proof substitutes for the receiver secret. Processing is synchronous and non-retryable; no exactly-once guarantee.",
		Parameters:  []*huma.Param{{Name: "secret", In: webhookPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Connection receiver secret; rotation invalidates the previous URL."}}, Responses: responses,
	}, Class: ClassPublic, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, Protocol: "provider-webhook", Reason: "External providers send heterogeneous JSON, form and multipart payloads and expect a bodyless acknowledgement."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if reg.deps.WebhookReceiver == nil {
			writeProblem(w, r, unavailable("webhook receiver"))
			return
		}
		if err := reg.deps.WebhookReceiver.ReceiveWebhook(r, chi.URLParam(r, "secret"), webhookDeliveryLimit); err != nil {
			writeProblem(w, r, serviceProblem(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (c WebhookReceiverCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
