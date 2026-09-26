package apiv2

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminCatalogImagesService interface {
	GetAdminItemImages(context.Context, string) (handlers.AdminItemImagesView, error)
	ApplyAdminItemImage(context.Context, string, handlers.AdminItemImageRequest) (handlers.AdminItemImageResult, error)
}
type AdminImagesInput struct {
	ID string `path:"id" minLength:"1" maxLength:"512"`
	LimitParam
	Cursor string `query:"cursor"`
}
type AdminImagesPage struct {
	Items          []ItemImageEntry  `json:"items"`
	Page           PageInfo          `json:"page"`
	Current        CurrentImages     `json:"current"`
	ProviderErrors map[string]string `json:"provider_errors,omitempty" doc:"Failed provider identities with generic failure messages."`
}
type AdminImagesOutput struct{ Body AdminImagesPage }
type AdminImageApplyInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"512"`
	Body struct {
		OriginalURL string `json:"original_url" minLength:"1" maxLength:"8192"`
		Type        string `json:"type" minLength:"1" maxLength:"100"`
		ProviderID  string `json:"provider_id,omitempty" maxLength:"512"`
	}
}
type AdminImageApplied struct {
	ContentID  string `json:"content_id"`
	StoredPath string `json:"stored_path"`
	Thumbhash  string `json:"thumbhash"`
	ImageURL   string `json:"image_url,omitempty"`
	Revision   string `json:"revision,omitempty"`
}
type AdminImageApplyOutput struct{ Body AdminImageApplied }
type adminImagePosition struct {
	Offset int    `json:"offset"`
	Digest string `json:"digest"`
}

func registerAdminCatalogImages(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/items/{id}/images", "listAdminItemImages", "admin-catalog", "Browse bounded image choices; refetches providers and rejects continuation when choices change."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminImagesInput) (*AdminImagesOutput, error) {
		if reg.deps.AdminCatalogImages == nil {
			return nil, unavailable("image selection")
		}
		scope := adminPolicyListScope(ctx, "listAdminItemImages", in.ID+"/"+strconv.Itoa(in.Limit), "image_choice", "offset")
		pos := adminImagePosition{}
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
				return nil, p
			}
			if pos.Offset <= 0 || pos.Digest == "" {
				return nil, NewProblem(TypeInvalidCursor, "Invalid image cursor")
			}
		}
		result, err := reg.deps.AdminCatalogImages.GetAdminItemImages(ctx, in.ID)
		if err != nil {
			return nil, collectionProblem(err)
		}
		// Display URLs may expire between requests; bind only provider choice metadata.
		type choice struct {
			key string
			row handlers.AdminImageEntryView
		}
		choices := make([]choice, 0, len(result.Images))
		for _, row := range result.Images {
			identity := row
			identity.URL = ""
			raw, err := json.Marshal(identity)
			if err != nil {
				return nil, serviceProblem(err)
			}
			choices = append(choices, choice{string(raw), row})
		}
		// Keep the metadata service's rating-first presentation. The stable
		// identity breaks ties without letting expiring display URLs affect pages.
		slices.SortStableFunc(choices, func(a, b choice) int {
			if order := cmp.Compare(b.row.Rating, a.row.Rating); order != 0 {
				return order
			}
			return strings.Compare(a.key, b.key)
		})
		digest := sha256.New()
		for _, c := range choices {
			digest.Write([]byte(c.key))
			digest.Write([]byte{0})
		}
		hash := hex.EncodeToString(digest.Sum(nil))
		if in.Cursor != "" && (pos.Digest != hash || pos.Offset >= len(choices)) {
			return nil, NewProblem(TypeInvalidCursor, "Image choices changed; reload the image list")
		}
		end := min(pos.Offset+in.Limit, len(choices))
		rows := make([]ItemImageEntry, 0, end-pos.Offset)
		for _, c := range choices[pos.Offset:end] {
			rows = append(rows, ItemImageEntry(c.row))
		}
		next := ""
		if end < len(choices) {
			next, err = cursors.Encode(scope, adminImagePosition{Offset: end, Digest: hash})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		failures := make(map[string]string, len(result.ProviderErrors))
		for provider := range result.ProviderErrors {
			failures[provider] = "Image provider failed"
		}
		return &AdminImagesOutput{Body: AdminImagesPage{Items: rows, Page: PageInfo{NextCursor: next, HasMore: next != ""}, Current: CurrentImages(result.Current), ProviderErrors: failures}}, nil
	})
	write := Operation{Operation: humaOp("POST", Prefix+"/admin/items/{id}/images/apply", "applyAdminItemImage", "admin-catalog", "Cache immutable artwork then publish the existing item selection synchronously."), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, write, func(ctx context.Context, in *AdminImageApplyInput) (*AdminImageApplyOutput, error) {
		if reg.deps.AdminCatalogImages == nil {
			return nil, unavailable("image selection")
		}
		b := in.Body
		out, err := reg.deps.AdminCatalogImages.ApplyAdminItemImage(ctx, in.ID, handlers.AdminItemImageRequest{OriginalURL: b.OriginalURL, Type: b.Type, ProviderID: b.ProviderID})
		if err != nil {
			return nil, collectionProblem(err)
		}
		return &AdminImageApplyOutput{Body: AdminImageApplied{ContentID: out.ContentID, StoredPath: out.StoredPath, Thumbhash: out.Thumbhash, ImageURL: out.ImageURL, Revision: out.Revision}}, nil
	})
}

// ItemImageEntry is the native transport projection, independent of handler views.
type ItemImageEntry struct {
	ProviderID  string  `json:"provider_id"`
	URL         string  `json:"url"`
	OriginalURL string  `json:"original_url"`
	Type        string  `json:"type"`
	Language    string  `json:"language"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Rating      float64 `json:"rating"`
}

// CurrentImages is the native transport projection, independent of handler views.
type CurrentImages struct {
	PosterURL   string `json:"poster_url,omitempty"`
	BackdropURL string `json:"backdrop_url,omitempty"`
	LogoURL     string `json:"logo_url,omitempty"`
}
