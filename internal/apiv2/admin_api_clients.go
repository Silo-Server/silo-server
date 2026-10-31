package apiv2

import (
	"context"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/apiusage"
)

// AdminAPIClientsService reads per-session API request summaries.
type AdminAPIClientsService interface {
	TopClients(ctx context.Context, now time.Time, hours, limit int) (*apiusage.Clients, error)
	ClientEndpoints(ctx context.Context, now time.Time, sessionID string, hours int) ([]apiusage.Endpoint, error)
}

type AdminAPIClientsInput struct {
	Hours int `query:"hours" minimum:"1" maximum:"720" default:"24" doc:"Window in hours: the last hours hours, read from the start of the hour the window begins in."`
	Limit int `query:"limit" minimum:"1" maximum:"25" default:"10"`
}
type AdminAPIClientEndpointsInput struct {
	SessionID string `path:"session_id" minLength:"1" maxLength:"128"`
	Hours     int    `query:"hours" minimum:"1" maximum:"24" default:"24" doc:"Window in hours, up to the request. It reads one session's activity log rows, so it covers at most a day."`
}

// AdminAPIClient is one sign-in session's API use in the window. Requests
// counts what the session quota counts: media transfer is left out.
type AdminAPIClient struct {
	SessionID      ID      `json:"session_id"`
	UserID         ID      `json:"user_id"`
	Username       string  `json:"username"`
	DeviceName     string  `json:"device_name"`
	DevicePlatform string  `json:"device_platform"`
	UserAgent      string  `json:"user_agent" doc:"The user agent the session sent most in its latest hour."`
	Requests       int64   `json:"requests"`
	Refused        int64   `json:"refused" doc:"Requests answered 429 Too Many Requests by any rate limit."`
	PeakHour       int64   `json:"peak_hour_requests" doc:"Most requests in one clock hour of the window."`
	FirstHour      Instant `json:"first_hour"`
	LastHour       Instant `json:"last_hour"`
	SessionActive  bool    `json:"session_active" doc:"False once the session was revoked, expired, or deleted."`
}
type AdminAPIClients struct {
	Hours         int              `json:"hours"`
	From          Instant          `json:"from"`
	TotalRequests int64            `json:"total_requests" doc:"API requests of every session in the window, for each client's share."`
	Clients       []AdminAPIClient `json:"clients"`
}
type AdminAPIClientsOutput struct{ Body AdminAPIClients }

type AdminAPIClientEndpoint struct {
	Method   string `json:"method"`
	Route    string `json:"route" doc:"The matched route pattern, or the path when no pattern was recorded."`
	Requests int64  `json:"requests"`
	Refused  int64  `json:"refused"`
}
type AdminAPIClientEndpoints struct {
	SessionID ID                       `json:"session_id"`
	Hours     int                      `json:"hours"`
	Endpoints []AdminAPIClientEndpoint `json:"endpoints"`
}
type AdminAPIClientEndpointsOutput struct{ Body AdminAPIClientEndpoints }

func registerAdminAPIClients(reg *Registry) {
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/stats/"+path, id, "admin-observability", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("api-clients", "getAdminDashboardAPIClients", "Read the sign-in sessions that made the most API requests in a window, from hourly summaries of the activity log."), func(ctx context.Context, in *AdminAPIClientsInput) (*AdminAPIClientsOutput, error) {
		if reg.deps.AdminAPIClients == nil {
			return nil, unavailable("API usage")
		}
		s, err := reg.deps.AdminAPIClients.TopClients(ctx, time.Now(), in.Hours, in.Limit)
		if err != nil || s == nil {
			return nil, NewProblem(TypeInternalError, "API usage could not be read")
		}
		body := AdminAPIClients{Hours: s.Hours, From: NewInstant(s.From), TotalRequests: s.TotalRequests, Clients: make([]AdminAPIClient, 0, len(s.Clients))}
		for _, c := range s.Clients {
			body.Clients = append(body.Clients, AdminAPIClient{
				SessionID: ID(c.SessionID), UserID: IDFromInt(int64(c.UserID)), Username: c.Username,
				DeviceName: c.DeviceName, DevicePlatform: c.DevicePlatform, UserAgent: c.UserAgent,
				Requests: c.Requests, Refused: c.Refused, PeakHour: c.PeakHour,
				FirstHour: NewInstant(c.FirstHour), LastHour: NewInstant(c.LastHour), SessionActive: c.SessionActive,
			})
		}
		return &AdminAPIClientsOutput{Body: body}, nil
	})
	Register(reg, op("api-clients/{session_id}/endpoints", "getAdminDashboardAPIClientEndpoints", "Read the routes one sign-in session called most in a window."), func(ctx context.Context, in *AdminAPIClientEndpointsInput) (*AdminAPIClientEndpointsOutput, error) {
		if reg.deps.AdminAPIClients == nil {
			return nil, unavailable("API usage")
		}
		endpoints, err := reg.deps.AdminAPIClients.ClientEndpoints(ctx, time.Now(), in.SessionID, in.Hours)
		if errors.Is(err, apiusage.ErrSessionNotFound) {
			return nil, NewProblem(TypeNotFound, "This session made no API requests in the window.")
		}
		if err != nil {
			return nil, NewProblem(TypeInternalError, "API usage could not be read")
		}
		body := AdminAPIClientEndpoints{SessionID: ID(in.SessionID), Hours: in.Hours, Endpoints: make([]AdminAPIClientEndpoint, 0, len(endpoints))}
		for _, e := range endpoints {
			body.Endpoints = append(body.Endpoints, AdminAPIClientEndpoint(e))
		}
		return &AdminAPIClientEndpointsOutput{Body: body}, nil
	})
}
