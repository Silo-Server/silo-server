package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/danielgtaylor/huma/v2"
)

type EbookConfigService interface {
	ReaderConfig(context.Context, int, string, string, catalogpkg.AccessFilter) (*handlers.EbookReaderConfig, error)
	SaveReaderConfig(context.Context, handlers.EbookReaderConfig, catalogpkg.AccessFilter, handlers.EbookConfigGuard) (*handlers.EbookReaderConfig, error)
}

// ReaderConfigValues is intentionally client-owned reader configuration. The
// server stores this bounded object without interpreting renderer-specific keys.
type ReaderConfigValues map[string]json.RawMessage

func (ReaderConfigValues) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Extensions: map[string]any{extExtensionBag: "ebook-reader-config"}, Description: "Client-owned reader configuration, bounded by the request body limit."}
}

type EbookConfigInput struct {
	ContentID string `path:"content_id" minLength:"1"`
}
type SaveEbookConfigInput struct {
	EbookConfigInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Config ReaderConfigValues `json:"config"`
	}
}
type EbookConfig struct {
	ContentID string             `json:"content_id"`
	Config    ReaderConfigValues `json:"config"`
	UpdatedAt *Instant           `json:"updated_at,omitempty"`
}
type EbookConfigOutput struct {
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         EbookConfig
}

func registerEbookConfig(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/ebooks/{content_id}/reader-config", "getEbookReaderConfig", "ebooks", "Read reader configuration and its current validator."), Class: ClassProfileScoped, ServiceBacked: true}, reg.getEbookReaderConfig)
	Register(reg, Operation{Operation: humaOp(http.MethodPut, Prefix+"/ebooks/{content_id}/reader-config", "saveEbookReaderConfig", "ebooks", "Replace reader configuration using its current validator."), Class: ClassProfileScoped, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent, MaxBodyBytes: 256 << 10}, reg.saveEbookReaderConfig)
}

func ebookConfigTag(userID int, profileID, contentID string, current *handlers.EbookReaderConfig) EntityTag {
	// The scope binds the virtual empty resource as well as persisted rows. All
	// writers change updated_at or configuration, including frozen v1 upserts.
	value := struct {
		UserID               int
		ProfileID, ContentID string
		Current              *handlers.EbookReaderConfig
	}{userID, profileID, contentID, current}
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return EntityTag{Opaque: hex.EncodeToString(sum[:])}
}
func ebookConfigOutput(userID int, profileID, contentID string, current *handlers.EbookReaderConfig) (*EbookConfigOutput, error) {
	out := &EbookConfigOutput{ETag: ebookConfigTag(userID, profileID, contentID, current).String(), CacheControl: "private, no-cache", Body: EbookConfig{ContentID: contentID, Config: ReaderConfigValues{}}}
	if current != nil {
		if err := json.Unmarshal(current.Config, &out.Body.Config); err != nil {
			return nil, serviceProblem(err)
		}
		out.Body.UpdatedAt = new(NewInstant(current.UpdatedAt))
	}
	return out, nil
}
func (reg *Registry) getEbookReaderConfig(ctx context.Context, in *EbookConfigInput) (*EbookConfigOutput, error) {
	if reg.deps.EbookConfig == nil {
		return nil, unavailable("ebook configuration")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	current, err := reg.deps.EbookConfig.ReaderConfig(ctx, userID, profileID, in.ContentID, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, ebookProblem(err)
	}
	return ebookConfigOutput(userID, profileID, in.ContentID, current)
}
func (reg *Registry) saveEbookReaderConfig(ctx context.Context, in *SaveEbookConfigInput) (*EbookConfigOutput, error) {
	if reg.deps.EbookConfig == nil {
		return nil, unavailable("ebook configuration")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	raw, err := json.Marshal(in.Body.Config)
	if err != nil {
		return nil, serviceProblem(err)
	}
	var condition *Problem
	saved, err := reg.deps.EbookConfig.SaveReaderConfig(ctx, handlers.EbookReaderConfig{UserID: userID, ProfileID: profileID, ContentID: in.ContentID, Config: raw}, handlers.AccessFilterFromContext(ctx, ""), func(current *handlers.EbookReaderConfig) error {
		condition = EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, ebookConfigTag(userID, profileID, in.ContentID, current))
		if condition != nil {
			return condition
		}
		return nil
	})
	if condition != nil {
		return nil, condition
	}
	if err != nil {
		return nil, ebookProblem(err)
	}
	return ebookConfigOutput(userID, profileID, in.ContentID, saved)
}
