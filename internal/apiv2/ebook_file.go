package apiv2

import (
	"context"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

type EbookFileService interface {
	ResolveReaderFile(context.Context, string, int, catalogpkg.AccessFilter) (*models.MediaFile, error)
	ServeReaderFile(http.ResponseWriter, *http.Request, *models.MediaFile) error
}

func registerEbookFiles(reg *Registry) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		id := "readEbookFile"
		if method == http.MethodHead {
			id = "headEbookFile"
		}
		op := humaOp(method, Prefix+"/ebooks/{content_id}/files/{file_id}/read", id, "ebooks", "Read an authorized ebook with HTTP byte-range and conditional semantics; HEAD does not start conversion.")
		op.Parameters = []*huma.Param{
			{Name: "content_id", In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}},
			{Name: "file_id", In: "path", Required: true, Schema: &huma.Schema{Type: huma.TypeString, MinLength: new(1)}},
		}
		for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
			op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: "header", Schema: &huma.Schema{Type: huma.TypeString}})
		}
		headers := map[string]*huma.Param{}
		for _, name := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition", "Last-Modified", "ETag", "Cache-Control", handlers.ConversionHeader} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
		}
		media := map[string]*huma.MediaType{}
		if method == http.MethodGet {
			for _, name := range []string{"application/epub+zip", "application/pdf", "application/x-mobipocket-ebook", "application/vnd.amazon.ebook", "application/vnd.amazon.mobi8-ebook", "application/vnd.comicbook+zip", "application/vnd.comicbook-rar", "application/x-fictionbook+xml", "application/x-zip-compressed-fb2", "multipart/byteranges"} {
				media[name] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
			}
		}
		op.Responses = map[string]*huma.Response{
			"200": {Description: "Ebook bytes, or HEAD metadata. Uncached Kindle HEAD may omit length and advertises EPUB optimistically; GET determines conversion or fallback.", Content: media, Headers: headers},
			"206": {Description: "Requested single or multipart byte ranges.", Content: media, Headers: headers},
			"304": {Description: "Representation not modified.", Headers: headers},
			"412": {Description: "File precondition failed.", Headers: headers},
			"416": {Description: "Requested range not satisfiable.", Content: map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: huma.TypeString}}}, Headers: headers},
		}
		RegisterRaw(reg, RawOperation{Operation: Operation{Operation: op, Class: ClassProfileScoped, ServiceBacked: true}, Protocol: "ebook-byte-range", Reason: "Ebook files and conversion results are streamed bytes with range and HEAD semantics."}, http.HandlerFunc(reg.readEbookFile))
	}
}

func (reg *Registry) readEbookFile(w http.ResponseWriter, r *http.Request) {
	if reg.deps.EbookFiles == nil {
		writeProblem(w, r, unavailable("ebook reader"))
		return
	}
	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	fileID, err := intOfID(ID(chi.URLParam(r, "file_id")))
	if err != nil || fileID <= 0 || contentID == "" {
		writeProblem(w, r, NewProblem(TypeValidationFailed, "content_id and file_id are required."))
		return
	}
	file, err := reg.deps.EbookFiles.ResolveReaderFile(r.Context(), contentID, fileID, handlers.AccessFilterFromContext(r.Context(), ""))
	if err != nil {
		writeProblem(w, r, ebookProblem(err))
		return
	}
	if err := reg.deps.EbookFiles.ServeReaderFile(w, r, file); err != nil {
		writeProblem(w, r, ebookProblem(err))
	}
}
