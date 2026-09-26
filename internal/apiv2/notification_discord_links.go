package apiv2

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

const (
	discordLinkQuery          = "query"
	discordLinkCode           = "code"
	discordLinkState          = "state"
	discordLinkError          = "error"
	discordLinkLocation       = "Location"
	discordLinkTextHtml       = "text/html"
	discordLinkCacheControl   = "Cache-Control"
	discordLinkNoStore        = "no-store"
	discordLinkReferrerPolicy = "Referrer-Policy"
	discordLinkNoReferrer     = "no-referrer"
	discordLinkRedirect       = "redirect"
	discordLinkDiscordLinking = "Discord linking"
)

const beginNotificationDiscordLinkOperation = "beginNotificationDiscordLink"
const completeNotificationDiscordLinkOperation = "completeNotificationDiscordLink"

type NotificationDiscordLinkService interface {
	BeginNotificationDiscordLink(context.Context, int) (string, error)
	HandleNotificationDiscordCallback(http.ResponseWriter, *http.Request)
}
type NotificationDiscordLink struct {
	URL string `json:"url" format:"uri"`
}
type NotificationDiscordLinkOutput struct{ Body NotificationDiscordLink }

func registerNotificationDiscordLinks(reg *Registry) {
	init := notificationOperation(http.MethodPost, "/discord/link/init", beginNotificationDiscordLinkOperation)
	init.ProfileOptional = true
	init.Summary = "Begin one account-bound Discord consent flow."
	init.RetrySafety = RetrySafetyNonRetryable
	Register(reg, init, func(ctx context.Context, _ *struct{}) (*NotificationDiscordLinkOutput, error) {
		svc := reg.deps.NotificationDiscordLinks
		if svc == nil {
			return nil, unavailable(discordLinkDiscordLinking)
		}
		target, err := svc.BeginNotificationDiscordLink(ctx, claimsFrom(ctx).UserID)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &NotificationDiscordLinkOutput{Body: NotificationDiscordLink{URL: target}}, nil
	})
	callback := Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/notifications/discord/link/callback", OperationID: completeNotificationDiscordLinkOperation, Tags: []string{"notifications"}, Summary: "Consume Discord OAuth state and redirect to notification settings.", Parameters: []*huma.Param{
		{Name: discordLinkState, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: discordLinkCode, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: discordLinkError, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}},
	}, Responses: map[string]*huma.Response{"302": {Description: "Consent outcome redirect to notification settings", Headers: map[string]*huma.Header{discordLinkLocation: {Schema: &huma.Schema{Type: huma.TypeString}}}, Content: map[string]*huma.MediaType{discordLinkTextHtml: {Schema: &huma.Schema{Type: huma.TypeString}}}}}}, Class: ClassPublic, ServiceBacked: true}
	RegisterRaw(reg, RawOperation{Operation: callback, Protocol: discordLinkRedirect, Reason: "Discord redirects browsers without Silo login headers; the stored one-time state authenticates the account-link callback."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(discordLinkCacheControl, discordLinkNoStore)
		w.Header().Set(discordLinkReferrerPolicy, discordLinkNoReferrer)
		svc := reg.deps.NotificationDiscordLinks
		if svc == nil {
			writeProblem(w, r, unavailable(discordLinkDiscordLinking))
			return
		}
		svc.HandleNotificationDiscordCallback(w, r)
	}))
}
