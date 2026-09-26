package handlers

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/themedelivery"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

// ThemeSongsHandler adds current account/session/profile authority to the domain
// service. A grant delegates only the selected file, never general API access.
type ThemeSongsHandler struct {
	*themesongs.Service
	Sessions eventsSessionValidator
	Users    access.UserRepository
	Resolver apimw.ViewerResolver
	// Router routes themes through the playback routing policy. Nil serves
	// every theme from this API node.
	Router *themedelivery.Router
	// FFmpegPath locates the encoder for conversions this node serves itself.
	FFmpegPath func() string
}

// Authorize selects a theme under the viewer's current access, chooses the
// original or the AAC conversion from the formats the client decodes, and
// routes it. A worker route returns the worker URL; a local route returns a
// grant for this node's audio route, which rechecks authority per request.
func (h *ThemeSongsHandler) Authorize(ctx context.Context, identity themesongs.Identity, owner, id string, filter catalog.AccessFilter, accepted []themesongs.Format, accessExpiry time.Time) (themesongs.Authorization, error) {
	file, err := h.Select(ctx, owner, id, filter)
	if err != nil {
		return themesongs.Authorization{}, err
	}
	delivery, ok := themesongs.Negotiate(file, accepted)
	if !ok || (delivery == themesongs.DeliveryConverted && h.Router != nil && !h.Router.CanConvert(ctx)) {
		// Neither the original nor a conversion this deployment can run.
		return themesongs.Authorization{}, themesongs.ErrNotAcceptable
	}
	expires, err := themesongs.Expiry(time.Now(), accessExpiry)
	if err != nil {
		return themesongs.Authorization{}, err
	}
	if h.Router != nil {
		result, err := h.Router.Resolve(ctx, themedelivery.Request{
			File: file, Delivery: delivery, Conversion: themesongs.ConversionFor(file),
			UserID: identity.UserID, ProfileID: identity.ProfileID,
			AccessPath: netaccess.PathFromContext(ctx), ExpiresAt: expires,
		})
		if err != nil {
			return themesongs.Authorization{}, err
		}
		if !result.Local() {
			return themesongs.Authorization{URL: result.URL, Delivery: delivery, ContentType: themesongs.DeliveryContentType(file, delivery), ExpiresAt: expires}, nil
		}
	}
	// Served here: prove this node can read the file before handing out a URL.
	f, err := themesongs.Open(file)
	if err != nil {
		return themesongs.Authorization{}, err
	}
	_ = f.Close()
	grant, grantExpires, err := h.Mint(identity, owner, file, delivery, accessExpiry)
	if err != nil {
		return themesongs.Authorization{}, err
	}
	return themesongs.Authorization{Grant: grant, Delivery: delivery, ContentType: themesongs.DeliveryContentType(file, delivery), ExpiresAt: grantExpires}, nil
}

// OpenGrant revalidates a grant for this node's audio route against current
// authority. An original grant returns the opened file; a converted grant
// returns no file after proving it is readable, and is served by ServeConverted.
func (h *ThemeSongsHandler) OpenGrant(ctx context.Context, owner, id, token string) (themesongs.File, themesongs.Delivery, *os.File, error) {
	grant, err := h.Validate(token, owner, id)
	if err != nil {
		return themesongs.File{}, "", nil, err
	}
	if h.Sessions == nil || h.Users == nil || h.Resolver == nil {
		return themesongs.File{}, "", nil, themesongs.ErrUnavailable
	}
	valid, err := h.Sessions.IsValid(ctx, grant.SessionID)
	if err != nil || !valid {
		return themesongs.File{}, "", nil, themesongs.ErrGrant
	}
	user, err := h.Users.GetByID(ctx, grant.UserID)
	if err != nil || user == nil || !user.Enabled || user.AccessPolicyRevision != grant.PolicyRevision {
		return themesongs.File{}, "", nil, themesongs.ErrGrant
	}
	scope, err := h.Resolver.Resolve(ctx, access.ResolveInput{UserID: grant.UserID, ProfileID: grant.ProfileID, SessionID: grant.SessionID, SkipPINVerification: true})
	if err != nil || !scope.ProfileVerified {
		return themesongs.File{}, "", nil, themesongs.ErrGrant
	}
	file, err := h.Select(ctx, owner, id, accessFilterFromScope(scope, grant.UserID, grant.ProfileID, ""))
	if err != nil {
		return themesongs.File{}, "", nil, err
	}
	if file.Size != grant.Size || file.Modified.UnixNano() != grant.Modified {
		return themesongs.File{}, "", nil, themesongs.ErrGrant
	}
	method := playback.PlayDirect
	if grant.Delivery == themesongs.DeliveryConverted {
		method = playback.PlayRemux
	}
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(grant.UserID), ProfileID: grant.ProfileID, PlayMethod: string(method)})
	f, err := themesongs.Open(file)
	if err != nil {
		return themesongs.File{}, "", nil, err
	}
	if grant.Delivery == themesongs.DeliveryConverted {
		// FFmpeg opens the path itself. The check above is the same one an
		// original grant gets; a file replaced in the gap is converted as found.
		_ = f.Close()
		return file, grant.Delivery, nil, nil
	}
	return file, grant.Delivery, f, nil
}

// ServeConverted streams the AAC conversion of a theme this node serves.
func (h *ThemeSongsHandler) ServeConverted(w http.ResponseWriter, r *http.Request, file themesongs.File) {
	ffmpeg := ""
	if h.FFmpegPath != nil {
		ffmpeg = h.FFmpegPath()
	}
	themesongs.ServeConverted(w, r, file.Path, themesongs.ConversionFor(file), 0, ffmpeg)
}

// ThemeCapabilities reports theme delivery on this deployment.
func (h *ThemeSongsHandler) ThemeCapabilities(ctx context.Context) themesongs.Capabilities {
	return themesongs.Capabilities{Transcode: h.Router.CanConvert(ctx), ClusterRouting: h.Router.ClusterRouting()}
}
