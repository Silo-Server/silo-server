package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/auth/passwordhash"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// compatSyncPlayAccessNone is Jellyfin's SyncPlayUserAccessType for a user
// who may not use SyncPlay.
const compatSyncPlayAccessNone = "None"

type authenticateByNameRequest struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
	Password string `json:"Password"`
}

// userPolicyResponse is Jellyfin's UserPolicy. Jellyfin always sends its
// list fields, as [] when empty, and strict decoders such as Lenna's reject a
// user without them, so they carry no omitempty.
type userPolicyResponse struct {
	IsAdministrator                  bool       `json:"IsAdministrator"`
	IsHidden                         bool       `json:"IsHidden"`
	EnableCollectionManagement       bool       `json:"EnableCollectionManagement"`
	EnableSubtitleManagement         bool       `json:"EnableSubtitleManagement"`
	EnableLyricManagement            bool       `json:"EnableLyricManagement"`
	IsDisabled                       bool       `json:"IsDisabled"`
	EnableUserPreferenceAccess       bool       `json:"EnableUserPreferenceAccess"`
	EnableRemoteControlOfOtherUsers  bool       `json:"EnableRemoteControlOfOtherUsers"`
	EnableSharedDeviceControl        bool       `json:"EnableSharedDeviceControl"`
	EnableRemoteAccess               bool       `json:"EnableRemoteAccess"`
	EnableLiveTVManagement           bool       `json:"EnableLiveTvManagement"`
	EnableLiveTVAccess               bool       `json:"EnableLiveTvAccess"`
	EnableMediaPlayback              bool       `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding   bool       `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding   bool       `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing           bool       `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding     bool       `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeletion            bool       `json:"EnableContentDeletion"`
	EnableContentDownloading         bool       `json:"EnableContentDownloading"`
	EnableSyncTranscoding            bool       `json:"EnableSyncTranscoding"`
	EnableMediaConversion            bool       `json:"EnableMediaConversion"`
	EnableAllDevices                 bool       `json:"EnableAllDevices"`
	EnableAllChannels                bool       `json:"EnableAllChannels"`
	EnableAllFolders                 bool       `json:"EnableAllFolders"`
	InvalidLoginAttemptCount         int        `json:"InvalidLoginAttemptCount"`
	LoginAttemptsBeforeLockout       int        `json:"LoginAttemptsBeforeLockout"`
	MaxActiveSessions                int        `json:"MaxActiveSessions"`
	EnablePublicSharing              bool       `json:"EnablePublicSharing"`
	RemoteClientBitrateLimit         int        `json:"RemoteClientBitrateLimit"`
	AuthenticationProviderID         string     `json:"AuthenticationProviderId"`
	PasswordResetProviderID          string     `json:"PasswordResetProviderId"`
	SyncPlayAccess                   string     `json:"SyncPlayAccess"`
	BlockedTags                      []string   `json:"BlockedTags"`
	AllowedTags                      []string   `json:"AllowedTags"`
	AccessSchedules                  []struct{} `json:"AccessSchedules"`
	BlockUnratedItems                []string   `json:"BlockUnratedItems"`
	EnableContentDeletionFromFolders []string   `json:"EnableContentDeletionFromFolders"`
	EnabledDevices                   []string   `json:"EnabledDevices"`
	EnabledChannels                  []string   `json:"EnabledChannels"`
	EnabledFolders                   []string   `json:"EnabledFolders"`
	BlockedMediaFolders              []string   `json:"BlockedMediaFolders"`
	BlockedChannels                  []string   `json:"BlockedChannels"`
}

type userDTOResponse struct {
	ID                        string                    `json:"Id"`
	Name                      string                    `json:"Name"`
	ServerID                  string                    `json:"ServerId"`
	HasPassword               bool                      `json:"HasPassword"`
	HasConfiguredPassword     bool                      `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool                      `json:"HasConfiguredEasyPassword"`
	EnableAutoLogin           bool                      `json:"EnableAutoLogin"`
	LastLoginDate             string                    `json:"LastLoginDate"`
	LastActivityDate          string                    `json:"LastActivityDate"`
	Policy                    userPolicyResponse        `json:"Policy"`
	Configuration             userConfigurationResponse `json:"Configuration"`
}

