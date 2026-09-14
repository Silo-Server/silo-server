package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/danielgtaylor/huma/v2"
)

// AdminPlaybackCommandService applies one sequenced administrator command to
// a live playback session and answers the receipt that identity resolves to.
type AdminPlaybackCommandService interface {
	AdminPlaybackCommandsAvailable() bool
	Command(context.Context, handlers.AdminPlaybackCommandInput) (handlers.AdminPlaybackCommandView, error)
}

// AdminPlaybackCommandIdentity is the ordered command identity every
// sequenced command carries. The client allocates both once per intended
// command and preserves the whole body on retry.
type AdminPlaybackCommandIdentity struct {
	CommandID  string `json:"command_id" format:"uuid" minLength:"36" maxLength:"36" doc:"Client-allocated canonical UUID naming this one command; a retry preserves it" example:"3fa85f64-5717-4562-b3fc-2c963f66afa6"`
	Sequence   int64  `json:"sequence" minimum:"1" maximum:"9007199254740991" doc:"Client-allocated positive order within this session, at most 2^53-1 so every client can represent the latest applied sequence exactly; a command below the latest applied sequence is refused as stale" example:"7"`
	Reason     string `json:"reason,omitempty" maxLength:"1024" doc:"Free-form administrator reason shown to the player when supported"`
	DeadlineMS int    `json:"deadline_ms,omitempty" minimum:"0" maximum:"10000" doc:"Delivery acknowledgement deadline in milliseconds; bounded to 10000, default 3000. Ignored by message"`
}

// AdminPlaybackSessionCommandInput addresses pause, resume and stop.
type AdminPlaybackSessionCommandInput struct {
	SessionID string `path:"session_id" minLength:"1" maxLength:"128"`
	Body      AdminPlaybackCommandIdentity
}

// AdminPlaybackSessionMessageInput addresses message; the text is required.
type AdminPlaybackSessionMessageInput struct {
	SessionID string `path:"session_id" minLength:"1" maxLength:"128"`
	Body      struct {
		AdminPlaybackCommandIdentity
		Title   string `json:"title,omitempty" maxLength:"256" doc:"Optional message title"`
		Message string `json:"message" minLength:"1" maxLength:"2048" doc:"Text displayed on the player"`
	}
}

// AdminPlaybackCommandReceipt is the applied-once receipt for a command identity.
type AdminPlaybackCommandReceipt struct {
	CommandID string `json:"command_id" doc:"The command identity this receipt belongs to"`
	Sequence  int64  `json:"sequence" doc:"The applied sequence"`
	Outcome   string `json:"outcome" enum:"applied,replayed" doc:"applied: this request dispatched the command once (202). replayed: the identity was already applied and this is its recorded receipt (200)"`
	Delivery  string `json:"delivery" enum:"dispatched,fallback_scheduled" doc:"dispatched: sent on the session's realtime lane. fallback_scheduled: no lane; the server ends the session after the deadline (stop only)"`
}

// AdminPlaybackCommandOutput carries 202 for a newly applied command and 200
// for a replayed receipt.
type AdminPlaybackCommandOutput struct {
	Status int
	Body   AdminPlaybackCommandReceipt
}

// AdminPlaybackCommandCapabilitiesOutput reports whether sequenced commands
// can be dispatched from this server and which actions exist.
type AdminPlaybackCommandCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminPlaybackCommandCapabilitiesOutputBody
}

type AdminPlaybackCommandCapabilitiesOutputBody struct {
	Capability
	Available bool     `json:"available"`
	Actions   []string `json:"actions"`
	// SequencedCommands marks the ordered command identity contract:
	// command_id + sequence applied once per session.
	SequencedCommands bool `json:"sequenced_commands"`
	// TerminateRevokesAuthority marks the terminate contract: the durable
	// revocation seam is wired, so terminate revokes first and reports
	// client notification separately. Present only when terminate is listed.
	TerminateRevokesAuthority bool `json:"terminate_revokes_authority"`
}

