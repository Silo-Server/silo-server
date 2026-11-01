package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// NotificationEmailArtworkService serves the signed poster and backdrop links
// in notification emails.
type NotificationEmailArtworkService interface {
	ServeEmailArtwork(ctx context.Context, token string) ([]byte, error)
}

const notificationEmailArtworkPath = "/notifications/email/artwork/"

func registerNotificationEmailArtwork(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+notificationEmailArtworkPath+"{token}", "getNotificationEmailArtwork", "notifications",
		"Read a poster or dimmed backdrop linked from a notification email."), Class: ClassPublic, ServiceBacked: true}
	op.Parameters = []*huma.Param{
		{Name: "token", In: paramInPath, Required: true, Description: "Signed artwork reference minted into the email.", Schema: &huma.Schema{Type: huma.TypeString}},
	}
	op.Responses = map[string]*huma.Response{
		"200": {Description: "JPEG artwork", Content: map[string]*huma.MediaType{"image/jpeg": {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}}},
		"404": {Description: "Unknown or invalid artwork link"},
		"503": {Description: "Email artwork unavailable"},
	}
	RegisterRaw(reg, RawOperation{Operation: op, Protocol: "email-artwork-image", Reason: "Image bytes for mail clients bypass JSON encoding."}, http.HandlerFunc(reg.serveNotificationEmailArtwork))
}

func (reg *Registry) serveNotificationEmailArtwork(w http.ResponseWriter, r *http.Request) {
	if reg.deps.NotificationEmailArtwork == nil {
		writeProblem(w, r, unavailable("email artwork"))
		return
	}
	token := strings.TrimPrefix(r.URL.Path, Prefix+notificationEmailArtworkPath)
	data, err := reg.deps.NotificationEmailArtwork.ServeEmailArtwork(r.Context(), token)
	if errors.Is(err, notifications.ErrEmailArtworkNotFound) {
		writeProblem(w, r, NewProblem(TypeNotFound, "Artwork not found."))
		return
	}
	if err != nil {
		writeProblem(w, r, unavailable("email artwork"))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(data)
}
