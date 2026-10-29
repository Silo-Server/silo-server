package apiv2

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

type AdminCatalogImagesService interface {
	GetAdminItemImages(context.Context, string) (handlers.AdminItemImagesView, error)
	ApplyAdminItemImage(context.Context, string, handlers.AdminItemImageRequest) (handlers.AdminItemImageResult, error)
	UploadAdminItemImage(context.Context, string, string, []byte) (handlers.AdminItemImageResult, error)
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
		OriginalURL string `json:"original_url" minLength:"1" maxLength:"8192" doc:"An original_url from this item's image list, sent back unchanged; not validated as a URI"`
		Type        string `json:"type" minLength:"1" maxLength:"100"`
		ProviderID  string `json:"provider_id,omitempty" maxLength:"512"`
	}
}
type AdminImageUploadForm struct {
	Type  string        `form:"type" enum:"poster,backdrop,logo" required:"true" doc:"The slot the image fills. Seasons take poster or backdrop."`
	Image huma.FormFile `form:"image" contentType:"image/jpeg,image/png,image/webp,application/octet-stream" required:"true" doc:"The image: JPEG, PNG or WebP, at most 10 MiB. Its bytes decide the format, whatever type the part declares, so a part a browser sends as application/octet-stream is accepted when its bytes are a JPEG, PNG or WebP image."`
}
type AdminImageUploadInput struct {
	ID      string `path:"id" minLength:"1" maxLength:"512"`
	RawBody huma.MultipartFormFiles[AdminImageUploadForm]
}
type AdminImageApplied struct {
	ContentID  string `json:"content_id"`
	StoredPath string `json:"stored_path"`
	Thumbhash  string `json:"thumbhash"`
	ImageURL   string `json:"image_url,omitempty" format:"uri-reference"`
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
		// Local sidecar artwork leads in discovery order, which puts the file a
		// refresh would pick first. Provider choices follow, highest rated
		// first; the stable identity breaks their ties without letting expiring
		// display URLs affect pages.
		slices.SortStableFunc(choices, func(a, b choice) int {
			aLocal := a.row.ProviderID == metadata.LocalImageProviderID
			bLocal := b.row.ProviderID == metadata.LocalImageProviderID
			switch {
			case aLocal && bLocal:
				return 0
			case aLocal:
				return -1
			case bLocal:
				return 1
			}
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
	upload := Operation{Operation: humaOp("POST", Prefix+"/admin/items/{id}/images/upload", "uploadAdminItemImage", "admin-catalog", "Store an uploaded image as local artwork, then publish it like an applied image."), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable, MaxBodyBytes: maxPosterBytes + posterFormOverhead}
	Register(reg, upload, func(ctx context.Context, in *AdminImageUploadInput) (*AdminImageApplyOutput, error) {
		if reg.deps.AdminCatalogImages == nil {
			return nil, unavailable("image selection")
		}
		form := in.RawBody.Data()
		if form == nil || !form.Image.IsSet {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "body.image", Code: codeRequired, Detail: "an image file is required"})
		}
		defer func() { _ = form.Image.Close() }()
		tooLarge := NewProblem(TypePayloadTooLarge, "The image exceeds 10 MiB.")
		if form.Image.Size > metadata.MaxUploadedImageBytes {
			return nil, tooLarge
		}
		data, err := io.ReadAll(io.LimitReader(form.Image, metadata.MaxUploadedImageBytes+1))
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Could not read the image.")
		}
		if len(data) > metadata.MaxUploadedImageBytes {
			return nil, tooLarge
		}
		out, err := reg.deps.AdminCatalogImages.UploadAdminItemImage(ctx, in.ID, form.Type, data)
		if err != nil {
			return nil, collectionProblem(err)
		}
		return &AdminImageApplyOutput{Body: AdminImageApplied{ContentID: out.ContentID, StoredPath: out.StoredPath, Thumbhash: out.Thumbhash, ImageURL: out.ImageURL, Revision: out.Revision}}, nil
	})
}

// ItemImageEntry is the native transport projection, independent of handler views.
type ItemImageEntry struct {
	ProviderID  string  `json:"provider_id"`
	URL         string  `json:"url" doc:"Preview of the image: a fetchable URL, a data: URI, or the provider's value as given; not validated as a URI"`
	OriginalURL string  `json:"original_url" doc:"The provider's image URL, file:// and the unescaped path of a local image, or upload:// and the SHA-256 of an uploaded image the item shows; not validated as a URI. Send it unchanged to apply the image"`
	Type        string  `json:"type"`
	Language    string  `json:"language" doc:"Language of text in the image as the provider reports it, usually ISO 639-1 (TMDB uses xx for none); empty when textless or unknown"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Rating      float64 `json:"rating"`
}

// CurrentImages is the native transport projection, independent of handler views.
type CurrentImages struct {
	PosterURL   string `json:"poster_url,omitempty" doc:"Stored artwork reference (storage key, plugin path, or URL), not a fetchable URL; not validated as a URI"`
	BackdropURL string `json:"backdrop_url,omitempty" doc:"Stored artwork reference (storage key, plugin path, or URL), not a fetchable URL; not validated as a URI"`
	LogoURL     string `json:"logo_url,omitempty" doc:"Stored artwork reference (storage key, plugin path, or URL), not a fetchable URL; not validated as a URI"`
}
