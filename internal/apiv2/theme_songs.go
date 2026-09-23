package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/themesongs"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

const (
	themeOwnerMovie       = "movie"
	themeOwnerSeries      = "series"
	themeOwnerSeason      = "season"
	themeOwnerEpisode     = "episode"
	themeRangeDescription = "Requested byte range"
)

type ThemeSongService interface {
	Discover(context.Context, string, bool, catalogpkg.AccessFilter) (themesongs.Set, error)
	Mint(context.Context, themesongs.Identity, string, string, catalogpkg.AccessFilter, time.Time) (string, time.Time, error)
	OpenGrant(context.Context, string, string, string) (themesongs.File, *os.File, error)
}

type ThemeSong themesongs.Song
type ThemeSongSet struct {
	OwnerID string      `json:"owner_id"`
	Items   []ThemeSong `json:"items"`
}

type ThemeSongsCapability struct {
	Capability
	Delivery             string `json:"delivery" enum:"local_direct_play" doc:"Original audio from the API node with filesystem access"`
	Transcode            bool   `json:"transcode"`
	ClusterRouting       bool   `json:"cluster_routing"`
	GrantLifetimeSeconds int    `json:"grant_lifetime_seconds"`
}

type ThemeSongsCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ThemeSongsCapability
}

type ThemePlaybackInput struct {
	OwnerID string `path:"owner_id"`
	ThemeID string `path:"theme_id" pattern:"^[1-9][0-9]*$"`
}

type ThemePlayback struct {
	URL       string    `json:"url" doc:"Short-lived credential; do not log, persist, or share"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ThemePlaybackOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         ThemePlayback
}

func registerThemeSongs(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/catalog/themes/capability", "getThemeSongsCapability", "catalog", "Local theme audio support and delivery limitations."), Class: ClassProfileScoped},
		func(ctx context.Context, _ *CapabilityInput) (*ThemeSongsCapabilityOutput, error) {
			return &ThemeSongsCapabilityOutput{Body: ThemeSongsCapability{Capability: Capability{State: configuredCapabilityState(reg.deps.ThemeSongs != nil), Allowed: ptr(capabilityLoginAllowed(ctx))}, Delivery: "local_direct_play", GrantLifetimeSeconds: int(themesongs.GrantLifetime.Seconds())}}, nil
		})
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/catalog/items/{owner_id}/themes/{theme_id}/playback", "createThemeSongPlayback", "catalog", "Authorize original theme audio for this account and profile."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, op, func(ctx context.Context, in *ThemePlaybackInput) (*ThemePlaybackOutput, error) {
		if reg.deps.ThemeSongs == nil || reg.deps.CatalogAccess == nil {
			return nil, unavailable("theme songs")
		}
		claims := claimsFrom(ctx)
		if !capabilityLoginAllowed(ctx) {
			return nil, NewProblem(TypeAuthenticationRequired, "A current login session is required.")
		}
		viewer, p := reg.itemViewer(ctx, "", "", "")
		if p != nil {
			return nil, p
		}
		scope, _ := scopeFrom(ctx)
		token, expires, err := reg.deps.ThemeSongs.Mint(ctx, themesongs.Identity{UserID: claims.UserID, ProfileID: viewer.ProfileID, SessionID: claims.SessionID, PolicyRevision: scope.PolicyRevision}, in.OwnerID, in.ThemeID, viewer.Access, claims.ExpiresAt.Time)
		if err != nil {
			return nil, themeSongProblem(err)
		}
		path := Prefix + "/catalog/items/" + url.PathEscape(in.OwnerID) + "/themes/" + url.PathEscape(in.ThemeID) + "/audio?token=" + url.QueryEscape(token)
		return &ThemePlaybackOutput{CacheControl: playbackCacheControl, Body: ThemePlayback{URL: path, ExpiresAt: expires}}, nil
	})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		params := []*huma.Param{}
		for _, name := range []string{"owner_id", "theme_id"} {
			params = append(params, &huma.Param{Name: name, In: paramInPath, Required: true, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		params = append(params, &huma.Param{Name: directAccountToken, In: directParamQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Short-lived theme playback grant"})
		for _, name := range []string{directRangeHeader, directIfRange, ifMatchField, ifNoneMatchField, directIfModified, directIfUnmodified} {
			params = append(params, &huma.Param{Name: name, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}})
		}
		var content map[string]*huma.MediaType
		if method == http.MethodGet {
			content = map[string]*huma.MediaType{}
			for _, mime := range []string{"audio/mpeg", "audio/mp4", "audio/flac", "audio/ogg", "audio/wav", "audio/aac", directMultipart} {
				content[mime] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: directBinaryFormat}}
			}
		}
		headers := map[string]*huma.Param{}
		for _, name := range []string{directContentType, directContentLength, directContentRange, directAcceptRanges, etagField, directLastModified, directCacheControl} {
			headers[name] = &huma.Param{Schema: &huma.Schema{Type: huma.TypeString}}
		}
		responses := map[string]*huma.Response{"200": {Description: "Original audio", Content: content, Headers: headers}, "206": {Description: themeRangeDescription, Content: content, Headers: headers}, "304": {Description: "Audio unchanged"}}
		for _, status := range []int{400, 401, 404, 412, 416, 500, 503} {
			responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}}
		}
		id := "getThemeSongAudio"
		if method == http.MethodHead {
			id = "headThemeSongAudio"
		}
		raw := RawOperation{Operation: Operation{Operation: huma.Operation{Method: method, Path: Prefix + "/catalog/items/{owner_id}/themes/{theme_id}/audio", OperationID: id, Tags: []string{"catalog"}, Parameters: params, Responses: responses}, Class: ClassPublic, ServiceBacked: true}, Protocol: "theme-audio", Reason: "A scoped playback grant and current access checks authorize original audio with range and conditional HTTP semantics."}
		var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if reg.deps.ThemeSongs == nil {
				writeProblem(w, r, unavailable("theme songs"))
				return
			}
			file, f, err := reg.deps.ThemeSongs.OpenGrant(r.Context(), chi.URLParam(r, "owner_id"), chi.URLParam(r, "theme_id"), r.URL.Query().Get(directAccountToken))
			if err != nil {
				writeProblem(w, r, themeSongProblem(err))
				return
			}
			defer func() { _ = f.Close() }()
			themesongs.Serve(&directDownloadWriter{ResponseWriter: w, request: r}, r, file, f)
		})
		if reg.deps.ObserveThemeAudio != nil {
			handler = reg.deps.ObserveThemeAudio(method, handler)
		}
		RegisterRaw(reg, raw, handler)
	}
}

func themeSongProblem(err error) *Problem {
	switch {
	case errors.Is(err, themesongs.ErrNotFound), errors.Is(err, catalogpkg.ErrItemNotFound):
		return NewProblem(TypeNotFound, "Theme not found.")
	case errors.Is(err, themesongs.ErrGrant):
		return NewProblem(TypeAuthenticationRequired, "The theme playback grant is invalid or expired.")
	case errors.Is(err, themesongs.ErrUnavailable):
		return unavailable("local theme audio")
	default:
		return NewProblem(TypeInternalError, "Theme audio could not be resolved.")
	}
}