type userConfigurationResponse struct {
	AudioLanguagePreference    string   `json:"AudioLanguagePreference"`
	SubtitleLanguagePreference string   `json:"SubtitleLanguagePreference"`
	CastReceiverID             string   `json:"CastReceiverId"`
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

// sessionInfoResponse is the SessionInfo Jellyfin returns with a sign-in,
// carrying every field Jellyfin sends for a new session: strict decoders such
// as Lenna's reject the whole sign-in response when one is missing.
type sessionInfoResponse struct {
	PlayState             sessionPlayStateDTO        `json:"PlayState"`
	AdditionalUsers       []struct{}                 `json:"AdditionalUsers"`
	Capabilities          clientCapabilitiesResponse `json:"Capabilities"`
	RemoteEndPoint        string                     `json:"RemoteEndPoint"`
	PlayableMediaTypes    []string                   `json:"PlayableMediaTypes"`
	ID                    string                     `json:"Id"`
	UserID                string                     `json:"UserId"`
	UserName              string                     `json:"UserName"`
	Client                string                     `json:"Client"`
	LastActivityDate      string                     `json:"LastActivityDate"`
	LastPlaybackCheckIn   string                     `json:"LastPlaybackCheckIn"`
	DeviceName            string                     `json:"DeviceName"`
	DeviceID              string                     `json:"DeviceId"`
	ApplicationVersion    string                     `json:"ApplicationVersion"`
	IsActive              bool                       `json:"IsActive"`
	SupportsMediaControl  bool                       `json:"SupportsMediaControl"`
	SupportsRemoteControl bool                       `json:"SupportsRemoteControl"`
	NowPlayingQueue       []struct{}                 `json:"NowPlayingQueue"`
	HasCustomDeviceName   bool                       `json:"HasCustomDeviceName"`
	SupportedCommands     []string                   `json:"SupportedCommands"`
	ServerID              string                     `json:"ServerId"`
}

// clientCapabilitiesResponse is Jellyfin's ClientCapabilitiesDto for a
// session that has not posted its capabilities.
type clientCapabilitiesResponse struct {
	PlayableMediaTypes           []string `json:"PlayableMediaTypes"`
	SupportedCommands            []string `json:"SupportedCommands"`
	SupportsMediaControl         bool     `json:"SupportsMediaControl"`
	SupportsPersistentIdentifier bool     `json:"SupportsPersistentIdentifier"`
}

type loginResolver interface {
	Resolve(ctx context.Context, combinedUsername, password, userAgent, remoteIP string) (*Session, error)
}

// quickConnectLogin is the login resolver's Quick Connect exchange
// (LoginResolver.ResolveQuickConnect).
type quickConnectLogin interface {
	ResolveQuickConnect(ctx context.Context, secret string) (*Session, error)
}

// quickConnectRequest is Jellyfin's QuickConnectDto.
type quickConnectRequest struct {
	Secret string `json:"Secret"`
}

// maxQuickConnectBody bounds the AuthenticateWithQuickConnect body, which
// holds one 64-character secret.
const maxQuickConnectBody = 4 << 10

// AuthHandler serves Jellyfin login/current-user routes.
type AuthHandler struct {
	cfg           func() *config.Config
	loginResolver loginResolver
	authenticator *Authenticator
	storeProvider userstore.UserStoreProvider
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

// maxAuthenticateByNameBodyBytes bounds the anonymous sign-in body, which
// holds only a username and password.
const maxAuthenticateByNameBodyBytes = 64 << 10

// HandleAuthenticateByName serves POST /Users/AuthenticateByName.
func (h *AuthHandler) HandleAuthenticateByName(w http.ResponseWriter, r *http.Request) {
	var req authenticateByNameRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthenticateByNameBodyBytes)).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "PayloadTooLarge", "Request body too large")
			return
		}
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
		if throttled, ok := errors.AsType[*auth.SignInThrottledError](err); ok {
			// The name has used its sign-in budget; answered like the
			// listener's other 429s.
			retryAfter := ratelimit.RetryAfterSeconds(throttled.RetryAfter)
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeCompatRateLimited(w, retryAfter)
			return
		}
		if errors.Is(err, passwordhash.ErrBusy) {
			w.Header().Set("Retry-After", strconv.Itoa(passwordhash.RetryAfterSeconds))
		}
		status, code, message := mapLoginError(err)
		writeError(w, status, code, message)
		return
	}
	attributeActivity(r, session)

	user, err := h.resolvedUserDTO(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, authenticateByNameResponse{
		AccessToken: session.Token,
		ServerID:    h.cfg().JellyfinCompat.ServerID,
		User:        user,
		SessionInfo: h.sessionInfo(r, session),
	})
}

// HandleAuthenticateWithQuickConnect serves POST
// /Users/AuthenticateWithQuickConnect: the secret of an approved Quick
// Connect request buys one session, answered as AuthenticateByName answers.
// A secret that is not an approved, uncollected request is 404, as Jellyfin
// answers one it does not hold.
func (h *AuthHandler) HandleAuthenticateWithQuickConnect(w http.ResponseWriter, r *http.Request) {
	quick, ok := h.loginResolver.(quickConnectLogin)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized", quickConnectDisabledMessage)
		return
	}
	var req quickConnectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQuickConnectBody)).Decode(&req); err != nil || req.Secret == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "The secret is required")
		return
	}
	session, err := quick.ResolveQuickConnect(r.Context(), req.Secret)
	if errors.Is(err, ErrQuickConnectNotAuthorized) {
		writeError(w, http.StatusNotFound, "NotFound", "Unable to find request")
		return
	}
	if err != nil {
		status, code, message := mapLoginError(err)
		writeError(w, status, code, message)
		return
	}
	attributeActivity(r, session)

	user, err := h.resolvedUserDTO(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, authenticateByNameResponse{
		AccessToken: session.Token,
		ServerID:    h.cfg().JellyfinCompat.ServerID,
		User:        user,
		SessionInfo: h.sessionInfo(r, session),
	})
}

