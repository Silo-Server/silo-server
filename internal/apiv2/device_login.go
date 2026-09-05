package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// The device-pairing domain: a device without a keyboard opens a pairing
// request, shows a code, and polls; a logged-in browser looks the request up
// and approves or denies it. One state machine, six operations plus the
// capability document. The states and their poll/lookup answers:
//
//	pending   — waiting for a decision (poll: 200, keep polling)
//	approved  — decided but not yet collected (lookup: 200; poll collects it
//	            and answers 200 with the token pair, moving to consumed)
//	consumed  — the device collected its tokens (poll/lookup: 200)
//	denied    — refused by the approver (poll/lookup: 200)
//	expired   — the request outlived its window (poll/lookup: 200 with
//	            status expired; a decision on it: 410 device_login_expired)
//
// A decision on a request that is already consumed, denied, approved by
// another identity, or of the other purpose is 409 conflict; an unknown
// request is 404 not_found.

// TokenPair is the credential response login, setup, signup and device poll
// share. Every response carrying it is Cache-Control: no-store.
type TokenPair struct {
	AccessToken  string  `json:"access_token" doc:"Bearer access token" example:"eyJhbGciOi..."`
	RefreshToken string  `json:"refresh_token" doc:"Refresh token for POST /auth/refresh" example:"eyJhbGciOi..."`
	ExpiresIn    int     `json:"expires_in" doc:"Access token lifetime in seconds" example:"3600"`
	User         Account `json:"user" doc:"The account the tokens authenticate"`
}

func tokenPairFromView(v handlers.TokenPairView) TokenPair {
	return TokenPair{AccessToken: v.AccessToken, RefreshToken: v.RefreshToken, ExpiresIn: v.ExpiresIn, User: accountFromView(v.User)}
}

// DeviceLoginCapability is the device-pairing capability document.
type DeviceLoginCapability struct {
	Capability
	RemotePlaybackHandoff bool  `json:"remote_playback_handoff" doc:"Whether approve-handoff (remote playback pairing) is supported" example:"true"`
	ProtocolVersions      []int `json:"protocol_versions" doc:"Pairing protocol versions this server speaks" example:"[2]"`
}

// DeviceLoginCapabilityOutput is the getDeviceLoginCapability response.
type DeviceLoginCapabilityOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         DeviceLoginCapability
}

// StartDeviceLoginInput opens a pairing request.
type StartDeviceLoginInput struct {
	Body struct {
		DeviceName     string `json:"device_name,omitempty" maxLength:"128" doc:"Human-readable device name shown to the approver" example:"Living room TV"`
		DevicePlatform string `json:"device_platform,omitempty" maxLength:"64" doc:"Platform label shown to the approver" example:"tvos"`
		ClientPurpose  string `json:"client_purpose,omitempty" enum:"device_login,remote_playback" doc:"Pairing purpose; defaults to device_login. remote_playback requires temporary=true and is approved through approve-handoff" example:"device_login"`
		Temporary      bool   `json:"temporary,omitempty" doc:"Request a temporary session bound to the approver's profile; required for remote_playback" example:"false"`
	}
}

// DeviceLoginStart is the opened pairing request as the device sees it.
type DeviceLoginStart struct {
	DeviceCode              string  `json:"device_code" doc:"Secret the device polls with; never shown to a person" example:"d3v1c3c0d3"`
	UserCode                string  `json:"user_code" doc:"Short code a person types into the approving browser" example:"ABCD-1234"`
	MatchCode               string  `json:"match_code" doc:"Confirmation code shown on both screens so the approver can match them" example:"42"`
	VerificationURI         string  `json:"verification_uri" doc:"Page where the approver enters the user code" example:"https://silo.example.test/link"`
	VerificationURIComplete string  `json:"verification_uri_complete" doc:"verification_uri with the user code prefilled" example:"https://silo.example.test/link?code=ABCD-1234"`
	ExpiresAt               Instant `json:"expires_at" doc:"When the request expires" example:"2026-01-02T03:14:05.678Z"`
	ExpiresIn               int     `json:"expires_in" doc:"Seconds until the request expires" example:"600"`
	Interval                int     `json:"interval" doc:"Minimum seconds between polls" example:"5"`
	DeviceName              string  `json:"device_name" doc:"Device name as recorded" example:"Living room TV"`
	DevicePlatform          string  `json:"device_platform" doc:"Platform as recorded" example:"tvos"`
	ClientPurpose           string  `json:"client_purpose" doc:"Effective purpose after defaulting" example:"device_login"`
	Temporary               bool    `json:"temporary" doc:"Whether the resulting session is temporary" example:"false"`
}