const (
	adminPlaybackActionPause   = "pause"
	adminPlaybackActionResume  = "resume"
	adminPlaybackActionStop    = "stop"
	adminPlaybackActionMessage = "message"

	// LatestSequenceHeader carries the session's latest applied sequence on
	// a stale 409, so a client whose allocation runs behind another
	// administrator's (or its own earlier clock) can allocate above it.
	LatestSequenceHeader = "X-Silo-Latest-Sequence"
)

// latestSequenceResponseHeaders documents LatestSequenceHeader on the 409
// the sequenced commands answer.
func latestSequenceResponseHeaders() map[string]*huma.Header {
	return map[string]*huma.Header{LatestSequenceHeader: {Schema: &huma.Schema{Type: huma.TypeString}, Description: "On a stale refusal, the session's latest applied sequence as a decimal integer; allocate a new command above it. Absent on other conflicts."}}
}

func registerAdminPlaybackCommands(reg *Registry) {
	const root = Prefix + "/admin/sessions"
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp(http.MethodPost, root+path, id, "admin", summary), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true}
	}
	Register(reg, Operation{Operation: humaOp(http.MethodGet, root+"/command-capabilities", "getAdminPlaybackCommandCapabilities", "admin", "Discover whether sequenced administrator playback commands can be dispatched from this server."), Class: ClassActingAdmin, ServiceBacked: true}, func(_ context.Context, _ *CapabilityInput) (*AdminPlaybackCommandCapabilitiesOutput, error) {
		out := new(AdminPlaybackCommandCapabilitiesOutput)
		out.Body.Actions = []string{}
		out.Body.Available = reg.deps.AdminPlaybackCommands != nil && reg.deps.AdminPlaybackCommands.AdminPlaybackCommandsAvailable()
		if out.Body.Available {
			out.Body.Actions = []string{adminPlaybackActionPause, adminPlaybackActionResume, adminPlaybackActionStop, adminPlaybackActionMessage}
			out.Body.SequencedCommands = true
		}
		if reg.deps.AdminPlaybackTerminate != nil && reg.deps.AdminPlaybackTerminate.AdminTerminateAvailable() {
			out.Body.Actions = append(out.Body.Actions, adminPlaybackActionTerminate)
			out.Body.TerminateRevokesAuthority = true
		}
		return out, nil
	})

	apply := func(ctx context.Context, sessionID string, identity AdminPlaybackCommandIdentity, name playback.CommandName, title, message string) (*AdminPlaybackCommandOutput, error) {
		if reg.deps.AdminPlaybackCommands == nil || !reg.deps.AdminPlaybackCommands.AdminPlaybackCommandsAvailable() {
			return nil, unavailable("administrator playback commands")
		}
		view, err := reg.deps.AdminPlaybackCommands.Command(ctx, handlers.AdminPlaybackCommandInput{
			SessionID:  sessionID,
			CommandID:  identity.CommandID,
			Sequence:   identity.Sequence,
			Name:       name,
			ActorID:    claimsFrom(ctx).UserID,
			Reason:     identity.Reason,
			Title:      title,
			Message:    message,
			DeadlineMS: identity.DeadlineMS,
		})
		if err != nil {
			return nil, adminPlaybackCommandProblem(err)
		}
		out := &AdminPlaybackCommandOutput{Status: http.StatusAccepted, Body: AdminPlaybackCommandReceipt{CommandID: view.CommandID, Sequence: view.Sequence, Outcome: view.Outcome, Delivery: view.Delivery}}
		if view.Outcome == handlers.AdminPlaybackCommandReplayed {
			out.Status = http.StatusOK
		}
		return out, nil
	}

	for _, action := range []struct {
		name, id, summary string
		command           playback.CommandName
	}{
		{adminPlaybackActionPause, "pauseAdminPlaybackSession", "Pause a live playback session with an ordered command identity the session applies once.", playback.CommandPause},
		{adminPlaybackActionResume, "resumeAdminPlaybackSession", "Resume a live playback session with an ordered command identity the session applies once.", playback.CommandUnpause},
		{adminPlaybackActionStop, "stopAdminPlaybackSession", "Stop a playback session with an ordered command identity; without a realtime lane the server ends it after the deadline.", playback.CommandStop},
	} {
		command := op("/{session_id}/"+action.name, action.id, action.summary)
		command.DefaultStatus = http.StatusAccepted
		command.Responses = adminPlaybackReplayResponse()
		command.RetrySafety = RetrySafetyDomainIdentity
		command.Errors = []int{http.StatusNotFound, http.StatusConflict}
		Register(reg, command, func(ctx context.Context, in *AdminPlaybackSessionCommandInput) (*AdminPlaybackCommandOutput, error) {
			return apply(ctx, in.SessionID, in.Body, action.command, "", "")
		})
		registeredOperation(reg.api.OpenAPI(), command).Responses[strconv.Itoa(http.StatusConflict)].Headers = latestSequenceResponseHeaders()
	}
	message := op("/{session_id}/"+adminPlaybackActionMessage, "messageAdminPlaybackSession", "Display a message on a live playback session with an ordered command identity the session applies once.")
	message.DefaultStatus = http.StatusAccepted
	message.Responses = adminPlaybackReplayResponse()
	message.RetrySafety = RetrySafetyDomainIdentity
	message.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, message, func(ctx context.Context, in *AdminPlaybackSessionMessageInput) (*AdminPlaybackCommandOutput, error) {
		return apply(ctx, in.SessionID, in.Body.AdminPlaybackCommandIdentity, playback.CommandDisplayMessage, in.Body.Title, in.Body.Message)
	})
	registeredOperation(reg.api.OpenAPI(), message).Responses[strconv.Itoa(http.StatusConflict)].Headers = latestSequenceResponseHeaders()
}

