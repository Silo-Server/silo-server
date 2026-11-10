package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/config"
)

// AdminAIProviderService manages the AI provider registry.
type AdminAIProviderService interface {
	ListAIProviders(context.Context) ([]handlers.AdminAIProviderView, error)
	CreateAIProvider(context.Context, handlers.AdminAIProviderInput) (handlers.AdminAIProviderView, error)
	UpdateAIProvider(context.Context, string, handlers.AdminAIProviderInput) (handlers.AdminAIProviderView, error)
	DeleteAIProvider(context.Context, string) error
	CheckAIProvider(context.Context, handlers.AdminAIProviderCheckInput) (handlers.AdminAIProviderCheckResult, error)
	AIProviderModels(context.Context, string) (handlers.AdminAIProviderCheckResult, error)
}

type AdminAIProvider struct {
	ID           string   `json:"id"`
	Type         string   `json:"type" enum:"openai,gemini,openrouter,groq,ollama,whisper,custom"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url" format:"uri"`
	HasAPIKey    bool     `json:"has_api_key"`
	Capabilities []string `json:"capabilities" enum:"chat,embeddings,speech" doc:"What the provider serves."`
	UsedBy       []string `json:"used_by" enum:"text,speech,embeddings" doc:"Jobs currently sending requests to the provider. A provider in use cannot be deleted."`
}

type AdminAIProviderType struct {
	Type           string   `json:"type" enum:"openai,gemini,openrouter,groq,ollama,whisper,custom"`
	Label          string   `json:"label"`
	DefaultBaseURL string   `json:"default_base_url" doc:"Prefills a new provider; empty for a custom endpoint, so not validated as a URI."`
	Capabilities   []string `json:"capabilities" enum:"chat,embeddings,speech" doc:"Fixed capabilities of a known type; empty when the administrator declares them."`
	KeyRequired    bool     `json:"key_required"`
}

type AdminAIProviderList struct {
	Providers []AdminAIProvider     `json:"providers"`
	Types     []AdminAIProviderType `json:"types"`
}
type AdminAIProviderListOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         AdminAIProviderList
}

type AdminAIProviderCreateInput struct {
	Body struct {
		Type         string   `json:"type" enum:"openai,gemini,openrouter,groq,ollama,whisper,custom"`
		Name         *string  `json:"name,omitempty" maxLength:"80" doc:"Defaults to the type's label."`
		BaseURL      *string  `json:"base_url,omitempty" format:"uri" maxLength:"2048" doc:"Defaults to the type's default address."`
		APIKey       *string  `json:"api_key,omitempty" maxLength:"4096" writeOnly:"true"`
		Capabilities []string `json:"capabilities,omitempty" enum:"chat,embeddings,speech" maxItems:"3" doc:"Required for a custom provider; ignored for known types."`
	}
}
type AdminAIProviderUpdateInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"64"`
	Body struct {
		Name         *string  `json:"name,omitempty" maxLength:"80"`
		BaseURL      *string  `json:"base_url,omitempty" format:"uri" maxLength:"2048" doc:"A different host needs api_key in the same request."`
		APIKey       *string  `json:"api_key,omitempty" maxLength:"4096" writeOnly:"true" doc:"Omit to keep the stored key; an empty string clears it."`
		Capabilities []string `json:"capabilities,omitempty" enum:"chat,embeddings,speech" maxItems:"3" doc:"Custom providers only."`
	}
}
type AdminAIProviderOutput struct{ Body AdminAIProvider }
type AdminAIProviderIDInput struct {
	ID string `path:"id" minLength:"1" maxLength:"64"`
}

type AdminAIModel struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty" enum:"chat,embeddings,speech" doc:"Which job can use the model, from the provider or the model's name. Omitted for a model no job uses."`
}
type AdminAIProviderCheck struct {
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Models  []AdminAIModel `json:"models"`
}
type AdminAIProviderCheckOutput struct{ Body AdminAIProviderCheck }
type AdminAIProviderCheckInput struct {
	Body struct {
		ID      string  `json:"id,omitempty" maxLength:"64" doc:"The stored provider this draft edits. Its key is reused only for the same address."`
		Type    string  `json:"type" enum:"openai,gemini,openrouter,groq,ollama,whisper,custom"`
		BaseURL string  `json:"base_url" format:"uri" minLength:"1" maxLength:"2048"`
		APIKey  *string `json:"api_key,omitempty" maxLength:"4096" writeOnly:"true"`
	}
}