// DeviceLoginStartOutput is the startDeviceLogin response.
type DeviceLoginStartOutput struct {
	Body DeviceLoginStart
}

// GetDeviceLoginInput identifies a pairing request to the approver by either
// code; at least one must be given.
type GetDeviceLoginInput struct {
	Token string `query:"token" doc:"Browser code from the verification link" example:"br0ws3rc0d3"`
	Code  string `query:"code" doc:"User code the person typed" example:"ABCD-1234"`
}

// DeviceLogin is the pairing request as the approver sees it.
type DeviceLogin struct {
	Status         string   `json:"status" enum:"pending,approved,denied,consumed,expired" doc:"Current state; see the domain notes" example:"pending"`
	UserCode       string   `json:"user_code" doc:"User code; empty once the request is no longer decidable" example:"ABCD-1234"`
	MatchCode      string   `json:"match_code" doc:"Confirmation code shown on the device" example:"42"`
	DeviceName     string   `json:"device_name" doc:"Device name the request was opened with" example:"Living room TV"`
	DevicePlatform string   `json:"device_platform" doc:"Platform the request was opened with" example:"tvos"`
	IPAddressHint  string   `json:"ip_address_hint" doc:"Partially masked address the request came from" example:"192.168.1.x"`
	ExpiresAt      *Instant `json:"expires_at,omitempty" doc:"When the request expires; absent once expired or unknown" example:"2026-01-02T03:14:05.678Z"`
	ClientPurpose  string   `json:"client_purpose" doc:"Purpose the request was opened with" example:"device_login"`
	Temporary      bool     `json:"temporary" doc:"Whether the resulting session will be temporary" example:"false"`
}

// DeviceLoginOutput is the getDeviceLogin response.
type DeviceLoginOutput struct {
	Body DeviceLogin
}

// PollDeviceLoginInput is the device's poll.
type PollDeviceLoginInput struct {
	Body struct {
		DeviceCode string `json:"device_code" minLength:"1" doc:"The device code from startDeviceLogin" example:"d3v1c3c0d3"`
	}
}

// DeviceLoginPoll is the poll answer. Tokens are present exactly once, on the
// poll that collects an approved request.
type DeviceLoginPoll struct {
	Status           string     `json:"status" enum:"pending,approved,denied,consumed,expired" doc:"State after this poll; approved carries tokens" example:"pending"`
	PollAfter        int        `json:"poll_after" doc:"Seconds to wait before polling again" example:"5"`
	Tokens           *TokenPair `json:"tokens,omitempty" doc:"The issued credentials; present only when status is approved"`
	ProfileID        string     `json:"profile_id" doc:"Profile the temporary session is bound to; empty for a full login" example:""`
	ProfileToken     string     `json:"profile_token" doc:"X-Profile-Token for the bound profile; empty for a full login" example:""`
	Temporary        bool       `json:"temporary" doc:"Whether the issued session is temporary" example:"false"`
	SessionExpiresAt *Instant   `json:"session_expires_at,omitempty" doc:"When a temporary session ends; absent otherwise" example:"2026-01-02T05:04:05.678Z"`
}

// DeviceLoginPollOutput is the pollDeviceLogin response.
type DeviceLoginPollOutput struct {
	Body DeviceLoginPoll
}

// DecideDeviceLoginInput identifies the request being approved or denied by
// either code; at least one must be given.
type DecideDeviceLoginInput struct {
	Body struct {
		Token string `json:"token,omitempty" doc:"Browser code from the verification link" example:"br0ws3rc0d3"`
		Code  string `json:"code,omitempty" doc:"User code the person typed" example:"ABCD-1234"`
	}
}

// DeviceLoginDecision is the state a decision moved the request to.
type DeviceLoginDecision struct {
	Status string `json:"status" enum:"approved,denied" doc:"State after the decision" example:"approved"`
}

// DeviceLoginDecisionOutput is the approve/deny response.
type DeviceLoginDecisionOutput struct {
	Body DeviceLoginDecision
}

const (
	bucketDeviceStart  = "device_start"
	bucketDeviceLookup = "device_lookup"
	bucketDevicePoll   = "device_poll"
)

