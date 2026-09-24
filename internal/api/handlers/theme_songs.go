package handlers

import (
	"context"
	"os"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

// ThemeSongsHandler adds current account/session/profile authority to the domain
// service. A grant delegates only the selected file, never general API access.
type ThemeSongsHandler struct {
	*themesongs.Service
	Sessions eventsSessionValidator
	Users    access.UserRepository
	Resolver apimw.ViewerResolver
}

func (h *ThemeSongsHandler) OpenGrant(ctx context.Context, owner, id, token string) (themesongs.File, *os.File, error) {
	grant, err := h.Validate(token, owner, id)
	if err != nil {
		return themesongs.File{}, nil, err
	}
	if h.Sessions == nil || h.Users == nil || h.Resolver == nil {
		return themesongs.File{}, nil, themesongs.ErrUnavailable
	}
	valid, err := h.Sessions.IsValid(ctx, grant.SessionID)
	if err != nil || !valid {
		return themesongs.File{}, nil, themesongs.ErrGrant
	}
	user, err := h.Users.GetByID(ctx, grant.UserID)
	if err != nil || user == nil || !user.Enabled || user.AccessPolicyRevision != grant.PolicyRevision {
		return themesongs.File{}, nil, themesongs.ErrGrant
	}
	scope, err := h.Resolver.Resolve(ctx, access.ResolveInput{UserID: grant.UserID, ProfileID: grant.ProfileID, SessionID: grant.SessionID, SkipPINVerification: true})
	if err != nil || !scope.ProfileVerified {
		return themesongs.File{}, nil, themesongs.ErrGrant
	}
	file, err := h.Select(ctx, owner, id, accessFilterFromScope(scope, grant.UserID, grant.ProfileID, ""))
	if err != nil {
		return themesongs.File{}, nil, err
	}
	if file.Size != grant.Size || file.Modified.UnixNano() != grant.Modified {
		return themesongs.File{}, nil, themesongs.ErrGrant
	}
	streamtelemetry.Attach(ctx, streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(grant.UserID), ProfileID: grant.ProfileID, PlayMethod: string(playback.PlayDirect)})
	f, err := themesongs.Open(file)
	return file, f, err
}
