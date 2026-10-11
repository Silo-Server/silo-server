package apiv2

import (
	"context"
	"errors"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/reasoning"
)

const chatGPTCacheControl = "no-store"

type ChatGPTService interface {
	Status(context.Context) (chatgpt.Status, error)
	StartLogin(context.Context, string) (chatgpt.Login, error)
	CompleteLoginWithSettings(context.Context, string) (*chatgpt.SettingsTransition, error)
	Models(context.Context, string) ([]chatgpt.Model, error)
	SelectAccountWithSettings(context.Context, string) (*chatgpt.SettingsTransition, error)
	Disconnect(context.Context, string) (bool, error)
}

// Named transport types keep OpenAPI schema names stable.
type ChatGPTAccountStatus chatgpt.AccountStatus
type ChatGPTLogin chatgpt.Login
type ChatGPTModel chatgpt.Model
type ChatGPTStatus struct {
	ActiveClientID       string                 `json:"active_client_id"`
	Accounts             []ChatGPTAccountStatus `json:"accounts"`
	LoginPending         bool                   `json:"login_pending"`
	LoginResult          string                 `json:"login_result" enum:"idle,pending,connected,failed,expired"`
	AttemptID            string                 `json:"attempt_id"`
	LoginFirstConnection bool                   `json:"login_first_connection,omitzero" doc:"Whether the matching connected login outcome first activated this server's ChatGPT plan connection."`
}

func nativeChatGPTStatus(status chatgpt.Status) ChatGPTStatus {
	accounts := make([]ChatGPTAccountStatus, len(status.Accounts))
	for i, account := range status.Accounts {
		accounts[i] = ChatGPTAccountStatus(account)
	}
	return ChatGPTStatus{ActiveClientID: status.ActiveClientID, Accounts: accounts, LoginPending: status.LoginPending, LoginResult: status.LoginResult, AttemptID: status.AttemptID, LoginFirstConnection: status.LoginFirstConnection}
}

type AdminAICapabilitiesInput struct {
	IfNoneMatch string `header:"If-None-Match"`
	IfMatch     string `header:"If-Match"`
	Model       string `query:"model" maxLength:"256"`
}

type AdminAICapabilities struct {
	Capability
	ReasoningLevels []string `json:"reasoning_levels"`
	ChatGPTSignIn   bool     `json:"chatgpt_sign_in"`
}
type AdminAICapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminAICapabilities
}

func (AdminAICapabilities) capabilityState() string { return StateAvailable }

type ChatGPTSettingsTransitionHeaders struct {
	BeforeETag      string `header:"X-Silo-Settings-Before-ETag"`
	AfterETag       string `header:"X-Silo-Settings-After-ETag"`
	FirstConnection string `header:"X-Silo-ChatGPT-First-Connection" doc:"True only for the activation that first enabled this server's ChatGPT plan connection."`
}

type ChatGPTCompleteOutput struct {
	ChatGPTSettingsTransitionHeaders
	CacheControl string `header:"Cache-Control"`
}

type ChatGPTStatusOutput struct {
	ChatGPTSettingsTransitionHeaders
	CacheControl string `header:"Cache-Control"`
	Body         ChatGPTStatus
}
type ChatGPTLoginInput struct {
	Body struct {
		ClientID string `json:"client_id,omitempty" maxLength:"256"`
	}
}
type ChatGPTLoginOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         ChatGPTLogin
}
type ChatGPTModelsOutput struct {
	Body struct {
		Models []ChatGPTModel `json:"models"`
	}
}
type ChatGPTModelsInput struct {
	ClientID string `query:"client_id" maxLength:"256"`
}
type ChatGPTAccountInput struct {
	ClientID string `path:"client_id" minLength:"1" maxLength:"256"`
}
type ChatGPTCompleteInput struct {
	Body struct {
		CallbackURL string `json:"callback_url" minLength:"1" maxLength:"32768"`
	}
}
type ChatGPTDisconnectOutput struct {
	Body struct {
		RevocationConfirmed bool `json:"revocation_confirmed"`
	}
}