func registerDeviceLogin(reg *Registry) {
	// The lookups identify the request by a code, not a path parameter, so
	// their 404 is the operation's own; a decision on a finished request is
	// 409 and on an expired one 410 (device_login_expired).
	lookup := humaOp(http.MethodGet, Prefix+"/auth/device", "getDeviceLogin", "device-login",
		"Describe a pairing request to the person approving it.")
	lookup.Errors = []int{http.StatusNotFound}
	Register(reg, Operation{
		Operation: lookup,
		Class:     ClassPublic, ServiceBacked: true, RateLimitBucket: bucketDeviceLookup,
	}, reg.getDeviceLogin)
	approve := humaOp(http.MethodPost, Prefix+"/auth/device/approve", "approveDeviceLogin", "device-login",
		"Approve a pairing request as the caller's account.")
	approve.Errors = []int{http.StatusNotFound, http.StatusConflict, http.StatusGone}
	Register(reg, Operation{Operation: approve, Class: ClassAuthenticated, ServiceBacked: true}, reg.approveDeviceLogin)
	handoff := humaOp(http.MethodPost, Prefix+"/auth/device/approve-handoff", "approveDeviceHandoff", "device-login",
		"Approve a remote-playback pairing request for the caller's verified profile.")
	handoff.Errors = []int{http.StatusConflict, http.StatusGone}
	Register(reg, Operation{Operation: handoff, Class: ClassProfileScoped, ServiceBacked: true}, reg.approveDeviceHandoff)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/auth/device/capability", "getDeviceLoginCapability", "device-login",
			"Describe device pairing support."),
		Class: ClassPublic,
	}, reg.getDeviceLoginCapability)
	deny := humaOp(http.MethodPost, Prefix+"/auth/device/deny", "denyDeviceLogin", "device-login",
		"Deny a pairing request.")
	deny.Errors = []int{http.StatusNotFound, http.StatusConflict, http.StatusGone}
	Register(reg, Operation{Operation: deny, Class: ClassAuthenticated, ServiceBacked: true}, reg.denyDeviceLogin)
	poll := humaOp(http.MethodPost, Prefix+"/auth/device/poll", "pollDeviceLogin", "device-login",
		"Poll a pairing request from the device and collect its tokens once approved.")
	poll.Errors = []int{http.StatusNotFound}
	Register(reg, Operation{
		Operation: poll,
		Class:     ClassPublic, ServiceBacked: true, RateLimitBucket: bucketDevicePoll,
	}, reg.pollDeviceLogin)
	start := humaOp(http.MethodPost, Prefix+"/auth/device/start", "startDeviceLogin", "device-login",
		"Open a pairing request from a device.")
	start.DefaultStatus = http.StatusCreated
	Register(reg, Operation{
		Operation: start,
		Class:     ClassPublic, ServiceBacked: true, RateLimitBucket: bucketDeviceStart,
	}, reg.startDeviceLogin)
}

func (reg *Registry) getDeviceLoginCapability(_ context.Context, _ *struct{}) (*DeviceLoginCapabilityOutput, error) {
	state := StateNotConfigured
	if reg.deps.Devices != nil && reg.deps.Devices.DeviceLoginConfigured() {
		state = StateAvailable
	}
	return &DeviceLoginCapabilityOutput{
		CacheControl: cacheNoCache,
		Body: DeviceLoginCapability{
			Capability:            Capability{Revision: "1", State: state},
			RemotePlaybackHandoff: true,
			ProtocolVersions:      []int{2},
		},
	}, nil
}

func (reg *Registry) startDeviceLogin(ctx context.Context, in *StartDeviceLoginInput) (*DeviceLoginStartOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	r := requestFrom(ctx)
	input := auth.DeviceLoginStartInput{
		DeviceName:     in.Body.DeviceName,
		DevicePlatform: in.Body.DevicePlatform,
		IPAddress:      clientip.FromContext(ctx),
		ClientPurpose:  in.Body.ClientPurpose,
		Temporary:      in.Body.Temporary,
	}
	if r != nil {
		input.UserAgent = r.UserAgent()
		input.BaseURL = handlers.RequestBaseURL(r)
	}
	result, err := reg.deps.Devices.StartDeviceLogin(ctx, input)
	if err != nil {
		return nil, deviceProblem(err)
	}
	return &DeviceLoginStartOutput{Body: DeviceLoginStart{
		DeviceCode:              result.DeviceCode,
		UserCode:                result.UserCode,
		MatchCode:               result.MatchCode,
		VerificationURI:         result.VerificationURI,
		VerificationURIComplete: result.VerificationURIComplete,
		ExpiresAt:               NewInstant(result.ExpiresAt),
		ExpiresIn:               result.ExpiresIn,
		Interval:                result.Interval,
		DeviceName:              result.DeviceName,
		DevicePlatform:          result.DevicePlatform,
		ClientPurpose:           result.ClientPurpose,
		Temporary:               result.Temporary,
	}}, nil
}

