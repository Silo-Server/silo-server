package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

type WebhookSyncService interface {
	ListWebhookConnections(context.Context, int, *webhooksync.PageKey, int) ([]webhooksync.Connection, bool, error)
	CreateWebhookConnection(context.Context, int, webhooksync.CreateConnectionInput) (*webhooksync.CreateConnectionResult, error)
	UpdateWebhookConnection(context.Context, int, string, webhooksync.UpdateConnectionInput) (*webhooksync.Connection, error)
	DeleteWebhookConnection(context.Context, int, string) error
	RotateWebhookConnection(context.Context, int, string) (*webhooksync.RotateWebhookResult, error)
	GetWebhookMappings(context.Context, int, string) (*webhooksync.ProfileMappingsResponse, error)
	UpdateWebhookMappings(context.Context, int, string, webhooksync.UpdateProfileMappingsInput) ([]webhooksync.ProfileMapping, error)
	ListWebhookEvents(context.Context, int, string, *webhooksync.PageKey, int) ([]webhooksync.WebhookEventLog, bool, error)
}

type WebhookConnection struct {
	ID                        ID       `json:"id"`
	Provider                  string   `json:"provider" enum:"plex,emby,jellyfin"`
	ServerID                  string   `json:"server_id"`
	ServerName                string   `json:"server_name"`
	DefaultProfileID          ID       `json:"default_profile_id"`
	WebhookURL                string   `json:"webhook_url" doc:"Relative URL of the external webhook receiver; resolve against the server origin."`
	AccountDiscoveryAvailable bool     `json:"account_discovery_available"`
	UserCount                 int      `json:"user_count"`
	LastWebhookReceivedAt     *Instant `json:"last_webhook_received_at,omitempty"`
	LastWebhookErrorAt        *Instant `json:"last_webhook_error_at,omitempty"`
	LastWebhookErrorMessage   string   `json:"last_webhook_error_message,omitempty"`
	CreatedAt                 Instant  `json:"created_at"`
	UpdatedAt                 Instant  `json:"updated_at"`
}

func webhookConnectionOf(c webhooksync.Connection) WebhookConnection {
	return WebhookConnection{ID: ID(c.ID), Provider: c.Provider, ServerID: c.ServerID, ServerName: c.ServerName, DefaultProfileID: ID(c.DefaultProfileID), WebhookURL: Prefix + "/webhook-sync/webhooks/" + url.PathEscape(c.WebhookSecret), AccountDiscoveryAvailable: c.AccountDiscoveryAvailable, UserCount: c.UserCount, LastWebhookReceivedAt: instantPtr(c.LastWebhookReceivedAt), LastWebhookErrorAt: instantPtr(c.LastWebhookErrorAt), LastWebhookErrorMessage: c.LastWebhookErrorMessage, CreatedAt: NewInstant(c.CreatedAt), UpdatedAt: NewInstant(c.UpdatedAt)}
}

type WebhookConnectionOutput struct{ Body WebhookConnection }
type WebhookConnectionCollection struct{ Collection[WebhookConnection] }
type WebhookConnectionsOutput struct{ Body WebhookConnectionCollection }
type WebhookConnectionID struct {
	ID string `path:"id" format:"uuid"`
}
type WebhookPageInput struct {
	LimitParam
	Cursor string `query:"cursor"`
}
type WebhookEventsInput struct {
	WebhookConnectionID
	WebhookPageInput
}
type WebhookCreateInput struct {
	Body struct {
		Provider         string `json:"provider" enum:"plex,emby,jellyfin"`
		ServerID         string `json:"server_id,omitempty" maxLength:"1024"`
		ServerName       string `json:"server_name" minLength:"1" maxLength:"1024"`
		BaseURL          string `json:"base_url,omitempty" maxLength:"8192"`
		AccessToken      string `json:"access_token,omitempty" maxLength:"8192"`
		DefaultProfileID ID     `json:"default_profile_id" minLength:"1"`
	}
}
type WebhookCreateOutput struct {
	Body struct {
		Connection WebhookConnection `json:"connection"`
		WebhookURL string            `json:"webhook_url"`
	}
}
type WebhookUpdateInput struct {
	WebhookConnectionID
	Body webhooksync.UpdateConnectionInput
}
type WebhookRotateOutput struct {
	Body webhooksync.RotateWebhookResult
}
type WebhookMapping struct {
	ID               ID      `json:"id"`
	ConnectionID     ID      `json:"connection_id"`
	ExternalUserID   string  `json:"external_user_id"`
	ExternalUserName string  `json:"external_user_name"`
	SiloProfileID    *ID     `json:"silo_profile_id,omitempty"`
	LastSeenAt       Instant `json:"last_seen_at"`
	CreatedAt        Instant `json:"created_at"`
	UpdatedAt        Instant `json:"updated_at"`
}

