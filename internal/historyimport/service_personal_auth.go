package historyimport

import (
	"context"
	"fmt"
	"slices"
)

// preparePersonalRun performs upstream exchanges without holding database locks.
// The repository revalidates captured sources/sessions and consumes a session in
// the same transaction that persists the run and its encrypted credentials.
func (s *Service) preparePersonalRun(ctx context.Context, userID int, input CreateRunInput) (personalRunAdmission, error) {
	out := personalRunAdmission{UserID: userID, ProfileID: input.ProfileID, SourceType: input.Source}
	switch input.Source {
	case SourceTypeEmby:
		mode, err := resolveConnectionMode(input)
		if err != nil {
			return out, err
		}
		out.ConnectionMode = mode
		var auth *embyLocalAuth
		switch mode {
		case ConnectionModeConnect:
			session, err := s.repo.GetConnectSession(ctx, userID, input.ConnectSessionID)
			if err != nil {
				return out, err
			}
			index := slices.IndexFunc(session.Servers, func(server ConnectServer) bool { return server.ID == input.ServerID })
			if index < 0 {
				return out, fmt.Errorf("%w: selected server is not in the connect session", ErrInvalidInput)
			}
			selected := session.Servers[index]
			baseURL := firstNonEmpty(selected.URL, selected.LocalAddress)
			if baseURL == "" {
				return out, fmt.Errorf("%w: selected server has no usable address", ErrInvalidInput)
			}
			auth, err = s.emby.ConnectExchange(ctx, baseURL, session.ConnectUserID, selected.AccessKey)
			if err != nil {
				return out, err
			}
			out.ConnectSession = session
			out.SelectedServerID = selected.ID
		case ConnectionModePredefined:
			if input.SourceID <= 0 || input.Username == "" || input.Password == "" {
				return out, fmt.Errorf("%w: source and user credentials are required", ErrInvalidInput)
			}
			source, err := s.repo.GetSourceByID(ctx, input.SourceID)
			if err != nil {
				return out, err
			}
			if !source.Enabled {
				return out, ErrSourceDisabled
			}
			if source.SourceType != SourceTypeEmby {
				return out, fmt.Errorf("%w: source is not an Emby server", ErrInvalidInput)
			}
			auth, err = s.emby.AuthenticateServerUser(ctx, source.BaseURL, input.Username, input.Password)
			if err != nil {
				return out, err
			}
			out.SourceID = source.ID
			out.SourceRevision = source.Revision
		default:
			return out, fmt.Errorf("%w: invalid connection mode", ErrInvalidInput)
		}
		out.Credentials = personalRunCredentials{BaseURL: auth.BaseURL, ExternalUserID: auth.UserID, ServerToken: auth.AccessToken}
	case SourceTypeJellyfin:
		if input.JellyfinBaseURL == "" || input.JellyfinUsername == "" || input.JellyfinPassword == "" {
			return out, fmt.Errorf("%w: Jellyfin address and credentials are required", ErrInvalidInput)
		}
		auth, err := s.jellyfin.AuthenticateServerUser(ctx, input.JellyfinBaseURL, input.JellyfinUsername, input.JellyfinPassword)
		if err != nil {
			return out, err
		}
		out.ConnectionMode = ConnectionModeCustom
		out.Credentials = personalRunCredentials{BaseURL: auth.BaseURL, ExternalUserID: auth.UserID, ServerToken: auth.AccessToken}
	case SourceTypePlex:
		out.ConnectionMode = ConnectionModePlexOAuth
		switch {
		case input.PlexSessionID != "":
			session, err := s.repo.GetPlexSession(ctx, userID, input.PlexSessionID)
			if err != nil {
				return out, err
			}
			if session.AuthToken == "" {
				return out, fmt.Errorf("%w: Plex sign-in is not complete", ErrInvalidInput)
			}
			index := slices.IndexFunc(session.Servers, func(server PlexServer) bool { return server.ClientIdentifier == input.PlexServerID })
			if index < 0 {
				return out, fmt.Errorf("%w: selected server is not in the Plex session", ErrInvalidInput)
			}
			selected := session.Servers[index]
			candidates := plexSessionCandidates(selected)
			if len(candidates) == 0 {
				return out, fmt.Errorf("%w: selected Plex server has no usable address", ErrInvalidInput)
			}
			out.PlexSession = session
			out.SelectedServerID = selected.ClientIdentifier
			out.Credentials = plexRunCredentials(candidates, selected.AccessToken, session.AuthToken)
		case input.PlexBaseURL != "" || len(plexBaseURLCandidates("", input.PlexBaseURLs)) > 0:
			if input.PlexToken == "" {
				return out, fmt.Errorf("%w: Plex token is required", ErrInvalidInput)
			}
			candidates := plexOAuthBaseURLCandidates(input.PlexBaseURL, input.PlexBaseURLs)
			if len(candidates) == 0 {
				return out, fmt.Errorf("%w: Plex server address is required", ErrInvalidInput)
			}
			out.Credentials = plexRunCredentials(candidates, input.PlexToken, firstNonEmpty(input.PlexAccountToken, input.PlexToken))
		case input.SourceID > 0:
			if input.PlexToken == "" {
				return out, fmt.Errorf("%w: Plex token is required", ErrInvalidInput)
			}
			source, err := s.repo.GetSourceByID(ctx, input.SourceID)
			if err != nil {
				return out, err
			}
			if !source.Enabled {
				return out, ErrSourceDisabled
			}
			if source.SourceType != SourceTypePlex {
				return out, fmt.Errorf("%w: source is not a Plex server", ErrInvalidInput)
			}
			out.SourceID = source.ID
			out.SourceRevision = source.Revision
			out.ConnectionMode = ConnectionModePredefined
			out.Credentials = personalRunCredentials{BaseURL: source.BaseURL, ServerToken: input.PlexToken, AccountToken: firstNonEmpty(input.PlexAccountToken, input.PlexToken)}
		default:
			return out, fmt.Errorf("%w: select a Plex session or server", ErrInvalidInput)
		}
	default:
		return out, fmt.Errorf("%w: unsupported source type", ErrInvalidInput)
	}
	return out, nil
}
