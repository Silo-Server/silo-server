package apiv2

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/overlays"
)

// OverlayImageService is the card overlay image library
// (*overlays.ImageLibrary).
type OverlayImageService interface {
	HasStorage() bool
	Upload(ctx context.Context, name, declaredType string, data []byte) (overlays.Image, error)
	List(ctx context.Context) ([]overlays.Image, error)
	Delete(ctx context.Context, ref string) error
	Get(ctx context.Context, ref string) ([]byte, error)
}

// overlayImageFormOverhead is the multipart framing allowance above the image
// cap; the cap itself is enforced on the file part.
const overlayImageFormOverhead = 64 << 10

// overlayImageContentSecurityPolicy hardens served overlay images the way
// branding assets are: the bytes are always a re-encoded WebP, and the policy
// keeps a navigated-to image from doing anything else.
const overlayImageContentSecurityPolicy = "default-src 'none'; sandbox"

const (
	overlayImageRefParam  = "ref"
	overlayImageFileField = "body.file"
	contentTypeOptions    = "X-Content-Type-Options"
	mediaTypeWebP         = "image/webp"
)

type AdminOverlayImageForm struct {
	File huma.FormFile `form:"file" contentType:"image/png,image/jpeg,image/webp" required:"true" doc:"The image: PNG, JPEG or WebP, at most 4 MiB. Silo stores it as WebP no larger than 600x300, keeping transparency."`
	// The name's bounds are the service's: Huma would report a form field it
	// refused at form.name, outside the contract's location grammar.
	Name string `form:"name" required:"false" doc:"Label administrators see when they assign the image to a badge: 1 to 64 characters after trimming. Required."`
}
type AdminOverlayImageUploadInput struct {
	RawBody huma.MultipartFormFiles[AdminOverlayImageForm]
}
type AdminOverlayImageRefInput struct {
	Ref string `path:"ref" pattern:"^[0-9a-f]{64}\\.webp$" doc:"Content reference of the image" example:"9f2c4e7a1b3d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7.webp"`
}

// OverlayImage is one image an administrator uploaded for card overlay
// badges. Card overlay documents refer to it by ref.
type OverlayImage struct {
	Ref    string `json:"ref" doc:"Content reference card overlay documents use; it changes exactly when the stored bytes change" example:"9f2c4e7a1b3d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7.webp"`
	Name   string `json:"name" doc:"Administrator's label for the image" example:"4K Dolby Vision"`
	URL    string `json:"url" doc:"Public, immutable path of the image bytes" example:"/api/v2/overlay-images/9f2c4e7a1b3d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7.webp"`
	Width  int    `json:"width" doc:"Stored width in pixels" example:"300"`
	Height int    `json:"height" doc:"Stored height in pixels" example:"120"`
}
type AdminOverlayImageUploadOutput struct {
	Location string `header:"Location"`
	Body     OverlayImage
}

func overlayImageURL(ref string) string { return Prefix + "/overlay-images/" + ref }

func overlayImageOf(img overlays.Image) OverlayImage {
	return OverlayImage{Ref: img.Ref, Name: img.Name, URL: overlayImageURL(img.Ref), Width: img.Width, Height: img.Height}
}

// overlayImagesFor lists the library for getOverlayConfig. Badges fall back
// to their own rendering without images, so a failed read degrades to none
// rather than failing the configuration every card depends on.
func (reg *Registry) overlayImagesFor(ctx context.Context) (images []OverlayImage, uploadsAvailable bool) {
	images = []OverlayImage{}
	library := reg.deps.OverlayImages
	if library == nil {
		return images, false
	}
	list, err := library.List(ctx)
	if err != nil {
		slog.WarnContext(ctx, "listing overlay images failed", "component", "api", "error", err)
		list = nil
	}
	for _, img := range list {
		images = append(images, overlayImageOf(img))
	}
	return images, library.HasStorage()
}

func overlayImageValidation(location, detail string) *Problem {
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
		WithErrors(ProblemError{Location: location, Code: codeInvalid, Detail: detail})
}

