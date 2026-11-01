package apiv2

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// NotificationEmailAlertLinkService serves the pages behind links in alert
// emails: the item hand-off and the recipient's manage page.
type NotificationEmailAlertLinkService interface {
	HandleOpen(http.ResponseWriter, *http.Request)
	HandleManage(http.ResponseWriter, *http.Request)
}

func registerNotificationEmailAlertLinks(reg *Registry) {
	for _, route := range []struct {
		method, path, id, param, summary string
		statuses                         []string
		open                             bool
	}{
		{http.MethodGet, "open", "openNotificationEmailItem", "id", "Open an item linked from an alert email in the Silo app or the browser.", []string{"200", "400"}, true},
		{http.MethodGet, "manage", "getNotificationEmailManagePage", notificationEmailLinkToken, "Render the email alert choices a manage link belongs to.", []string{"200", "400", "404", "500"}, false},
		{http.MethodPost, "manage", "saveNotificationEmailManagePage", notificationEmailLinkToken, "Save the email alert choices submitted from a manage page.", []string{"200", "400", "404", "500"}, false},
	} {
		responses := map[string]*huma.Response{}
		for _, status := range route.statuses {
			responses[status] = &huma.Response{Description: "Standalone email-link page", Content: map[string]*huma.MediaType{notificationEmailLinkMediaType: {Schema: &huma.Schema{Type: huma.TypeString}}}}
		}
		desc := "Email capability token. An unknown token returns an HTML error page."
		if route.open {
			desc = "Item id to open."
		}
		op := Operation{Operation: huma.Operation{Method: route.method, Path: Prefix + "/notifications/email/" + route.path, OperationID: route.id, Tags: []string{"notifications"}, Summary: route.summary, Parameters: []*huma.Param{{Name: route.param, In: notificationEmailLinkQuery, Description: desc, Schema: &huma.Schema{Type: huma.TypeString}}}, Responses: responses}, Class: ClassPublic, ServiceBacked: true}
		if route.method == http.MethodPost {
			op.RetrySafety = RetrySafetyNonRetryable
		}
		RegisterRaw(reg, RawOperation{Operation: op, Protocol: notificationEmailLinkProtocol, Reason: "Mail clients follow alert-email links without a Silo session; the manage form posts HTML form data."}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(notificationEmailLinkCacheHeader, notificationEmailLinkCachePolicy)
			w.Header().Set("Referrer-Policy", "no-referrer")
			svc := reg.deps.NotificationEmailAlertLinks
			if svc == nil {
				writeProblem(w, r, unavailable("notification email alert links"))
				return
			}
			if route.open {
				svc.HandleOpen(w, r)
			} else {
				svc.HandleManage(w, r)
			}
		}))
	}
}
