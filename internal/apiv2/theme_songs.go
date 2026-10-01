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
	"github.com/Silo-Server/silo-server/internal/themedelivery"
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
	Authorize(context.Context, themesongs.Identity, string, string, catalogpkg.AccessFilter, []themesongs.Format, time.Time) (themesongs.Authorization, error)
	OpenGrant(context.Context, string, string, string) (themesongs.File, themesongs.Delivery, *os.File, error)
	ServeConverted(http.ResponseWriter, *http.Request, themesongs.File)
	ThemeCapabilities(context.Context) themesongs.Capabilities
}

type ThemeSong themesongs.Song
type ThemeSongSet struct {
	OwnerID string      `json:"owner_id"`
	Items   []ThemeSong `json:"items"`
}

type ThemeSongsCapability struct {
	Capability
	Delivery             string `json:"delivery" enum:"local_direct_play,routed" doc:"routed: themes follow the playback routing policy, so audio may come from a proxy node on another origin; local_direct_play: audio comes from this API origin only"`
	Transcode            bool   `json:"transcode" doc:"A theme the client cannot decode can be converted to AAC in audio-only MP4 when the client accepts it"`
	ClusterRouting       bool   `json:"cluster_routing" doc:"Theme audio can be served by worker nodes"`
	GrantLifetimeSeconds int    `json:"grant_lifetime_seconds"`
}

type ThemeSongsCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ThemeSongsCapability
}

type ThemePlaybackInput struct {
	OwnerID string                `path:"id"`
	ThemeID string                `path:"theme_id" pattern:"^[1-9][0-9]*$"`
	Body    *ThemePlaybackRequest `required:"false" doc:"Absent authorizes the original audio, as for a client that does not describe what it decodes"`
}

// ThemePlaybackRequest describes what the client can decode.
type ThemePlaybackRequest struct {
	AcceptedFormats []ThemeAudioFormat `json:"accepted_formats,omitempty" maxItems:"32" doc:"Container and codec pairs the client decodes. The original is chosen when it matches; otherwise AAC in audio-only MP4 when an mp4 (or m4a) entry accepts aac or any codec. Unknown values are ignored"`
}

// ThemeAudioFormat is one container and audio codec a client decodes.
type ThemeAudioFormat struct {
	Container  string `json:"container" maxLength:"16" doc:"File container, lower case, e.g. mp3, mp4, m4a, flac, ogg, opus, wav, aac" example:"ogg"`
	AudioCodec string `json:"audio_codec,omitempty" maxLength:"16" doc:"Codec, lower case, e.g. mp3, aac, alac, flac, vorbis, opus, pcm. Empty accepts any codec in the container" example:"vorbis"`
}

type ThemePlayback struct {
	URL         string    `json:"url" doc:"Short-lived credential; do not log, persist, or share. Relative to this server, or an absolute URL on a proxy node's origin"`
	ExpiresAt   time.Time `json:"expires_at"`
	Delivery    string    `json:"delivery" enum:"original,converted" doc:"converted is progressive AAC in audio-only MP4 with no length and no byte ranges; fetch a new grant to replay it"`
	ContentType string    `json:"content_type" doc:"Media type of the audio at url" example:"audio/mpeg"`
}

type ThemePlaybackOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         ThemePlayback
}