func chatGPTProblem(err error) error {
	switch {
	case errors.Is(err, chatgpt.ErrModelUnavailable):
		return NewProblem(TypeValidationFailed, chatgpt.ErrModelUnavailable.Error())
	case errors.Is(err, chatgpt.ErrAccountChanged):
		return NewProblem(TypeValidationFailed, chatgpt.ErrAccountChanged.Error())
	case errors.Is(err, chatgpt.ErrInvalidLogin):
		return NewProblem(TypeValidationFailed, chatgpt.ErrInvalidLogin.Error())
	case errors.Is(err, chatgpt.ErrInvalidCallbackURL):
		return NewProblem(TypeValidationFailed, chatgpt.ErrInvalidCallbackURL.Error())
	case errors.Is(err, chatgpt.ErrIdentity):
		return NewProblem(TypeValidationFailed, chatgpt.ErrIdentity.Error())
	case errors.Is(err, chatgpt.ErrSignInRequired):
		return NewProblem(TypeCapabilityNotConfigured, "Sign in with ChatGPT in AI Services settings.")
	case errors.Is(err, chatgpt.ErrPlanIneligible):
		return NewProblem(TypeCapabilityNotConfigured, chatgpt.ErrPlanIneligible.Error())
	case errors.Is(err, chatgpt.ErrUsageLimited):
		return NewProblem(TypeCapabilityNotConfigured, "ChatGPT usage is paused; use Test text model to recheck usage")
	default:
		return unavailable("ChatGPT plan connection")
	}
}