// adminPlaybackReplayResponse documents the 200 a replayed identity answers
// with, next to the 202 Huma derives for a newly applied command.
func adminPlaybackReplayResponse() map[string]*huma.Response {
	return map[string]*huma.Response{"200": {Description: "This command identity was already applied; the recorded receipt is returned and nothing is dispatched.", Content: map[string]*huma.MediaType{mediaTypeJSON: {Schema: &huma.Schema{Ref: "#/components/schemas/AdminPlaybackCommandReceipt"}}}}}
}

// adminPlaybackCommandProblem renders the sequenced command seam's errors.
func adminPlaybackCommandProblem(err error) *Problem {
	switch {
	case errors.Is(err, handlers.ErrAdminPlaybackCommandUnavailable):
		return unavailable("administrator playback commands")
	case errors.Is(err, playback.ErrSessionNotFound):
		return NewProblem(TypeNotFound, "Playback session not found.")
	case errors.Is(err, handlers.ErrAdminPlaybackCommandStale):
		p := NewProblem(TypeConflict, "The command sequence is behind the session's latest applied command; allocate a newer sequence.")
		var stale *handlers.AdminPlaybackCommandStaleError
		if errors.As(err, &stale) {
			p = p.WithHeader(LatestSequenceHeader, strconv.FormatInt(stale.Latest, 10))
		}
		return p
	case errors.Is(err, handlers.ErrAdminPlaybackCommandConflict):
		return NewProblem(TypeIdempotencyConflict, "This command identity was already applied with different content.")
	case errors.Is(err, handlers.ErrAdminPlaybackRealtimeRequired):
		return NewProblem(TypeConflict, "The playback session has no realtime connection for this command.")
	case errors.Is(err, handlers.ErrAdminPlaybackCommandInvalid):
		return validationProblem("body", codeInvalid, "The command identity or content is invalid.")
	}
	return serviceProblem(err)
}

func (c AdminPlaybackCommandCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(len(c.Actions) > 0)
}
