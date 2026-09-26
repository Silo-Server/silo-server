package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type AdminSubtitleInspectionService interface {
	ListSubtitleProviderConfigs(context.Context) ([]subtitles.ProviderConfig, error)
	TestSubtitleProvider(context.Context, string, handlers.SubtitleProviderTestConfig) handlers.SubtitleProviderTestView
}

type AdminSubtitleProvider struct {
	ProviderName   string   `json:"provider_name"`
	Enabled        bool     `json:"enabled"`
	HasAPIKey      bool     `json:"has_api_key"`
	HasCredentials bool     `json:"has_credentials"`
	UpdatedAt      *Instant `json:"updated_at,omitempty"`
}
type AdminSubtitleProvidersOutput struct {
	Body struct {
		Providers []AdminSubtitleProvider `json:"providers"`
	}
}
type AdminSubtitleProviderTestInput struct {
	Provider string `path:"provider" minLength:"1" maxLength:"128"`
	Body     struct {
		Enabled  bool   `json:"enabled,omitempty"`
		APIKey   string `json:"api_key,omitempty" maxLength:"8192"`
		Username string `json:"username,omitempty" maxLength:"1024"`
		Password string `json:"password,omitempty" maxLength:"8192"`
	}
}
type AdminSubtitleProviderTestOutput struct {
	Body SubtitleProviderTestView
}

func registerAdminSubtitleInspection(reg *Registry) {
	op := func(method, path, id string) Operation {
		return Operation{Operation: humaOp(method, Prefix+path, id, "admin", "Inspect subtitle provider configuration without persisting supplied credentials."), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true}
	}
	Register(reg, op(http.MethodGet, "/admin/subtitle-providers", "listAdminSubtitleProviders"), func(ctx context.Context, _ *struct{}) (*AdminSubtitleProvidersOutput, error) {
		if reg.deps.AdminSubtitleInspection == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle provider administration is unavailable.")
		}
		rows, err := reg.deps.AdminSubtitleInspection.ListSubtitleProviderConfigs(ctx)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to load subtitle provider configuration.")
		}
		out := new(AdminSubtitleProvidersOutput)
		out.Body.Providers = make([]AdminSubtitleProvider, 0, len(rows))
		for _, row := range rows {
			item := AdminSubtitleProvider{ProviderName: row.ProviderName, Enabled: row.Enabled, HasAPIKey: row.HasAPIKey, HasCredentials: row.HasCredentials}
			if !row.UpdatedAt.IsZero() {
				item.UpdatedAt = new(NewInstant(row.UpdatedAt))
			}
			out.Body.Providers = append(out.Body.Providers, item)
		}
		return out, nil
	})
	test := op(http.MethodPost, "/admin/subtitle-providers/{provider}/test", "testAdminSubtitleProvider")
	test.RetrySafety = RetrySafetyNonRetryable
	test.Description = "Runs one bounded provider search with draft credentials, preserving omitted stored values. Does not save the draft. Send once: an uncertain result does not authorize an automatic repeat of the upstream request."
	Register(reg, test, func(ctx context.Context, in *AdminSubtitleProviderTestInput) (*AdminSubtitleProviderTestOutput, error) {
		if reg.deps.AdminSubtitleInspection == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle provider administration is unavailable.")
		}
		view := reg.deps.AdminSubtitleInspection.TestSubtitleProvider(ctx, in.Provider, handlers.SubtitleProviderTestConfig{Enabled: in.Body.Enabled, APIKey: in.Body.APIKey, Username: in.Body.Username, Password: in.Body.Password})
		// Legacy provider diagnostics can embed credentials or upstream URLs.
		if !view.Success {
			view.Error = "Provider test failed. Verify the provider settings and availability."
		} else {
			view.Error = ""
		}
		return &AdminSubtitleProviderTestOutput{Body: SubtitleProviderTestView(view)}, nil
	})
}

// SubtitleProviderTestView is the native transport projection, independent of handler views.
type SubtitleProviderTestView struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}