func registerChatGPT(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin-ai", summary), Class: ClassActingAdmin, ServiceBacked: true}
		if isMutatingMethod(method) {
			o.RetrySafety = RetrySafetyNonRetryable
			o.DemoRestricted = true
		}
		return o
	}
	capabilities := op("GET", "/admin/ai/capabilities", "getAdminAICapabilities", "Discover AI reasoning controls and ChatGPT plan connections.")
	capabilities.ServiceBacked = false
	Register(reg, capabilities, func(_ context.Context, in *AdminAICapabilitiesInput) (*AdminAICapabilitiesOutput, error) {
		return &AdminAICapabilitiesOutput{Body: AdminAICapabilities{ReasoningLevels: reasoning.ForModel(in.Model), ChatGPTSignIn: reg.deps.ChatGPT != nil}}, nil
	})
	Register(reg, op("GET", "/admin/ai/chatgpt", "getAdminChatGPTConnection", "Read ChatGPT account connection status without credentials."), func(ctx context.Context, _ *struct{}) (*ChatGPTStatusOutput, error) {
		if reg.deps.ChatGPT == nil {
			return nil, unavailable("ChatGPT plan connection")
		}
		v, err := reg.deps.ChatGPT.Status(ctx)
		if err != nil {
			return nil, chatGPTProblem(err)
		}
		return &ChatGPTStatusOutput{CacheControl: chatGPTCacheControl, Body: nativeChatGPTStatus(v)}, nil
	})
	Register(reg, op("POST", "/admin/ai/chatgpt/login", "startAdminChatGPTLogin", "Start a ten-minute browser sign-in attempt for manual callback URL submission. Reconcile uncertain results through connection status."), func(ctx context.Context, in *ChatGPTLoginInput) (*ChatGPTLoginOutput, error) {
		if reg.deps.ChatGPT == nil {
			return nil, unavailable("ChatGPT plan connection")
		}
		v, err := reg.deps.ChatGPT.StartLogin(ctx, in.Body.ClientID)
		if err != nil {
			return nil, chatGPTProblem(err)
		}
		return &ChatGPTLoginOutput{CacheControl: chatGPTCacheControl, Body: ChatGPTLogin(v)}, nil
	})
	Register(reg, op("GET", "/admin/ai/chatgpt/models", "listAdminChatGPTModels", "List models available to a saved ChatGPT account, defaulting to the active account."), func(ctx context.Context, in *ChatGPTModelsInput) (*ChatGPTModelsOutput, error) {
		if reg.deps.ChatGPT == nil {
			return nil, unavailable("ChatGPT plan connection")
		}
		models, err := reg.deps.ChatGPT.Models(ctx, in.ClientID)
		if err != nil {
			return nil, chatGPTProblem(err)
		}
		out := new(ChatGPTModelsOutput)
		out.Body.Models = make([]ChatGPTModel, len(models))
		for i, model := range models {
			out.Body.Models[i] = ChatGPTModel(model)
		}
		return out, nil
	})
	Register(reg, op("POST", "/admin/ai/chatgpt/accounts/{client_id}/select", "selectAdminChatGPTAccount", "Select a connected ChatGPT account for subsequent text AI requests."), func(ctx context.Context, in *ChatGPTAccountInput) (*ChatGPTStatusOutput, error) {
		if reg.deps.ChatGPT == nil {
			return nil, unavailable("ChatGPT plan connection")
		}
		transition, err := reg.deps.ChatGPT.SelectAccountWithSettings(ctx, in.ClientID)
		if err != nil {
			return nil, chatGPTProblem(err)
		}
		v, err := reg.deps.ChatGPT.Status(ctx)
		if err != nil {
			return nil, chatGPTProblem(err)
		}
		headers, err := reg.chatGPTSettingsTransitionHeaders(ctx, transition)
		if err != nil {
			return nil, err
		}
		return &ChatGPTStatusOutput{ChatGPTSettingsTransitionHeaders: headers, CacheControl: chatGPTCacheControl, Body: nativeChatGPTStatus(v)}, nil
	})
	Register(reg, op("DELETE", "/admin/ai/chatgpt/accounts/{client_id}", "disconnectAdminChatGPTAccount", "Revoke and clear an account's tokens while retaining its registration for reconnect. Report whether remote revocation was confirmed."), func(ctx context.Context, in *ChatGPTAccountInput) (*ChatGPTDisconnectOutput, error) {
		if reg.deps.ChatGPT == nil {
			return nil, unavailable("ChatGPT plan connection")
		}
		revoked, err := reg.deps.ChatGPT.Disconnect(ctx, in.ClientID)
		if err != nil {
			return nil, chatGPTProblem(err)
		}
		out := new(ChatGPTDisconnectOutput)
		out.Body.RevocationConfirmed = revoked
		return out, nil
	})
	Register(reg, op("POST", "/admin/ai/chatgpt/login/complete", "completeAdminChatGPTLogin", "Consume a one-time, state-checked OAuth callback from a pasted browser URL. Reconcile uncertain results through connection status before starting another attempt."), func(ctx context.Context, in *ChatGPTCompleteInput) (*ChatGPTCompleteOutput, error) {
		if reg.deps.ChatGPT == nil {
			return nil, unavailable("ChatGPT plan connection")
		}
		transition, err := reg.deps.ChatGPT.CompleteLoginWithSettings(ctx, in.Body.CallbackURL)
		if err != nil {
			return nil, chatGPTProblem(err)
		}
		headers, err := reg.chatGPTSettingsTransitionHeaders(ctx, transition)
		if err != nil {
			return nil, err
		}
		return &ChatGPTCompleteOutput{ChatGPTSettingsTransitionHeaders: headers, CacheControl: chatGPTCacheControl}, nil
	})
}

func (reg *Registry) chatGPTSettingsTransitionHeaders(ctx context.Context, transition *chatgpt.SettingsTransition) (ChatGPTSettingsTransitionHeaders, error) {
	if transition == nil {
		return ChatGPTSettingsTransitionHeaders{}, nil
	}
	if reg.deps.AdminSettingsWrite == nil {
		return ChatGPTSettingsTransitionHeaders{}, unavailable("administrator settings")
	}
	before, err := reg.adminSettingsSnapshotTag(ctx, reg.deps.AdminSettingsWrite.SnapshotAdminSettings(transition.Before))
	if err != nil {
		return ChatGPTSettingsTransitionHeaders{}, err
	}
	after, err := reg.adminSettingsSnapshotTag(ctx, reg.deps.AdminSettingsWrite.SnapshotAdminSettings(transition.After))
	if err != nil {
		return ChatGPTSettingsTransitionHeaders{}, err
	}
	return ChatGPTSettingsTransitionHeaders{BeforeETag: before.String(), AfterETag: after.String(), FirstConnection: strconv.FormatBool(transition.FirstConnection)}, nil
}
