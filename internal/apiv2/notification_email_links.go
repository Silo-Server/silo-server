package apiv2

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

const (
	notificationEmailLinkMediaType   = "text/html"
	notificationEmailLinkCachePolicy = "no-store"
	notificationEmailLinkToken       = "token"

	notificationEmailVerifyPath      = "verify"
	notificationEmailUnsubscribePath = "unsubscribe"
	notificationEmailLinkProtocol    = "html-callback"
	notificationEmailLinkCacheHeader = "Cache-Control"
	notificationEmailLinkQuery       = "query"
)

type NotificationEmailLinkService interface {
	HandleVerify(http.ResponseWriter, *http.Request)
	HandleUnsubscribe(http.ResponseWriter, *http.Request)
}

func registerNotificationEmailLinks(reg *Registry) {
	for _, route := range []struct {
		method, path, id string
		verify           bool
	}{
		{http.MethodGet, notificationEmailVerifyPath, "verifyNotificationEmailAddress", true},
		{http.MethodGet, notificationEmailUnsubscribePath, "unsubscribeNotificationEmail", false},
		{http.MethodPost, notificationEmailUnsubscribePath, "unsubscribeNotificationEmailOneClick", false},
	} {
		responses := map[string]*huma.Response{}
		for _, status := range []string{"200", "400", "500"} {
			responses[status] = &huma.Response{Description: "Standalone email-link result page", Content: map[string]*huma.MediaType{notificationEmailLinkMediaType: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		}
		if route.verify {
			responses["409"] = &huma.Response{Description: "Address now belongs to another profile or account", Content: map[string]*huma.MediaType{notificationEmailLinkMediaType: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		}
		for _, response := range responses {
			response.Headers = map[string]*huma.Header{
				notificationEmailLinkCacheHeader: {Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{notificationEmailLinkCachePolicy}}},
				"Referrer-Policy":                {Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{"no-referrer"}}},
			}
		}
		op := Operation{Operation: huma.Operation{Method: route.method, Path: Prefix + "/notifications/email/" + route.path, OperationID: route.id, Tags: []string{"notifications"}, Summary: "Process a tokenized notification email link and render its standalone result.", Parameters: []*huma.Param{{Name: notificationEmailLinkToken, In: notificationEmailLinkQuery, Description: "Email capability token. Missing, expired or consumed proof returns an HTML error page.", Schema: &huma.Schema{Type: huma.TypeString}}}, Responses: responses}, Class: ClassPublic, ServiceBacked: true}
		if route.method == http.MethodPost {
			op.RetrySafety = RetrySafetyNonRetryable
		}
		RegisterRaw(reg, RawOperation{Operation: op, Protocol: notificationEmailLinkProtocol, Reason: "Mail clients follow tokenized links without a Silo session; RFC 8058 clients POST form data and consume HTML."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(notificationEmailLinkCacheHeader, notificationEmailLinkCachePolicy)
			w.Header().Set("Referrer-Policy", "no-referrer")
			svc := reg.deps.NotificationEmailLinks
			if svc == nil {
				writeProblem(w, r, unavailable("notification email links"))
				return
			}
			if route.verify {
				svc.HandleVerify(w, r)
			} else {
				svc.HandleUnsubscribe(w, r)
			}
		}))
	}
}