func webhookMappingsOf(rows []webhooksync.ProfileMapping) []WebhookMapping {
	out := make([]WebhookMapping, 0, len(rows))
	for _, r := range rows {
		var profile *ID
		if r.SiloProfileID != nil {
			profile = new(ID(*r.SiloProfileID))
		}
		out = append(out, WebhookMapping{ID: IDFromInt(int64(r.ID)), ConnectionID: ID(r.ConnectionID), ExternalUserID: r.ExternalUserID, ExternalUserName: r.ExternalUserName, SiloProfileID: profile, LastSeenAt: NewInstant(r.LastSeenAt), CreatedAt: NewInstant(r.CreatedAt), UpdatedAt: NewInstant(r.UpdatedAt)})
	}
	return out
}

type WebhookMappingsOutput struct {
	Body struct {
		Mappings                  []WebhookMapping             `json:"mappings"`
		DiscoveredUsers           []webhooksync.DiscoveredUser `json:"discovered_users"`
		AccountDiscoveryAvailable bool                         `json:"account_discovery_available"`
	}
}
type WebhookMappingUpdate struct {
	ExternalUserID   string  `json:"external_user_id" minLength:"1" maxLength:"1024"`
	ExternalUserName string  `json:"external_user_name" maxLength:"1024"`
	SiloProfileID    *string `json:"silo_profile_id"`
}
type WebhookMappingsInput struct {
	WebhookConnectionID
	Body struct {
		Mappings []WebhookMappingUpdate `json:"mappings" maxItems:"1000"`
	}
}
type WebhookEvent struct {
	ID           ID             `json:"id"`
	ConnectionID ID             `json:"connection_id"`
	ReceivedAt   Instant        `json:"received_at"`
	RequestID    string         `json:"request_id,omitempty"`
	HTTPStatus   int            `json:"http_status"`
	Outcome      string         `json:"outcome" enum:"applied,ignored,unmatched,skipped,rejected,error"`
	Summary      string         `json:"summary"`
	ErrorMessage string         `json:"error_message,omitempty"`
	BodyExcerpt  string         `json:"body_excerpt,omitempty"`
	Attrs        map[string]any `json:"attrs,omitempty"`
}
type WebhookEventCollection struct{ Collection[WebhookEvent] }
type WebhookEventsOutput struct{ Body WebhookEventCollection }
type webhookPosition struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func registerWebhookSync(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/webhook-sync"+path, id, "webhook-sync", summary), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}
		o.Errors = []int{http.StatusNotFound, http.StatusConflict}
		if method != http.MethodGet {
			o.DemoRestricted = true
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	svc := func(ctx context.Context) (WebhookSyncService, int, *Problem) {
		if reg.deps.WebhookSync == nil {
			return nil, 0, unavailable("webhook sync")
		}
		return reg.deps.WebhookSync, claimsFrom(ctx).UserID, nil
	}
	position := func(ctx context.Context, id, cursor string) (CursorScope, *webhooksync.PageKey, *Problem) {
		scope := CursorScope{OperationID: id, Security: strconv.Itoa(claimsFrom(ctx).UserID), Sort: "-created_at", Tiebreaker: "id"}
		if cursor == "" {
			return scope, nil, nil
		}
		var pos webhookPosition
		if p := cursors.Decode(scope, cursor, &pos); p != nil {
			return scope, nil, p
		}
		if pos.ID == "" || pos.At.IsZero() {
			return scope, nil, NewProblem(TypeInvalidCursor, "Invalid cursor position.")
		}
		return scope, &webhooksync.PageKey{At: pos.At, ID: pos.ID}, nil
	}
	Register(reg, op(http.MethodGet, "/connections", "listWebhookConnections", "List the account's webhook connections."), func(ctx context.Context, in *WebhookPageInput) (*WebhookConnectionsOutput, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		scope, key, p := position(ctx, "listWebhookConnections", in.Cursor)
		if p != nil {
			return nil, p
		}
		rows, more, err := s.ListWebhookConnections(ctx, u, key, in.Limit)
		if err != nil {
			return nil, webhookProblem(err)
		}
		out := make([]WebhookConnection, 0, len(rows))
		for _, r := range rows {
			out = append(out, webhookConnectionOf(r))
		}
		next := ""
		if more && len(rows) > 0 {
			r := rows[len(rows)-1]
			next, err = cursors.Encode(scope, webhookPosition{At: r.CreatedAt, ID: r.ID})
			if err != nil {
				return nil, webhookProblem(err)
			}
		}
		return &WebhookConnectionsOutput{Body: WebhookConnectionCollection{Collection: Paginated(out, next)}}, nil
	})
	create := op(http.MethodPost, "/connections", "createWebhookConnection", "Create an external webhook connection.")
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *WebhookCreateInput) (*WebhookCreateOutput, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		b := in.Body
		r, err := s.CreateWebhookConnection(ctx, u, webhooksync.CreateConnectionInput{Provider: b.Provider, ServerID: b.ServerID, ServerName: b.ServerName, BaseURL: b.BaseURL, AccessToken: b.AccessToken, DefaultProfileID: string(b.DefaultProfileID)})
		if err != nil {
			return nil, webhookProblem(err)
		}
		out := new(WebhookCreateOutput)
		out.Body.Connection = webhookConnectionOf(r.Connection)
		out.Body.WebhookURL = out.Body.Connection.WebhookURL
		return out, nil
	})
	Register(reg, op(http.MethodPut, "/connections/{id}", "updateWebhookConnection", "Update a webhook connection."), func(ctx context.Context, in *WebhookUpdateInput) (*WebhookConnectionOutput, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		r, err := s.UpdateWebhookConnection(ctx, u, in.ID, in.Body)
		if err != nil {
			return nil, webhookProblem(err)
		}
		return &WebhookConnectionOutput{Body: webhookConnectionOf(*r)}, nil
	})
	del := op(http.MethodDelete, "/connections/{id}", "deleteWebhookConnection", "Delete a webhook connection.")
	del.DefaultStatus = http.StatusNoContent
	Register(reg, del, func(ctx context.Context, in *WebhookConnectionID) (*struct{}, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		if err := s.DeleteWebhookConnection(ctx, u, in.ID); err != nil {
			return nil, webhookProblem(err)
		}
		return nil, nil
	})
	Register(reg, op(http.MethodPost, "/connections/{id}/webhook/rotate", "rotateWebhookConnection", "Rotate the external receiver URL."), func(ctx context.Context, in *WebhookConnectionID) (*WebhookRotateOutput, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		r, err := s.RotateWebhookConnection(ctx, u, in.ID)
		if err != nil {
			return nil, webhookProblem(err)
		}
		return &WebhookRotateOutput{Body: *r}, nil
	})
	Register(reg, op(http.MethodGet, "/connections/{id}/profile-mappings", "getWebhookMappings", "Read external-user profile mappings."), func(ctx context.Context, in *WebhookConnectionID) (*WebhookMappingsOutput, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		r, err := s.GetWebhookMappings(ctx, u, in.ID)
		if err != nil {
			return nil, webhookProblem(err)
		}
		out := new(WebhookMappingsOutput)
		out.Body.Mappings = webhookMappingsOf(r.Mappings)
		out.Body.DiscoveredUsers = NonNil(r.DiscoveredUsers)
		out.Body.AccountDiscoveryAvailable = r.AccountDiscoveryAvailable
		return out, nil
	})
	Register(reg, op(http.MethodPut, "/connections/{id}/profile-mappings", "updateWebhookMappings", "Replace external-user profile mappings."), func(ctx context.Context, in *WebhookMappingsInput) (*WebhookMappingsOutput, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		mappings := make([]webhooksync.UpdateProfileMapping, 0, len(in.Body.Mappings))
		seen := make(map[string]bool, len(in.Body.Mappings))
		for i, m := range in.Body.Mappings {
			if strings.TrimSpace(m.ExternalUserID) == "" || seen[m.ExternalUserID] {
				return nil, NewProblem(TypeValidationFailed, "External user IDs must be nonblank and unique.").WithErrors(ProblemError{Location: "body.mappings[" + strconv.Itoa(i) + "].external_user_id", Code: codeInvalid, Detail: "Expected a unique nonblank external user ID."})
			}
			seen[m.ExternalUserID] = true
			mappings = append(mappings, webhooksync.UpdateProfileMapping{ExternalUserID: m.ExternalUserID, ExternalUserName: m.ExternalUserName, SiloProfileID: m.SiloProfileID})
		}
		rows, err := s.UpdateWebhookMappings(ctx, u, in.ID, webhooksync.UpdateProfileMappingsInput{Mappings: mappings})
		if err != nil {
			return nil, webhookProblem(err)
		}
		out := new(WebhookMappingsOutput)
		out.Body.Mappings = webhookMappingsOf(rows)
		out.Body.DiscoveredUsers = []webhooksync.DiscoveredUser{}
		return out, nil
	})
	Register(reg, op(http.MethodGet, "/connections/{id}/events", "listWebhookEvents", "List webhook events with stable pagination."), func(ctx context.Context, in *WebhookEventsInput) (*WebhookEventsOutput, error) {
		s, u, p := svc(ctx)
		if p != nil {
			return nil, p
		}
		scope, key, p := position(ctx, "listWebhookEvents/"+in.ID, in.Cursor)
		if p != nil {
			return nil, p
		}
		if key != nil {
			if _, err := strconv.ParseInt(key.ID, 10, 64); err != nil {
				return nil, NewProblem(TypeInvalidCursor, "Invalid event cursor.")
			}
		}
		rows, more, err := s.ListWebhookEvents(ctx, u, in.ID, key, in.Limit)
		if err != nil {
			return nil, webhookProblem(err)
		}
		out := make([]WebhookEvent, 0, len(rows))
		for _, r := range rows {
			out = append(out, WebhookEvent{ID: IDFromInt(r.ID), ConnectionID: ID(r.ConnectionID), ReceivedAt: NewInstant(r.ReceivedAt), RequestID: r.RequestID, HTTPStatus: r.HTTPStatus, Outcome: r.Outcome, Summary: r.Summary, ErrorMessage: r.ErrorMessage, BodyExcerpt: r.BodyExcerpt, Attrs: r.Attrs})
		}
		next := ""
		if more && len(rows) > 0 {
			r := rows[len(rows)-1]
			next, err = cursors.Encode(scope, webhookPosition{At: r.ReceivedAt, ID: strconv.FormatInt(r.ID, 10)})
			if err != nil {
				return nil, webhookProblem(err)
			}
		}
		return &WebhookEventsOutput{Body: WebhookEventCollection{Collection: Paginated(out, next)}}, nil
	})
}

func webhookProblem(err error) *Problem {
	if e, ok := errors.AsType[*handlers.APIError](err); ok && e.Status == http.StatusBadRequest {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: e.Message})
	}
	return serviceProblem(err)
}