func adminAIProviderOf(view handlers.AdminAIProviderView) AdminAIProvider {
	return AdminAIProvider{
		ID: view.ID, Type: view.Type, Name: view.Name, BaseURL: view.BaseURL, HasAPIKey: view.HasAPIKey,
		Capabilities: nonNilStrings(view.Capabilities), UsedBy: nonNilStrings(view.UsedBy),
	}
}

func adminAIProviderCheckOf(result handlers.AdminAIProviderCheckResult) AdminAIProviderCheck {
	models := make([]AdminAIModel, len(result.Models))
	for i, model := range result.Models {
		models[i] = AdminAIModel{ID: model.ID, Kind: model.Kind}
	}
	return AdminAIProviderCheck{Success: result.Success, Message: result.Message, Models: models}
}

func adminAIProviderTypes() []AdminAIProviderType {
	types := make([]AdminAIProviderType, len(config.AIProviderTypes))
	for i, spec := range config.AIProviderTypes {
		capabilities := make([]string, len(spec.Capabilities))
		for j, capability := range spec.Capabilities {
			capabilities[j] = string(capability)
		}
		types[i] = AdminAIProviderType{
			Type: string(spec.Type), Label: spec.Label, DefaultBaseURL: spec.DefaultBaseURL,
			Capabilities: capabilities, KeyRequired: spec.KeyRequired,
		}
	}
	return types
}

// adminAIProviderProblem maps service errors to problems. Invalid and
// conflict messages are authored by the service and safe to show.
func adminAIProviderProblem(err error, action string) error {
	detail := func(sentinel error) string {
		return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
	}
	switch {
	case errors.Is(err, handlers.ErrAIProviderNotFound):
		return NewProblem(TypeNotFound, "AI provider not found.")
	case errors.Is(err, handlers.ErrAIProviderInvalid):
		return NewProblem(TypeValidationFailed, detail(handlers.ErrAIProviderInvalid))
	case errors.Is(err, handlers.ErrAIProviderConflict):
		return NewProblem(TypeConflict, detail(handlers.ErrAIProviderConflict))
	case errors.Is(err, handlers.ErrAdminSettingsUnavailable):
		return unavailable("AI providers")
	default:
		return NewProblem(TypeInternalError, "The AI provider "+action+" could not be confirmed. Refresh providers before trying again.")
	}
}

