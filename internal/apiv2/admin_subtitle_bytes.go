package apiv2

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"path"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

type AdminSubtitleBytesService interface {
	GetAdminSubtitleBytes(context.Context, int) (*subtitles.DownloadedSubtitle, []byte, error)
}

func adminSubtitleAttachmentName(row *subtitles.DownloadedSubtitle) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(row.ReleaseName), `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSuffix(name, path.Ext(name))
	if name == "" || name == "." || name == ".." || name == "/" {
		name = "subtitle-" + strconv.Itoa(row.ID)
	}
	// Stored format values outside the supported set must not become filename paths.
	extension := string(row.Format)
	switch row.Format {
	case subtitles.FormatSRT, subtitles.FormatVTT, subtitles.FormatASS, subtitles.FormatSSA, subtitles.FormatSUB:
	default:
		extension = "bin"
	}
	return name + "." + extension
}

const (
	adminSubtitleBytesTag     = "admin"
	adminSubtitleCacheHeader  = "Cache-Control"
	adminSubtitleLengthHeader = "Content-Length"
)

func registerAdminSubtitleBytes(reg *Registry) {
	content := map[string]*huma.MediaType{}
	for _, media := range []string{"application/x-subrip", "text/vtt", "text/x-ssa", mediaTypeBinary} {
		content[media] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	}
	headers := map[string]*huma.Param{}
	for _, name := range []string{"Content-Disposition", adminSubtitleCacheHeader, "X-Content-Type-Options"} {
		headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
	}
	headers[adminSubtitleLengthHeader] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeInteger, Format: "int64"}}
	responses := map[string]*huma.Response{"200": {Description: "Complete stored subtitle attachment. Range and conditional headers are ignored; no partial or conditional response is supported.", Content: content, Headers: headers}}
	for _, status := range []int{404, 422, 500, 503} {
		responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}}
	}
	raw := RawOperation{Operation: Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/admin/subtitles/{id}/download", OperationID: "downloadAdminStoredSubtitle", Tags: []string{adminSubtitleBytesTag}, Parameters: []*huma.Param{{Name: "id", In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString, Pattern: "^[1-9][0-9]*$"}}}, Responses: responses}, Class: ClassActingAdmin, ServiceBacked: true}, Protocol: "subtitle-bytes", Reason: "Complete stored subtitle attachment retains binary HTTP delivery without a JSON envelope."}
	RegisterRaw(reg, raw, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawID := chi.URLParam(r, "id")
		id, err := strconv.Atoi(rawID)
		if err != nil || id <= 0 || strconv.Itoa(id) != rawID {
			writeProblem(w, r, NewProblem(TypeValidationFailed, "Invalid subtitle ID."))
			return
		}
		if reg.deps.AdminSubtitleBytes == nil {
			writeProblem(w, r, NewProblem(TypeDependencyUnavailable, "Subtitle download is unavailable."))
			return
		}
		row, data, err := reg.deps.AdminSubtitleBytes.GetAdminSubtitleBytes(r.Context(), id)
		if err != nil {
			kind := TypeInternalError
			if errors.Is(err, subtitles.ErrSubtitleNotFound) {
				kind = TypeNotFound
			}
			if errors.Is(err, handlers.ErrAdminSubtitleBytesUnavailable) {
				kind = TypeDependencyUnavailable
			}
			writeProblem(w, r, NewProblem(kind, kind.Title))
			return
		}
		if row == nil {
			writeProblem(w, r, NewProblem(TypeNotFound, "Stored subtitle not found."))
			return
		}
		w.Header().Set(adminSubtitleCacheHeader, "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", subtitles.SubtitleContentType(row.Format))
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": adminSubtitleAttachmentName(row)}))
		w.Header().Set(adminSubtitleLengthHeader, strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
}
