package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
)

type authenticateByNameRequest struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
	Password string `json:"Password"`
}

type userPolicyResponse struct {
	IsAdministrator                  bool     `json:"IsAdministrator"`
	IsHidden                         bool     `json:"IsHidden"`
	EnableCollectionManagement       bool     `json:"EnableCollectionManagement"`
	EnableSubtitleManagement         bool     `json:"EnableSubtitleManagement"`
	EnableLyricManagement            bool     `json:"EnableLyricManagement"`
	IsDisabled                       bool     `json:"IsDisabled"`
	EnableUserPreferenceAccess       bool     `json:"EnableUserPreferenceAccess"`
	EnableRemoteControlOfOtherUsers  bool     `json:"EnableRemoteControlOfOtherUsers"`
	EnableSharedDeviceControl        bool     `json:"EnableSharedDeviceControl"`
	EnableRemoteAccess               bool     `json:"EnableRemoteAccess"`
	EnableLiveTVManagement           bool     `json:"EnableLiveTvManagement"`
	EnableLiveTVAccess               bool     `json:"EnableLiveTvAccess"`
	EnableMediaPlayback              bool     `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding   bool     `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding   bool     `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing           bool     `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding     bool     `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeletion            bool     `json:"EnableContentDeletion"`
	EnableContentDownloading         bool     `json:"EnableContentDownloading"`
	EnableSyncTranscoding            bool     `json:"EnableSyncTranscoding"`
	EnableMediaConversion            bool     `json:"EnableMediaConversion"`
	EnableAllDevices                 bool     `json:"EnableAllDevices"`
	EnableAllChannels                bool     `json:"EnableAllChannels"`
	EnableAllFolders                 bool     `json:"EnableAllFolders"`
	InvalidLoginAttemptCount         int      `json:"InvalidLoginAttemptCount"`
	LoginAttemptsBeforeLockout       int      `json:"LoginAttemptsBeforeLockout"`
	MaxActiveSessions                int      `json:"MaxActiveSessions"`
	EnablePublicSharing              bool     `json:"EnablePublicSharing"`
	RemoteClientBitrateLimit         int      `json:"RemoteClientBitrateLimit"`
	AuthenticationProviderID         string   `json:"AuthenticationProviderId"`
	PasswordResetProviderID          string   `json:"PasswordResetProviderId"`
	SyncPlayAccess                   string   `json:"SyncPlayAccess"`
	BlockedTags                      []string `json:"BlockedTags,omitempty"`
	AllowedTags                      []string `json:"AllowedTags,omitempty"`
	EnableContentDeletionFromFolders []string `json:"EnableContentDeletionFromFolders,omitempty"`
	EnabledDevices                   []string `json:"EnabledDevices,omitempty"`
	EnabledChannels                  []string `json:"EnabledChannels,omitempty"`
	EnabledFolders                   []string `json:"EnabledFolders,omitempty"`
	BlockedMediaFolders              []string `json:"BlockedMediaFolders,omitempty"`
	BlockedChannels                  []string `json:"BlockedChannels,omitempty"`
}

type userDTOResponse struct {
	ID                        string                    `json:"Id"`
	Name                      string                    `json:"Name"`
	ServerID                  string                    `json:"ServerId"`
	HasPassword               bool                      `json:"HasPassword"`
	HasConfiguredPassword     bool                      `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool                      `json:"HasConfiguredEasyPassword"`
	Policy                    userPolicyResponse        `json:"Policy"`
	Configuration             userConfigurationResponse `json:"Configuration"`
}

type userConfigurationResponse struct {
	PlayDefaultAudioTrack      bool     `json:"PlayDefaultAudioTrack"`
	DisplayMissingEpisodes     bool     `json:"DisplayMissingEpisodes"`
	GroupedFolders             []string `json:"GroupedFolders"`
	SubtitleMode               string   `json:"SubtitleMode"`
	DisplayCollectionsView     bool     `json:"DisplayCollectionsView"`
	EnableLocalPassword        bool     `json:"EnableLocalPassword"`
	OrderedViews               []string `json:"OrderedViews"`
	LatestItemsExcludes        []string `json:"LatestItemsExcludes"`
	MyMediaExcludes            []string `json:"MyMediaExcludes"`
	HidePlayedInLatest         bool     `json:"HidePlayedInLatest"`
	RememberAudioSelections    bool     `json:"RememberAudioSelections"`
	RememberSubtitleSelections bool     `json:"RememberSubtitleSelections"`
	EnableNextEpisodeAutoPlay  bool     `json:"EnableNextEpisodeAutoPlay"`
}

type authenticateByNameResponse struct {
	AccessToken string               `json:"AccessToken"`
	ServerID    string               `json:"ServerId"`
	User        userDTOResponse      `json:"User"`
	SessionInfo *sessionInfoResponse `json:"SessionInfo,omitempty"`
}

type sessionInfoResponse struct {
	PlayableMediaTypes    []string `json:"PlayableMediaTypes"`
	UserID                string   `json:"UserId"`
	UserName              string   `json:"UserName"`
	LastActivityDate      string   `json:"LastActivityDate"`
	LastPlaybackCheckIn   string   `json:"LastPlaybackCheckIn"`
	IsActive              bool     `json:"IsActive"`
	SupportsMediaControl  bool     `json:"SupportsMediaControl"`
	SupportsRemoteControl bool     `json:"SupportsRemoteControl"`
	HasCustomDeviceName   bool     `json:"HasCustomDeviceName"`
	SupportedCommands     []string `json:"SupportedCommands"`
	ServerID              string   `json:"ServerId"`
}

type loginResolver interface {
	Resolve(ctx context.Context, combinedUsername, password, userAgent, remoteIP string) (*Session, error)
}