func registerOverlayImages(reg *Registry) {
	upload := humaOp(http.MethodPost, Prefix+"/admin/overlay-images", "uploadAdminOverlayImage", "admin-settings",
		"Add an image to the card overlay image library. The image is re-encoded and addressed by its content: uploading the same image again keeps one entry and renames it. No automatic retry: a delayed retry can restore an image deleted in between.")
	upload.DefaultStatus = http.StatusCreated
	upload.Errors = append(upload.Errors, http.StatusConflict)
	Register(reg, Operation{Operation: upload, Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable, MaxBodyBytes: overlays.MaxImageUploadBytes + overlayImageFormOverhead},
		func(ctx context.Context, in *AdminOverlayImageUploadInput) (*AdminOverlayImageUploadOutput, error) {
			if reg.deps.OverlayImages == nil {
				return nil, unavailable("overlay images")
			}
			form := in.RawBody.Data()
			if form == nil || !form.File.IsSet {
				return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
					WithErrors(ProblemError{Location: overlayImageFileField, Code: codeRequired, Detail: "an image file is required"})
			}
			tooLarge := NewProblem(TypePayloadTooLarge, "The image exceeds the 4 MiB limit.")
			if form.File.Size > overlays.MaxImageUploadBytes {
				return nil, tooLarge
			}
			data, err := io.ReadAll(io.LimitReader(form.File, overlays.MaxImageUploadBytes+1))
			if err != nil {
				return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
			}
			if len(data) > overlays.MaxImageUploadBytes {
				return nil, tooLarge
			}
			img, err := reg.deps.OverlayImages.Upload(ctx, form.Name, form.File.ContentType, data)
			switch {
			case errors.Is(err, overlays.ErrImageStorageUnavailable):
				return nil, NewProblem(TypeCapabilityNotConfigured, "Overlay images need asset storage, which this server has not configured.")
			case errors.Is(err, overlays.ErrInvalidImageName):
				return nil, overlayImageValidation("body.name", "a name of 1 to 64 characters is required")
			case errors.Is(err, overlays.ErrUnsupportedImage):
				return nil, overlayImageValidation(overlayImageFileField, "the file is not a PNG, JPEG or WebP image Silo can read")
			case errors.Is(err, overlays.ErrImageTooLarge):
				return nil, tooLarge
			case errors.Is(err, overlays.ErrImageLibraryFull):
				return nil, NewProblem(TypeConflict, "The overlay image library holds its maximum of "+strconv.Itoa(overlays.MaxImages)+" images; delete one first.")
			case err != nil:
				return nil, serviceProblem(err)
			}
			return &AdminOverlayImageUploadOutput{Location: Prefix + "/admin/overlay-images/" + img.Ref, Body: overlayImageOf(img)}, nil
		})

	remove := humaOp(http.MethodDelete, Prefix+"/admin/overlay-images/{ref}", "deleteAdminOverlayImage", "admin-settings",
		"Remove an image from the card overlay image library and delete its bytes. Badges that name it fall back to their own rendering.")
	remove.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: remove, Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent},
		func(ctx context.Context, in *AdminOverlayImageRefInput) (*struct{}, error) {
			if reg.deps.OverlayImages == nil {
				return nil, unavailable("overlay images")
			}
			switch err := reg.deps.OverlayImages.Delete(ctx, in.Ref); {
			case errors.Is(err, overlays.ErrImageNotFound):
				return nil, NewProblem(TypeNotFound, "No overlay image has that reference.")
			case err != nil:
				return nil, serviceProblem(err)
			}
			return nil, nil
		})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		id := "getOverlayImage"
		if method == http.MethodHead {
			id = "headOverlayImage"
		}
		raw := humaOp(method, Prefix+"/overlay-images/{ref}", id, "settings", "Read a card overlay image. The ref addresses the content, so a response never changes.")
		text := func() *huma.Schema { return &huma.Schema{Type: huma.TypeString} }
		raw.Parameters = []*huma.Param{
			{Name: overlayImageRefParam, In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString, Pattern: `^[0-9a-f]{64}\.webp$`}},
			{Name: ifNoneMatchField, In: paramInHeader, Schema: text()},
		}
		headers := map[string]*huma.Param{directContentLength: {Schema: text()}, etagField: {Schema: text()}, directCacheControl: {Schema: text()}, "Content-Security-Policy": {Schema: text()}, contentTypeOptions: {Schema: text()}}
		content := map[string]*huma.MediaType{}
		if method == http.MethodGet {
			content[mediaTypeWebP] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: artworkBinaryFormat}}
		}
		raw.Responses = map[string]*huma.Response{
			"200": {Description: "Image bytes (no body for HEAD)", Headers: headers, Content: content},
			"304": {Description: "Image unchanged", Headers: headers},
			"404": {Description: "Unknown or deleted image"},
			"500": {Description: "Image could not be read"},
			"503": {Description: "Overlay images are not available on this server"},
		}
		RegisterRaw(reg, RawOperation{Operation: Operation{Operation: raw, Class: ClassPublic, ServiceBacked: true}, Protocol: "overlay-image", Reason: "Image bytes and HEAD/conditional HTTP semantics bypass JSON encoding."}, http.HandlerFunc(reg.serveOverlayImage))
	}
}

func (reg *Registry) serveOverlayImage(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, overlayImageRefParam)
	if !overlays.IsImageRef(ref) {
		writeProblem(w, r, NewProblem(TypeNotFound, "Unknown overlay image."))
		return
	}
	if reg.deps.OverlayImages == nil {
		writeProblem(w, r, unavailable("overlay images"))
		return
	}
	data, err := reg.deps.OverlayImages.Get(r.Context(), ref)
	switch {
	case errors.Is(err, overlays.ErrImageNotFound):
		writeProblem(w, r, NewProblem(TypeNotFound, "Unknown overlay image."))
		return
	case errors.Is(err, overlays.ErrImageStorageUnavailable):
		writeProblem(w, r, unavailable("overlay images"))
		return
	case err != nil:
		writeProblem(w, r, serviceProblem(err))
		return
	}
	tag := EntityTag{Opaque: ref}
	w.Header().Set(directCacheControl, "public, max-age=31536000, immutable")
	w.Header().Set(etagField, tag.String())
	w.Header().Set(contentTypeOptions, "nosniff")
	w.Header().Set("Content-Security-Policy", overlayImageContentSecurityPolicy)
	if matched, p := EvaluateReadPreconditions("", r.Header.Get(ifNoneMatchField), tag); p != nil {
		w.Header().Set(directCacheControl, "no-store")
		writeProblem(w, r, p)
		return
	} else if matched {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", mediaTypeWebP)
	w.Header().Set(directContentLength, strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		// The bytes are a WebP the library re-encoded, sent as image/webp
		// with nosniff and a sandboxing policy, never as markup.
		_, _ = w.Write(data) //nolint:gosec // G705: see above
	}
}
