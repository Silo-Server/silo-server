package apiv2

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

const opGetAdminRequestPoster = "getAdminRequestPoster"

// requestPosterFetchKey shares one download among all requests for the same
// TMDB poster, apart from catalog posters, which the fetcher keys by item.
const requestPosterFetchKey = "request-poster"

// adminRequestPosters is the request service's admin poster read
// (*requests.Service).
type adminRequestPosters interface {
	AdminRequestPosterURL(ctx context.Context, viewer mediarequests.Viewer, id string) (string, error)
}

// requestPosters returns the admin poster read when the server can serve
// request posters: the request service reads the poster and the remote fetcher
// downloads it.
func (reg *Registry) requestPosters() (adminRequestPosters, bool) {
	s, ok := reg.deps.AdminRequests.(adminRequestPosters)
	return s, ok && reg.deps.RemotePosters != nil
}

// setRequestPosterURL gives an admin request row its poster_url: the
// getAdminRequestPoster path, when the row has a TMDB poster and the server
// can serve it.
func (reg *Registry) setRequestPosterURL(out *MediaRequest, r *mediarequests.Request) {
	if _, ok := reg.requestPosters(); !ok || !mediarequests.ValidTMDBImagePath(r.PosterPath) {
		return
	}
	out.PosterURL = Prefix + "/admin/requests/" + url.PathEscape(r.ID) + "/poster"
}

func registerAdminRequestPoster(reg *Registry) {
	problem := map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}
	op := Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/admin/requests/{id}/poster", opGetAdminRequestPoster, "admin",
			"Read a media request's poster through the server, from the path admin request reads return in poster_url."),
		Class: ClassActingAdmin, ServiceBacked: true, RateLimitBucket: ratelimit.ItemPosterBucket,
	}
	op.Parameters = []*huma.Param{{Name: "id", In: paramInPath, Required: true, Description: "The request", Schema: &huma.Schema{Type: huma.TypeString}}}
	op.Responses = map[string]*huma.Response{
		"200": {Description: "Poster bytes", Content: map[string]*huma.MediaType{artworkImageMediaType: {Schema: &huma.Schema{Type: huma.TypeString, Format: artworkBinaryFormat}}}},
		"404": {Description: "No such request, the request has no TMDB poster, or TMDB refused or failed", Content: problem},
		"503": {Description: "Poster fetches are busy or not configured; retry after Retry-After", Content: problem},
	}
	RegisterRaw(reg, RawOperation{Operation: op, Protocol: artworkImageProtocol, Reason: "Poster bytes bypass JSON encoding."}, http.HandlerFunc(reg.serveAdminRequestPoster))
}

func (reg *Registry) serveAdminRequestPoster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s, ok := reg.requestPosters()
	if !ok {
		writeProblem(w, r, unavailable("request posters"))
		return
	}
	id := chi.URLParam(r, "id")
	target, err := s.AdminRequestPosterURL(r.Context(), adminRequestViewer(r.Context()), id)
	if err != nil {
		writeProblem(w, r, requestProblem(err))
		return
	}
	if target == "" {
		writeProblem(w, r, NewProblem(TypeNotFound, "Poster not found."))
		return
	}
	serveRemotePoster(w, r, reg.deps.RemotePosters, requestPosterFetchKey, target, slog.String("media_request_id", id))
}