func (reg *Registry) getDeviceLogin(ctx context.Context, in *GetDeviceLoginInput) (*DeviceLoginOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	if in.Token == "" && in.Code == "" {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "query.code", Code: codeRequired, Detail: "Either token or code is required."})
	}
	info, err := reg.deps.Devices.LookupDeviceLogin(ctx, auth.DeviceLoginLookupInput{BrowserCode: in.Token, UserCode: in.Code})
	if err != nil {
		return nil, deviceProblem(err)
	}
	out := DeviceLogin{
		Status:         info.Status,
		UserCode:       info.UserCode,
		MatchCode:      info.MatchCode,
		DeviceName:     info.DeviceName,
		DevicePlatform: info.DevicePlatform,
		IPAddressHint:  info.IPAddressHint,
		ClientPurpose:  info.ClientPurpose,
		Temporary:      info.Temporary,
	}
	if !info.ExpiresAt.IsZero() {
		out.ExpiresAt = ptr(NewInstant(info.ExpiresAt))
	}
	return &DeviceLoginOutput{Body: out}, nil
}

func (reg *Registry) pollDeviceLogin(ctx context.Context, in *PollDeviceLoginInput) (*DeviceLoginPollOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	result, err := reg.deps.Devices.PollDeviceLogin(ctx, in.Body.DeviceCode)
	if err != nil {
		return nil, deviceProblem(err)
	}
	out := DeviceLoginPoll{Status: result.Status, PollAfter: result.PollAfter}
	if result.Tokens != nil {
		out.Tokens = ptr(tokenPairFromView(*result.Tokens))
		if result.Temporary {
			out.ProfileID = result.ProfileID
			out.ProfileToken = result.ProfileToken
			out.Temporary = true
			if !result.SessionExpiresAt.IsZero() {
				out.SessionExpiresAt = ptr(NewInstant(result.SessionExpiresAt))
			}
		}
	}
	return &DeviceLoginPollOutput{Body: out}, nil
}

func (reg *Registry) approveDeviceLogin(ctx context.Context, in *DecideDeviceLoginInput) (*DeviceLoginDecisionOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if p := decisionCodes(in); p != nil {
		return nil, p
	}
	d, err := reg.deps.Devices.ApproveDeviceLogin(ctx, auth.DeviceLoginLookupInput{BrowserCode: in.Body.Token, UserCode: in.Body.Code}, claims.UserID)
	if err != nil {
		return nil, deviceProblem(err)
	}
	return &DeviceLoginDecisionOutput{Body: DeviceLoginDecision{Status: d.Status}}, nil
}

func (reg *Registry) approveDeviceHandoff(ctx context.Context, in *DecideDeviceLoginInput) (*DeviceLoginDecisionOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	scope, ok := scopeFrom(ctx)
	if !ok || scope.UserID == 0 || scope.ProfileID == "" {
		return nil, NewProblem(TypePermissionDenied, "An active verified profile is required.")
	}
	if p := decisionCodes(in); p != nil {
		return nil, p
	}
	d, err := reg.deps.Devices.ApproveDeviceHandoff(ctx, auth.DeviceLoginLookupInput{BrowserCode: in.Body.Token, UserCode: in.Body.Code}, scope.UserID, scope.ProfileID)
	if err != nil {
		return nil, deviceProblem(err)
	}
	return &DeviceLoginDecisionOutput{Body: DeviceLoginDecision{Status: d.Status}}, nil
}

func (reg *Registry) denyDeviceLogin(ctx context.Context, in *DecideDeviceLoginInput) (*DeviceLoginDecisionOutput, error) {
	if reg.deps.Devices == nil {
		return nil, unavailable("device login")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if p := decisionCodes(in); p != nil {
		return nil, p
	}
	d, err := reg.deps.Devices.DenyDeviceLogin(ctx, auth.DeviceLoginLookupInput{BrowserCode: in.Body.Token, UserCode: in.Body.Code}, claims.UserID)
	if err != nil {
		return nil, deviceProblem(err)
	}
	return &DeviceLoginDecisionOutput{Body: DeviceLoginDecision{Status: d.Status}}, nil
}

// decisionCodes requires one of the two identifying codes. v1 forwards an
// empty pair to the store and answers 404; v2 names the omission.
func decisionCodes(in *DecideDeviceLoginInput) *Problem {
	if in.Body.Token == "" && in.Body.Code == "" {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody + ".code", Code: codeRequired, Detail: "Either token or code is required."})
	}
	return nil
}

// deviceProblem renders a device-login seam failure. An expired request keeps
// its 410 under the domain's own type (the status default,
// client_upgrade_required, means something else); a rejected purpose is a
// validation problem at the member; everything else follows the v1 status.
func deviceProblem(err error) *Problem {
	var apiErr *handlers.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Field != "":
			return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
		case apiErr.Status == http.StatusGone:
			return NewProblem(TypeDeviceLoginExpired, apiErr.Message+".")
		}
	}
	return serviceProblem(err)
}