func registerThemeSongs(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/catalog/themes/capabilities", "getThemeSongsCapability", "catalog", "Local theme audio support and delivery limitations."), Class: ClassProfileScoped},
		func(ctx context.Context, _ *CapabilityInput) (*ThemeSongsCapabilityOutput, error) {
			body := ThemeSongsCapability{Capability: Capability{State: configuredCapabilityState(reg.deps.ThemeSongs != nil), Allowed: ptr(capabilityLoginAllowed(ctx))}, Delivery: "local_direct_play", GrantLifetimeSeconds: int(themesongs.GrantLifetime.Seconds())}
			if reg.deps.ThemeSongs != nil {
				caps := reg.deps.ThemeSongs.ThemeCapabilities(ctx)
				body.Transcode, body.ClusterRouting = caps.Transcode, caps.ClusterRouting
				if caps.ClusterRouting {
					body.Delivery = "routed"
				}
			}
			return &ThemeSongsCapabilityOutput{Body: body}, nil
		})
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/catalog/items/{id}/themes/{theme_id}/playback", "createThemeSongPlayback", "catalog", "Authorize theme audio for this account and profile, routed like video playback and converted to AAC when the client cannot decode the original."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
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
		var accepted []themesongs.Format
		if in.Body != nil {
			for _, format := range in.Body.AcceptedFormats {
				accepted = append(accepted, themesongs.Format{Container: format.Container, AudioCodec: format.AudioCodec})
			}
		}
		authorization, err := reg.deps.ThemeSongs.Authorize(ctx, themesongs.Identity{UserID: claims.UserID, ProfileID: viewer.ProfileID, SessionID: claims.SessionID, PolicyRevision: scope.PolicyRevision}, in.OwnerID, in.ThemeID, viewer.Access, accepted, claims.ExpiresAt.Time)
		if err != nil {
			return nil, themeSongProblem(err)
		}
		location := authorization.URL
		if location == "" {
			location = Prefix + "/catalog/items/" + url.PathEscape(in.OwnerID) + "/themes/" + url.PathEscape(in.ThemeID) + "/audio?token=" + url.QueryEscape(authorization.Grant)
		}
		return &ThemePlaybackOutput{CacheControl: playbackCacheControl, Body: ThemePlayback{URL: location, ExpiresAt: authorization.ExpiresAt, Delivery: string(authorization.Delivery), ContentType: authorization.ContentType}}, nil
	})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		params := []*huma.Param{}
		for _, name := range []string{"id", "theme_id"} {
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
		responses := map[string]*huma.Response{"200": {Description: "Original audio, or for a converted grant progressive AAC in audio-only MP4 with no length or ranges", Content: content, Headers: headers}, "206": {Description: themeRangeDescription, Content: content, Headers: headers}, "304": {Description: "Audio unchanged"}}
		for _, status := range []int{400, 401, 404, 412, 416, 500, 503} {
			responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}}
		}
		id := "getThemeSongAudio"
		if method == http.MethodHead {
			id = "headThemeSongAudio"
		}
		raw := RawOperation{Operation: Operation{Operation: huma.Operation{Method: method, Path: Prefix + "/catalog/items/{id}/themes/{theme_id}/audio", OperationID: id, Tags: []string{"catalog"}, Parameters: params, Responses: responses}, Class: ClassPublic, ServiceBacked: true}, Protocol: "theme-audio", Reason: "A scoped playback grant and current access checks authorize theme audio this node serves: original audio with range and conditional HTTP semantics, or a progressive AAC conversion."}
		var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if reg.deps.ThemeSongs == nil {
				writeProblem(w, r, unavailable("theme songs"))
				return
			}
			file, delivery, f, err := reg.deps.ThemeSongs.OpenGrant(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "theme_id"), r.URL.Query().Get(directAccountToken))
			if err != nil {
				writeProblem(w, r, themeSongProblem(err))
				return
			}
			if delivery == themesongs.DeliveryConverted {
				reg.deps.ThemeSongs.ServeConverted(&directDownloadWriter{ResponseWriter: w, request: r}, r, file)
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
	case errors.Is(err, themesongs.ErrNotAcceptable):
		return NewProblem(TypeNotAcceptable, "The client decodes neither this theme's format nor its AAC conversion.")
	case errors.Is(err, themedelivery.ErrPolicyUnsatisfied):
		return NewProblem(TypeDependencyUnavailable, "The playback routing policy admits no route for theme audio.")
	case errors.Is(err, themedelivery.ErrCapacityUnavailable):
		return NewProblem(TypeDependencyUnavailable, "No node can serve theme audio right now.").WithRetryAfter(30)
	case errors.Is(err, themesongs.ErrUnavailable):
		return unavailable("local theme audio")
	default:
		return NewProblem(TypeInternalError, "Theme audio could not be resolved.")
	}
}