func registerAdminAIProviders(reg *Registry) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin-ai", summary), Class: ClassActingAdmin, ServiceBacked: true}
		if isMutatingMethod(method) {
			o.RetrySafety = RetrySafetyNonRetryable
			o.DemoRestricted = true
		}
		return o
	}
	service := func() (AdminAIProviderService, error) {
		if reg.deps.AdminAIProviders == nil {
			return nil, unavailable("AI providers")
		}
		return reg.deps.AdminAIProviders, nil
	}

	Register(reg, op("GET", "/admin/ai/providers", "listAdminAIProviders", "List connected AI providers without their keys, with the provider types that can be connected."), func(ctx context.Context, _ *struct{}) (*AdminAIProviderListOutput, error) {
		svc, err := service()
		if err != nil {
			return nil, err
		}
		views, err := svc.ListAIProviders(ctx)
		if err != nil {
			return nil, adminAIProviderProblem(err, "list")
		}
		providers := make([]AdminAIProvider, len(views))
		for i, view := range views {
			providers[i] = adminAIProviderOf(view)
		}
		return &AdminAIProviderListOutput{CacheControl: chatGPTCacheControl, Body: AdminAIProviderList{Providers: providers, Types: adminAIProviderTypes()}}, nil
	})

	create := op("POST", "/admin/ai/providers", "createAdminAIProvider", "Connect an AI provider. Nothing uses it until a job selects it. No probe, replay identity or automatic retry; reconcile uncertain completion by listing providers.")
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *AdminAIProviderCreateInput) (*AdminAIProviderOutput, error) {
		svc, err := service()
		if err != nil {
			return nil, err
		}
		b := in.Body
		view, err := svc.CreateAIProvider(ctx, handlers.AdminAIProviderInput{Type: b.Type, Name: b.Name, BaseURL: b.BaseURL, APIKey: b.APIKey, Capabilities: b.Capabilities})
		if err != nil {
			return nil, adminAIProviderProblem(err, "creation")
		}
		return &AdminAIProviderOutput{Body: adminAIProviderOf(view)}, nil
	})

	Register(reg, op("PATCH", "/admin/ai/providers/{id}", "updateAdminAIProvider", "Edit a provider. Every job using it switches to the new values at once. Its type is fixed, and a new host needs its key in the same request."), func(ctx context.Context, in *AdminAIProviderUpdateInput) (*AdminAIProviderOutput, error) {
		svc, err := service()
		if err != nil {
			return nil, err
		}
		b := in.Body
		view, err := svc.UpdateAIProvider(ctx, in.ID, handlers.AdminAIProviderInput{Name: b.Name, BaseURL: b.BaseURL, APIKey: b.APIKey, Capabilities: b.Capabilities})
		if err != nil {
			return nil, adminAIProviderProblem(err, "update")
		}
		return &AdminAIProviderOutput{Body: adminAIProviderOf(view)}, nil
	})

	Register(reg, op("DELETE", "/admin/ai/providers/{id}", "deleteAdminAIProvider", "Disconnect a provider and discard its key. A provider a job uses cannot be deleted."), func(ctx context.Context, in *AdminAIProviderIDInput) (*struct{}, error) {
		svc, err := service()
		if err != nil {
			return nil, err
		}
		if err := svc.DeleteAIProvider(ctx, in.ID); err != nil {
			return nil, adminAIProviderProblem(err, "deletion")
		}
		return nil, nil
	})

	check := op("POST", "/admin/ai/providers/check", "checkAdminAIProvider", "Ask a stored or draft provider for its models without saving it. Failed checks return success false; never automatically retry.")
	Register(reg, check, func(ctx context.Context, in *AdminAIProviderCheckInput) (*AdminAIProviderCheckOutput, error) {
		svc, err := service()
		if err != nil {
			return nil, err
		}
		b := in.Body
		result, err := svc.CheckAIProvider(ctx, handlers.AdminAIProviderCheckInput{ID: b.ID, Type: b.Type, BaseURL: b.BaseURL, APIKey: b.APIKey})
		if err != nil {
			return nil, adminAIProviderProblem(err, "check")
		}
		return &AdminAIProviderCheckOutput{Body: adminAIProviderCheckOf(result)}, nil
	})

	Register(reg, op("GET", "/admin/ai/providers/{id}/models", "listAdminAIProviderModels", "List the models a stored provider offers. A provider that does not list models returns an empty list with success true."), func(ctx context.Context, in *AdminAIProviderIDInput) (*AdminAIProviderCheckOutput, error) {
		svc, err := service()
		if err != nil {
			return nil, err
		}
		result, err := svc.AIProviderModels(ctx, in.ID)
		if err != nil {
			return nil, adminAIProviderProblem(err, "model list")
		}
		return &AdminAIProviderCheckOutput{Body: adminAIProviderCheckOf(result)}, nil
	})
}
