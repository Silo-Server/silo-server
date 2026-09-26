package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type ThemeCatalogService interface {
	LoadThemeCatalog(context.Context) (*handlers.ThemeCatalogResult, error)
	RefreshThemeCatalog(context.Context) (*handlers.ThemeCatalogResult, error)
	DownloadThemeFile(context.Context, string) ([]byte, error)
}

// ThemeCatalogDocument and ThemeFileDocument use the portable theme format's
// established property spelling, inside a native document envelope.
type ThemeCatalogDocument struct {
	Version   int                 `json:"version" minimum:"1"`
	UpdatedAt string              `json:"updatedAt,omitempty"`
	Themes    []ThemeCatalogEntry `json:"themes"`
}
type ThemeCatalogEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Author        string   `json:"author"`
	PreviewAccent string   `json:"previewAccent"`
	PreviewBg     string   `json:"previewBg"`
	Tags          []string `json:"tags"`
	DownloadURL   string   `json:"downloadUrl"`
	Version       string   `json:"version"`
}
type ThemeCatalogResponse struct {
	Document ThemeCatalogDocument `json:"document"`
	Stale    bool                 `json:"stale"`
}
type ThemeCatalogOutput struct{ Body ThemeCatalogResponse }
type ThemeFileDocument struct {
	Version     int               `json:"version" minimum:"1"`
	Name        string            `json:"name" minLength:"1"`
	Description string            `json:"description,omitempty"`
	Author      string            `json:"author,omitempty"`
	BaseTheme   string            `json:"baseTheme" minLength:"1"`
	Vars        map[string]string `json:"vars"`
	CustomCSS   string            `json:"customCss"`
	CreatedAt   string            `json:"createdAt,omitempty"`
}
type ThemeDownloadInput struct {
	URL string `query:"url" minLength:"1" maxLength:"4096" required:"true"`
}
type ThemeDownloadResponse struct {
	Document ThemeFileDocument `json:"document"`
}
type ThemeDownloadOutput struct{ Body ThemeDownloadResponse }
type ThemeCatalogCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ThemeCatalogCapabilitiesOutputBody
}

type ThemeCatalogCapabilitiesOutputBody struct {
	Capability
	Available        bool `json:"available"`
	CatalogByteLimit int  `json:"catalog_byte_limit"`
	ThemeByteLimit   int  `json:"theme_byte_limit"`
}

func themeCatalogProblem(err error, download bool) *Problem {
	if e, ok := errors.AsType[*handlers.APIError](err); ok && download {
		if e.Status == http.StatusBadRequest {
			return NewProblem(TypeValidationFailed, e.Message)
		}
		if e.Status == http.StatusForbidden {
			return NewProblem(TypePermissionDenied, e.Message)
		}
	}
	return NewProblem(TypeDependencyUnavailable, "The upstream theme document is unavailable or invalid.")
}
func decodeThemeCatalog(result *handlers.ThemeCatalogResult) (*ThemeCatalogOutput, error) {
	var doc ThemeCatalogDocument
	// The frozen bridge reads at most the cap. Refusing the cap itself prevents
	// accepting a valid JSON prefix of a truncated document without changing v1.
	if result == nil || len(result.Body) >= handlers.ThemeCatalogLimit || json.Unmarshal(result.Body, &doc) != nil || doc.Version < 1 {
		return nil, themeCatalogProblem(nil, false)
	}
	if doc.Themes == nil {
		doc.Themes = []ThemeCatalogEntry{}
	}
	for i := range doc.Themes {
		if doc.Themes[i].ID == "" || strings.TrimSpace(doc.Themes[i].Name) == "" || doc.Themes[i].DownloadURL == "" {
			return nil, themeCatalogProblem(nil, false)
		}
		if doc.Themes[i].Tags == nil {
			doc.Themes[i].Tags = []string{}
		}
	}
	return &ThemeCatalogOutput{Body: ThemeCatalogResponse{Document: doc, Stale: result.Stale}}, nil
}
func registerThemeCatalog(reg *Registry) {
	op := func(method, path, id string, class Class) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/theme/"+path, id, "themes", "Read portable theme documents from the configured approved upstream."), Class: class, ServiceBacked: true}
		if method == http.MethodPost {
			o.RetrySafety = RetrySafetyNaturalIdempotent
		}
		return o
	}
	Register(reg, op(http.MethodGet, "catalog/capabilities", "getThemeCatalogCapabilities", ClassAuthenticated), func(_ context.Context, _ *CapabilityInput) (*ThemeCatalogCapabilitiesOutput, error) {
		out := new(ThemeCatalogCapabilitiesOutput)
		out.Body.Available = reg.deps.ThemeCatalog != nil
		out.Body.CatalogByteLimit = handlers.ThemeCatalogLimit - 1
		out.Body.ThemeByteLimit = handlers.ThemeFileLimit - 1
		return out, nil
	})
	Register(reg, op(http.MethodGet, "catalog", "getThemeCatalog", ClassAuthenticated), func(ctx context.Context, _ *struct{}) (*ThemeCatalogOutput, error) {
		if reg.deps.ThemeCatalog == nil {
			return nil, unavailable("theme catalog")
		}
		result, err := reg.deps.ThemeCatalog.LoadThemeCatalog(ctx)
		if err != nil {
			return nil, themeCatalogProblem(err, false)
		}
		return decodeThemeCatalog(result)
	})
	Register(reg, op(http.MethodPost, "catalog/refresh", "refreshThemeCatalog", ClassActingAdmin), func(ctx context.Context, _ *struct{}) (*ThemeCatalogOutput, error) {
		if reg.deps.ThemeCatalog == nil {
			return nil, unavailable("theme catalog")
		}
		result, err := reg.deps.ThemeCatalog.RefreshThemeCatalog(ctx)
		if err != nil {
			return nil, themeCatalogProblem(err, false)
		}
		return decodeThemeCatalog(result)
	})
	Register(reg, op(http.MethodGet, "download", "downloadThemeFile", ClassAuthenticated), func(ctx context.Context, in *ThemeDownloadInput) (*ThemeDownloadOutput, error) {
		if reg.deps.ThemeCatalog == nil {
			return nil, unavailable("theme download")
		}
		body, err := reg.deps.ThemeCatalog.DownloadThemeFile(ctx, in.URL)
		if err != nil {
			return nil, themeCatalogProblem(err, true)
		}
		var doc ThemeFileDocument
		if len(body) >= handlers.ThemeFileLimit || json.Unmarshal(body, &doc) != nil || doc.Version < 1 || strings.TrimSpace(doc.Name) == "" || doc.BaseTheme == "" {
			return nil, themeCatalogProblem(nil, true)
		}
		if doc.Vars == nil {
			doc.Vars = map[string]string{}
		}
		return &ThemeDownloadOutput{Body: ThemeDownloadResponse{Document: doc}}, nil
	})
}

func (c ThemeCatalogCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