// AuthHandler serves Jellyfin login/current-user routes.
type AuthHandler struct {
	cfg           func() *config.Config
	loginResolver loginResolver
	authenticator *Authenticator
}

// NewAuthHandler creates a new auth handler. The config provider is invoked
// per request so compat setting changes apply without restart.
func NewAuthHandler(cfg func() *config.Config, loginResolver loginResolver, authenticator *Authenticator) *AuthHandler {
	return &AuthHandler{
		cfg:           cfg,
		loginResolver: loginResolver,
		authenticator: authenticator,
	}
}

// HandlePublicUsers serves GET /Users/Public.
func (h *AuthHandler) HandlePublicUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

// HandleAuthenticateByName serves POST /Users/AuthenticateByName.
func (h *AuthHandler) HandleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	var req authenticateByNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid request body")
		return
	}

	password := req.Pw
	if password == "" {
		password = req.Password
	}
	if req.Username == "" || password == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "Username and password are required")
		return
	}

	session, err := h.loginResolver.Resolve(r.Context(), req.Username, password, r.UserAgent(), clientip.FromContext(r.Context()))
	if err != nil {
		status, code, message := mapLoginError(err)
		writeError(w, status, code, message)
		return
	}

	writeJSON(w, http.StatusOK, authenticateByNameResponse{
		AccessToken: session.Token,
		ServerID:    h.cfg().JellyfinCompat.ServerID,
		User:        h.userDTO(session),
		SessionInfo: h.sessionInfo(session),
	})
}

// HandleCurrentUser serves GET /Users/Me.
func (h *AuthHandler) HandleCurrentUser(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	writeJSON(w, http.StatusOK, h.userDTO(session))
}

// HandleUsers serves GET /Users.
//
// Jellyfin's user-list endpoint accepts any authenticated caller (including an
// API key) and is what Tunarr probes to verify an API-key connection (the key
// has no "me"). Silo separates login accounts from household profiles and is
// multi-account, so we return only the caller's own user as a single-element
// list rather than enumerating every account's profiles — listing all users
// would leak identities across households.
func (h *AuthHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	writeJSON(w, http.StatusOK, []userDTOResponse{h.userDTO(session)})
}

// HandleUserByID serves GET /Users/{id}.
func (h *AuthHandler) HandleUserByID(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	if chi.URLParam(r, "id") != session.PseudoUserID.String() {
		writeError(w, http.StatusNotFound, "NotFound", "User not found")
		return
	}
	writeJSON(w, http.StatusOK, h.userDTO(session))
}

// HandleLogout serves POST /Sessions/Logout.
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	token, ok := ExtractToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	h.authenticator.sessions.Delete(token)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) userDTO(session *Session) userDTOResponse {
	name := session.Username

	return userDTOResponse{
		ID:                        session.PseudoUserID.String(),
		Name:                      name,
		ServerID:                  h.cfg().JellyfinCompat.ServerID,
		HasPassword:               true,
		HasConfiguredPassword:     true,
		HasConfiguredEasyPassword: false,
		Policy: userPolicyResponse{
			IsAdministrator:                 false,
			IsHidden:                        false,
			EnableCollectionManagement:      false,
			EnableSubtitleManagement:        false,
			EnableLyricManagement:           false,
			IsDisabled:                      false,
			EnableUserPreferenceAccess:      true,
			EnableRemoteControlOfOtherUsers: false,
			EnableSharedDeviceControl:       false,
			EnableRemoteAccess:              true,
			EnableLiveTVManagement:          false,
			EnableLiveTVAccess:              false,
			EnableMediaPlayback:             true,
			EnableAudioPlaybackTranscoding:  true,
			EnableVideoPlaybackTranscoding:  true,
			EnablePlaybackRemuxing:          true,
			ForceRemoteSourceTranscoding:    false,
			EnableContentDeletion:           false,
			EnableContentDownloading:        true,
			EnableSyncTranscoding:           false,
			EnableMediaConversion:           false,
			EnableAllDevices:                true,
			EnableAllChannels:               false,
			EnableAllFolders:                true,
			InvalidLoginAttemptCount:        0,
			LoginAttemptsBeforeLockout:      0,
			MaxActiveSessions:               0,
			EnablePublicSharing:             false,
			RemoteClientBitrateLimit:        0,
			AuthenticationProviderID:        "silo-local",
			PasswordResetProviderID:         "silo-local",
			SyncPlayAccess:                  "None",
		},
		Configuration: userConfigurationResponse{
			PlayDefaultAudioTrack:      true,
			DisplayMissingEpisodes:     false,
			GroupedFolders:             []string{},
			SubtitleMode:               "Smart",
			DisplayCollectionsView:     true,
			EnableLocalPassword:        false,
			OrderedViews:               []string{},
			LatestItemsExcludes:        []string{},
			MyMediaExcludes:            []string{},
			HidePlayedInLatest:         false,
			RememberAudioSelections:    true,
			RememberSubtitleSelections: true,
			EnableNextEpisodeAutoPlay:  true,
		},
	}
}

func (h *AuthHandler) sessionInfo(session *Session) *sessionInfoResponse {
	name := session.ProfileName
	if name == "" {
		name = session.Username
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	return &sessionInfoResponse{
		PlayableMediaTypes:    []string{},
		UserID:                session.PseudoUserID.String(),
		UserName:              name,
		LastActivityDate:      now,
		LastPlaybackCheckIn:   now,
		IsActive:              true,
		SupportsMediaControl:  true,
		SupportsRemoteControl: true,
		HasCustomDeviceName:   false,
		SupportedCommands:     []string{},
		ServerID:              h.cfg().JellyfinCompat.ServerID,
	}
}