// HandleCurrentUser serves GET /Users/Me.
func (h *AuthHandler) HandleCurrentUser(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	user, err := h.resolvedUserDTO(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
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
	user, err := h.resolvedUserDTO(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, []userDTOResponse{user})
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
	user, err := h.resolvedUserDTO(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// HandleLogout serves POST /Sessions/Logout.
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	token, ok := ExtractToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if err := h.authenticator.sessions.Delete(token); err != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Could not sign out")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) userDTO(session *Session) userDTOResponse {
	name := session.Username
	now := time.Now()
	lastLogin := session.CreatedAt
	if lastLogin.IsZero() {
		lastLogin = now
	}

	return userDTOResponse{
		ID:                        session.PseudoUserID.String(),
		Name:                      name,
		ServerID:                  h.cfg().JellyfinCompat.ServerID,
		HasPassword:               true,
		HasConfiguredPassword:     true,
		HasConfiguredEasyPassword: false,
		EnableAutoLogin:           false,
		LastLoginDate:             jellyfinDate(lastLogin),
		LastActivityDate:          jellyfinDate(now),
		Policy: userPolicyResponse{
			IsAdministrator:                  false,
			IsHidden:                         false,
			EnableCollectionManagement:       false,
			EnableSubtitleManagement:         false,
			EnableLyricManagement:            false,
			IsDisabled:                       false,
			EnableUserPreferenceAccess:       true,
			EnableRemoteControlOfOtherUsers:  false,
			EnableSharedDeviceControl:        false,
			EnableRemoteAccess:               true,
			EnableLiveTVManagement:           false,
			EnableLiveTVAccess:               false,
			EnableMediaPlayback:              true,
			EnableAudioPlaybackTranscoding:   true,
			EnableVideoPlaybackTranscoding:   true,
			EnablePlaybackRemuxing:           true,
			ForceRemoteSourceTranscoding:     false,
			EnableContentDeletion:            false,
			EnableContentDownloading:         true,
			EnableSyncTranscoding:            false,
			EnableMediaConversion:            false,
			EnableAllDevices:                 true,
			EnableAllChannels:                false,
			EnableAllFolders:                 true,
			InvalidLoginAttemptCount:         0,
			LoginAttemptsBeforeLockout:       0,
			MaxActiveSessions:                0,
			EnablePublicSharing:              false,
			RemoteClientBitrateLimit:         0,
			AuthenticationProviderID:         "silo-local",
			PasswordResetProviderID:          "silo-local",
			SyncPlayAccess:                   compatSyncPlayAccessNone,
			BlockedTags:                      []string{},
			AllowedTags:                      []string{},
			AccessSchedules:                  []struct{}{},
			BlockUnratedItems:                []string{},
			EnableContentDeletionFromFolders: []string{},
			EnabledDevices:                   []string{},
			EnabledChannels:                  []string{},
			EnabledFolders:                   []string{},
			BlockedMediaFolders:              []string{},
			BlockedChannels:                  []string{},
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

// sessionInfo describes the signing-in device from its MediaBrowser
// authorization header. Id is stable per user and device, as Jellyfin keeps
// one session per device. A sign-in that names no device gets an Id from its
// login session, so a Quick Connect retry that returns the same session keeps
// its Id and no two sign-ins share one. Id never derives from the access token.
func (h *AuthHandler) sessionInfo(r *http.Request, session *Session) *sessionInfoResponse {
	name := session.ProfileName
	if name == "" {
		name = session.Username
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	deviceID := authorizationValue(r, "DeviceId")
	var sessionID uuid.UUID
	switch {
	case deviceID != "":
		sessionID = uuid.NewSHA1(session.PseudoUserID, []byte(deviceID))
	case session.LoginSessionID != "":
		sessionID = uuid.NewSHA1(session.PseudoUserID, []byte("login-session\x00"+session.LoginSessionID))
	default:
		sessionID = uuid.New()
	}

	return &sessionInfoResponse{
		PlayState:       sessionPlayStateDTO{RepeatMode: compatRepeatNone, PlaybackOrder: compatPlaybackOrderDefault},
		AdditionalUsers: []struct{}{},
		Capabilities: clientCapabilitiesResponse{
			PlayableMediaTypes: []string{},
			SupportedCommands:  []string{},
		},
		RemoteEndPoint:        clientip.FromContext(r.Context()),
		PlayableMediaTypes:    []string{},
		ID:                    strings.ReplaceAll(sessionID.String(), "-", ""),
		UserID:                session.PseudoUserID.String(),
		UserName:              name,
		Client:                authorizationValue(r, "Client"),
		LastActivityDate:      now,
		LastPlaybackCheckIn:   now,
		DeviceName:            authorizationValue(r, "Device"),
		DeviceID:              deviceID,
		ApplicationVersion:    authorizationValue(r, "Version"),
		IsActive:              true,
		SupportsMediaControl:  false,
		SupportsRemoteControl: false,
		NowPlayingQueue:       []struct{}{},
		HasCustomDeviceName:   false,
		SupportedCommands:     []string{},
		ServerID:              h.cfg().JellyfinCompat.ServerID,
	}
}
