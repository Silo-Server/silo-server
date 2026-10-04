package apiv2

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

const (
	opGetAdminRequestCounts  = "getAdminRequestCounts"
	opListAdminRequestEvents = "listAdminRequestEvents"
)

// adminRequestQueue is the queue slice of the request service: view counts
// and request history.
type adminRequestQueue interface {
	CountAdminViews(context.Context, mediarequests.Viewer) (mediarequests.AdminViewCounts, error)
	ListRequestEvents(context.Context, mediarequests.Viewer, string) ([]mediarequests.RequestEvent, error)
}

// adminRequestCloser lets the admin cancel also close a failed request.
type adminRequestCloser interface {
	AdminCancel(context.Context, mediarequests.Viewer, string, string) (*mediarequests.Request, error)
}

// AdminMediaRequestListInput filters the admin request queue.
type AdminMediaRequestListInput struct {
	Status            string `query:"status" enum:"pending,approved,queued,downloading,completed" doc:"Only requests in this status" example:"pending"`
	Outcome           string `query:"outcome" enum:"active,declined,cancelled,failed" doc:"Only requests with this outcome" example:"active"` //nolint:misspell // the store's spelling
	View              string `query:"view" enum:"needs_approval,in_progress,failed,done" doc:"Only requests in this queue view: needs_approval (pending), in_progress (approved, queued or downloading), failed, or done (completed, or closed by a decline or cancellation)" example:"needs_approval"`
	Q                 string `query:"q" maxLength:"200" doc:"Only requests whose title contains this text, or whose TMDB ID equals it" example:"severance"`
	MediaType         string `query:"media_type" enum:"movie,series" doc:"Only requests for this media type" example:"series"`
	RequestedByUserID string `query:"requested_by_user_id" pattern:"^[1-9][0-9]{0,9}$" doc:"Only requests made by this account; at most 2147483647" example:"7"`
	Limit             int    `query:"limit" minimum:"1" maximum:"50" default:"50" doc:"Page size; default 50, maximum 50" example:"50"`
	Cursor            string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// filterKey binds a cursor to the filters it was issued for.
func (in *AdminMediaRequestListInput) filterKey() string {
	return strings.Join([]string{in.Status, in.Outcome, in.View, in.Q, in.MediaType, in.RequestedByUserID}, "|")
}

// AdminRequestCounts counts the requests in each queue view.
type AdminRequestCounts struct {
	NeedsApproval int `json:"needs_approval" doc:"Pending requests waiting for an admin" example:"3"`
	InProgress    int `json:"in_progress" doc:"Approved requests on their way to the library" example:"5"`
	Failed        int `json:"failed" doc:"Failed requests; Retry sends them again" example:"1"`
	Done          int `json:"done" doc:"Completed requests, and those closed by a decline or cancellation" example:"42"`
}

type AdminRequestCountsOutput struct {
	Body AdminRequestCounts
}

// AdminRequestEvent is one entry of a request's history.
type AdminRequestEvent struct {
	ID            ID      `json:"id" example:"981"`
	Type          string  `json:"type" doc:"What happened: created, approved, retried, submit_deferred, available_in_library, status_<status> or outcome_<outcome>; clients show unknown types as they are" example:"approved"`
	ActorUserID   ID      `json:"actor_user_id,omitempty" doc:"The account that acted; absent for the server itself" example:"1"`
	ActorUsername string  `json:"actor_username,omitempty" doc:"The acting account's username, while the account exists" example:"admin"`
	Message       string  `json:"message,omitempty" doc:"A reason or error that came with the event" example:"auto approved"`
	CreatedAt     Instant `json:"created_at"`
}

type AdminRequestEventCollection struct {
	Collection[AdminRequestEvent]
}

type AdminRequestEventsOutput struct {
	Body AdminRequestEventCollection
}

func registerAdminRequestQueue(reg *Registry) {
	op := func(path, id, summary string) Operation {
		o := Operation{Operation: humaOp(http.MethodGet, Prefix+path, id, "admin", summary), Class: ClassActingAdmin, ServiceBacked: true}
		o.Errors = []int{http.StatusNotFound}
		return o
	}
	Register(reg, op("/admin/requests/counts", opGetAdminRequestCounts, "Count the requests in each admin queue view."), reg.getAdminRequestCounts)
	Register(reg, op("/admin/requests/{id}/events", opListAdminRequestEvents, "List a request's history, newest first (at most 200 entries)."), reg.listAdminRequestEvents)
}

func (reg *Registry) adminRequestQueueService() (adminRequestQueue, *Problem) {
	s, ok := reg.deps.AdminRequests.(adminRequestQueue)
	if !ok {
		return nil, unavailable("request queue")
	}
	return s, nil
}

func (reg *Registry) getAdminRequestCounts(ctx context.Context, _ *struct{}) (*AdminRequestCountsOutput, error) {
	s, p := reg.adminRequestQueueService()
	if p != nil {
		return nil, p
	}
	c, err := s.CountAdminViews(ctx, adminRequestViewer(ctx))
	if err != nil {
		return nil, requestProblem(err)
	}
	return &AdminRequestCountsOutput{Body: AdminRequestCounts{NeedsApproval: c.NeedsApproval, InProgress: c.InProgress, Failed: c.Failed, Done: c.Done}}, nil
}

func (reg *Registry) listAdminRequestEvents(ctx context.Context, in *MediaRequestGetInput) (*AdminRequestEventsOutput, error) {
	s, p := reg.adminRequestQueueService()
	if p != nil {
		return nil, p
	}
	events, err := s.ListRequestEvents(ctx, adminRequestViewer(ctx), string(in.ID))
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]AdminRequestEvent, 0, len(events))
	for _, e := range events {
		item := AdminRequestEvent{ID: IDFromInt(e.ID), Type: e.EventType, ActorUsername: e.ActorUsername, Message: e.Message, CreatedAt: NewInstant(e.CreatedAt)}
		if e.ActorUserID != nil {
			item.ActorUserID = IDFromInt(int64(*e.ActorUserID))
		}
		items = append(items, item)
	}
	return &AdminRequestEventsOutput{Body: AdminRequestEventCollection{Collection: Paginated(items, "")}}, nil
}

// adminListFilter turns the queue's query into the store filter.
func adminListFilter(in *AdminMediaRequestListInput) (mediarequests.ListFilter, *Problem) {
	filter := mediarequests.ListFilter{
		Status: mediarequests.Status(in.Status), Outcome: mediarequests.Outcome(in.Outcome),
		View: mediarequests.AdminView(in.View), Query: in.Q, MediaType: mediarequests.MediaType(in.MediaType),
	}
	if in.RequestedByUserID != "" {
		id, err := strconv.ParseInt(in.RequestedByUserID, 10, 32)
		if err != nil {
			return filter, NewProblem(TypeValidationFailed, "Invalid account ID.")
		}
		filter.RequestedByUserID = int(id)
	}
	return filter, nil
}
