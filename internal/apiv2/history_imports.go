package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// HistoryImportService is the account-owned history import service.
type HistoryImportService interface {
	ListImportSources(context.Context) ([]historyimport.Source, error)
	ListImportRunsPage(context.Context, int, *historyimport.RunKey, int) ([]historyimport.Run, bool, error)
	CreateImportRun(context.Context, int, historyimport.CreateRunInput) (*historyimport.Run, error)
	GetImportRun(context.Context, int, string) (*historyimport.Run, error)
	CreatePlexPin(context.Context, int) (*historyimport.PlexPinResponse, error)
	CheckPlexPin(context.Context, int, string) (*historyimport.PlexCheckResponse, error)
	LoginEmbyConnect(context.Context, int, historyimport.LoginConnectInput) (*historyimport.ConnectSessionLoginResult, error)
}

// The history-imports domain: pulling a profile's watch history from an
// Emby, Jellyfin, or Plex server. Every operation is account level (the
// target profile is named in the run request), so the class is profile
// scoped with an optional header, as the v1 group is mounted without
// RequireProfile.

// HistoryImportSource is an administrator-configured server a user may
// import from.
type HistoryImportSource struct {
	ID            ID      `json:"id" doc:"Source identifier" example:"1"`
	Name          string  `json:"name" example:"Family Plex"`
	SourceType    string  `json:"source_type" doc:"emby, jellyfin, or plex" example:"plex"`
	BaseURL       string  `json:"base_url,omitempty" doc:"Server address; absent for Plex sources reached through plex.tv" example:"https://emby.example.test"`
	SystemID      string  `json:"system_id,omitempty" doc:"The source server's own identifier when known"`
	Enabled       bool    `json:"enabled" example:"true"`
	SortOrder     int     `json:"sort_order" example:"0"`
	HasAdminToken bool    `json:"has_admin_token" doc:"Whether an administrator token lets users import without their own credentials" example:"true"`
	CreatedAt     Instant `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	UpdatedAt     Instant `json:"updated_at" example:"2026-01-02T03:04:05.678Z"`
}

// HistoryImportSourceCollection is the listHistoryImportSources envelope: the
// configured sources, a short administrator-bounded list.
type HistoryImportSourceCollection struct {
	Collection[HistoryImportSource]
}

// HistoryImportSourceCollectionOutput is the listHistoryImportSources response.
type HistoryImportSourceCollectionOutput struct {
	Body HistoryImportSourceCollection
}

// HistoryImportUnmatchedSample is one imported title the matcher could not
// place in the catalog.
type HistoryImportUnmatchedSample struct {
	Kind   string `json:"kind" example:"movie"`
	Title  string `json:"title" example:"Example Movie"`
	Year   int    `json:"year,omitzero" doc:"Absent when the source carried no year" example:"2021"`
	Reason string `json:"reason" example:"no catalog match"`
}

// HistoryImportRun is one import execution and its counters. Status is
// queued, running, canceling, completed, failed, or the stored cancel state;
// it is not declared as an enum because stored values predate
// the vocabulary.
type HistoryImportRun struct {
	Terminal          bool                           `json:"terminal" doc:"Whether this run has reached its final state"`
	Cancelable        bool                           `json:"cancelable" doc:"False on the personal API, which has no cancellation command"`
	ID                string                         `json:"id" doc:"Run identifier" example:"7b1d0d2e-3f4a-4b5c-8d6e-9f0a1b2c3d4e"`
	UserID            ID                             `json:"user_id" doc:"The account that started the run" example:"1"`
	ProfileID         ID                             `json:"profile_id" doc:"The profile the history is written to" example:"p-owner"`
	SourceType        string                         `json:"source_type" doc:"emby, jellyfin, or plex" example:"plex"`
	ConnectionMode    string                         `json:"connection_mode" doc:"How the source was reached (connect, custom, plex_oauth, predefined)" example:"plex_oauth"`
	Status            string                         `json:"status" doc:"queued, running, canceling, completed, failed, or the stored cancel state" example:"queued"`
	MappingID         *ID                            `json:"mapping_id,omitempty" doc:"The administrator user mapping the run used; absent for self-service runs" example:"3"`
	Fetched           int                            `json:"fetched" example:"0"`
	Matched           int                            `json:"matched" example:"0"`
	Unmatched         int                            `json:"unmatched" example:"0"`
	ProgressUpdated   int                            `json:"progress_updated" example:"0"`
	HistoryCreated    int                            `json:"history_created" example:"0"`
	WatchlistAdded    int                            `json:"watchlist_added" example:"0"`
	FavoritesImported int                            `json:"favorites_imported" example:"0"`
	Skipped           int                            `json:"skipped" example:"0"`
	Warnings          []string                       `json:"warnings" doc:"Non-fatal notes; empty, never null"`
	UnmatchedSamples  []HistoryImportUnmatchedSample `json:"unmatched_samples" doc:"Up to ten unmatched titles; empty, never null"`
	ErrorMessage      string                         `json:"error_message,omitempty" doc:"Failure reason; absent unless the run failed"`
	CreatedAt         Instant                        `json:"created_at" example:"2026-01-02T03:04:05.678Z"`
	StartedAt         *Instant                       `json:"started_at,omitempty" doc:"Absent until the run starts"`
	CompletedAt       *Instant                       `json:"completed_at,omitempty" doc:"Absent until the run finishes"`
}

// HistoryImportRunOutput is a single-run response.
type HistoryImportRunOutput struct {
	Status     int
	ETag       string `header:"ETag"`
	Location   string `header:"Location"`
	RetryAfter string `header:"Retry-After"`
	Body       HistoryImportRun
}

// HistoryImportRunAcceptedOutput is the createHistoryImportRun response: the
// queued run, where to poll it, and how soon.
type HistoryImportRunAcceptedOutput struct {
	Location   string `header:"Location" doc:"The run's resource path"`
	RetryAfter int    `header:"Retry-After" doc:"Seconds before polling the run"`
	Body       HistoryImportRun
}

// HistoryImportRunIDInput names one run.
type HistoryImportRunIDInput struct {
	ID string `path:"id" doc:"The run" example:"7b1d0d2e-3f4a-4b5c-8d6e-9f0a1b2c3d4e"`
}

// HistoryImportRunGetInput supports conditional account-owned monitoring.
type HistoryImportRunGetInput struct {
	HistoryImportRunIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

// HistoryImportRunListInput is the listHistoryImportRuns query.
type HistoryImportRunListInput struct {
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvZmZzZXQiOjUwfQ"`
}

// HistoryImportRunCollection is the named envelope the contract carries.
type HistoryImportRunCollection struct {
	Collection[HistoryImportRun]
}

// HistoryImportRunCollectionOutput is the listHistoryImportRuns response.
type HistoryImportRunCollectionOutput struct {
	Body HistoryImportRunCollection
}

// runPosition is the cursor payload: the keyset (created_at, id) of the last
// run the previous page emitted.
type runPosition struct {
	CreatedAt time.Time `json:"c"`
	ID        string    `json:"i"`
}

// HistoryImportRunCreate is the createHistoryImportRun body. The members a
// source needs depend on source; the service rejects an incomplete
// combination with a validation problem.
type HistoryImportRunCreate struct {
	ProfileID        ID       `json:"profile_id" minLength:"1" doc:"The profile to write history into" example:"p-owner"`
	Source           string   `json:"source" enum:"emby,jellyfin,plex" doc:"Which kind of server to import from" example:"plex"`
	ConnectSessionID string   `json:"connect_session_id,omitempty" doc:"Emby: the session from loginEmbyConnect" example:"9c3e1f0a-1b2c-4d5e-8f6a-7b8c9d0e1f2a"`
	ServerID         string   `json:"server_id,omitempty" doc:"Emby: the server chosen from the connect session"`
	SourceID         *ID      `json:"source_id,omitempty" nullable:"false" doc:"Emby or Plex: a configured source from listHistoryImportSources" example:"1"`
	Username         string   `json:"username,omitempty" doc:"Emby: the source server user name when importing from a configured source"`
	Password         string   `json:"password,omitempty" doc:"Emby: the source server password when importing from a configured source"`
	JellyfinBaseURL  string   `json:"jellyfin_base_url,omitempty" doc:"Jellyfin: the server address" example:"https://jellyfin.example.test"`
	JellyfinUsername string   `json:"jellyfin_username,omitempty" doc:"Jellyfin: the user name"`
	JellyfinPassword string   `json:"jellyfin_password,omitempty" doc:"Jellyfin: the password"`
	PlexSessionID    string   `json:"plex_session_id,omitempty" doc:"Plex: the authenticated session from createPlexPin"`
	PlexServerID     string   `json:"plex_server_id,omitempty" doc:"Plex: the client identifier of the server chosen from checkPlexPin"`
	PlexBaseURL      string   `json:"plex_base_url,omitempty" doc:"Plex: a server address when the client holds its own token"`
	PlexBaseURLs     []string `json:"plex_base_urls,omitempty" maxItems:"8" doc:"Plex: the other addresses plex.tv advertised for the same server, in preference order. The run races them and keeps the first that answers, so a server whose preferred address is unreachable still imports. plex_base_url stays the preferred one." example:"[\"https://relay.plex.direct:443\"]"`
	PlexToken        string   `json:"plex_token,omitempty" doc:"Plex: the server access token that goes with plex_base_url"`
	PlexAccountToken string   `json:"plex_account_token,omitempty" doc:"Plex: the plex.tv account token, for watchlist import alongside a server token"`
}

// HistoryImportCapability describes what a client may rely on when it submits
// an import run. Without it a client cannot tell a server that races
// plex_base_urls from one that accepts the field and ignores it: the request
// decoder drops members it does not know, so both answer the same way until
// the import fails.
type HistoryImportCapability struct {
	Capability
	PlexConnectionFallback bool `json:"plex_connection_fallback" doc:"Whether plex_base_urls is honored and the advertised connections are raced" example:"true"`
	MaxPlexConnections     int  `json:"max_plex_connections" doc:"How many Plex addresses one run races, counting plex_base_url" example:"8"`
}

// HistoryImportCapabilityOutput is the getHistoryImportCapability response.
type HistoryImportCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         HistoryImportCapability
}

// HistoryImportRunCreateInput is the createHistoryImportRun request.
type HistoryImportRunCreateInput struct {
	Body HistoryImportRunCreate
}

// EmbyConnectLogin is the loginEmbyConnect body.
type EmbyConnectLogin struct {
	Username string `json:"username" minLength:"1" doc:"Emby Connect user name or email" example:"alice@example.test"`
	Password string `json:"password" minLength:"1" doc:"Emby Connect password"`
}

// EmbyConnectLoginInput is the loginEmbyConnect request.
type EmbyConnectLoginInput struct {
	Body EmbyConnectLogin
}

// EmbyConnectServer is one server linked to an Emby Connect account.
type EmbyConnectServer struct {
	ServerID        string `json:"server_id" example:"a1b2c3"`
	Name            string `json:"name" example:"Home"`
	SystemID        string `json:"system_id,omitempty" doc:"The Emby server's own identifier when known"`
	HasRemoteURL    bool   `json:"has_remote_url" example:"true"`
	HasLocalAddress bool   `json:"has_local_address" example:"true"`
}

// EmbyConnectSession is the loginEmbyConnect result: a short-lived session
// that createHistoryImportRun consumes.
type EmbyConnectSession struct {
	ConnectSessionID string              `json:"connect_session_id" example:"9c3e1f0a-1b2c-4d5e-8f6a-7b8c9d0e1f2a"`
	Servers          []EmbyConnectServer `json:"servers" doc:"The account's servers; empty, never null"`
	ExpiresAt        Instant             `json:"expires_at" example:"2026-01-02T03:14:05.678Z"`
}

// EmbyConnectSessionOutput is the loginEmbyConnect response.
type EmbyConnectSessionOutput struct {
	Body EmbyConnectSession
}

// PlexPin is a plex.tv sign-in PIN the user completes in a browser.
type PlexPin struct {
	SessionID string  `json:"session_id" doc:"The session checkPlexPin polls" example:"4d5e6f7a-8b9c-4d0e-9f1a-2b3c4d5e6f7a"`
	PinCode   string  `json:"pin_code" example:"ABCD"`
	AuthURL   string  `json:"auth_url" doc:"Where the user signs in" example:"https://app.plex.tv/auth#?code=ABCD"`
	ExpiresAt Instant `json:"expires_at" example:"2026-01-02T03:19:05.678Z"`
}

// PlexPinOutput is the createPlexPin response.
type PlexPinOutput struct {
	Body PlexPin
}

// PlexPinCheck is the checkPlexPin body.
type PlexPinCheck struct {
	SessionID string `json:"session_id" minLength:"1" doc:"The session from createPlexPin" example:"4d5e6f7a-8b9c-4d0e-9f1a-2b3c4d5e6f7a"`
}

// PlexPinCheckInput is the checkPlexPin request.
type PlexPinCheckInput struct {
	Body PlexPinCheck
}

// PlexServer is one server the signed-in Plex account can reach.
type PlexServer struct {
	Name             string `json:"name" example:"Home"`
	ClientIdentifier string `json:"client_identifier" example:"abc123"`
	Owned            bool   `json:"owned" example:"true"`
	HasRemoteURL     bool   `json:"has_remote_url" example:"true"`
	HasLocalURL      bool   `json:"has_local_url" example:"true"`
}

// PlexPinStatus is the checkPlexPin result.
type PlexPinStatus struct {
	Authenticated bool         `json:"authenticated" doc:"Whether the user has completed the sign-in" example:"false"`
	Servers       []PlexServer `json:"servers" doc:"The account's servers once authenticated; empty, never null"`
}

// PlexPinStatusOutput is the checkPlexPin response.
type PlexPinStatusOutput struct {
	Body PlexPinStatus
}

// opListHistoryImportRuns is the operation id; the cursor scope is bound to it.
const opListHistoryImportRuns = "listHistoryImportRuns"

// historyImportPollSeconds is the Retry-After a queued run advertises.
const historyImportPollSeconds = 2
const historyImportCanceling = "canceling"

func registerHistoryImports(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	accountOp := func(op huma.Operation) Operation {
		registered := Operation{Operation: op, Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true}
		if op.Method == http.MethodPost {
			registered.DemoRestricted = true
			// Admission is durable, but submissions and upstream authentication
			// have no durable request identity and must not be replayed automatically.
			registered.RetrySafety = RetrySafetyNonRetryable
		}
		return registered
	}

	Register(reg, accountOp(humaOp(http.MethodGet, Prefix+"/history-imports/sources", "listHistoryImportSources", "history-imports",
		"List the configured servers a user may import watch history from.")), reg.listHistoryImportSources)

	Register(reg, accountOp(humaOp(http.MethodGet, Prefix+"/history-imports/runs", opListHistoryImportRuns, "history-imports",
		"List the account's import runs, newest first.")), func(ctx context.Context, in *HistoryImportRunListInput) (*HistoryImportRunCollectionOutput, error) {
		return reg.listHistoryImportRuns(ctx, cursors, in)
	})

	create := humaOp(http.MethodPost, Prefix+"/history-imports/runs", "createHistoryImportRun", "history-imports",
		"Start an import run for a profile; answers 202 with the queued run and its Location.")
	create.DefaultStatus = http.StatusAccepted
	create.Errors = []int{http.StatusConflict}
	Register(reg, accountOp(create), reg.createHistoryImportRun)

	get := accountOp(humaOp(http.MethodGet, Prefix+"/history-imports/runs/{id}", "getHistoryImportRun", "history-imports",
		"Read one of the account's import runs."))
	get.Conditional = true
	Register(reg, get, reg.getHistoryImportRun)

	Register(reg, accountOp(humaOp(http.MethodPost, Prefix+"/history-imports/plex/auth/pin", "createPlexPin", "history-imports",
		"Start a plex.tv sign-in: a PIN the user completes in a browser and a session to poll.")), reg.createPlexPin)

	Register(reg, accountOp(humaOp(http.MethodPost, Prefix+"/history-imports/plex/auth/check", "checkPlexPin", "history-imports",
		"Poll a plex.tv sign-in; once authenticated, lists the servers to import from.")), reg.checkPlexPin)

	Register(reg, accountOp(humaOp(http.MethodPost, Prefix+"/history-imports/emby-connect/login", "loginEmbyConnect", "history-imports",
		"Sign in to Emby Connect and list the account's servers in a short-lived session.")), reg.loginEmbyConnect)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/history-imports/capability", "getHistoryImportCapability", "history-imports",
			"Describe what an import run may rely on, including Plex connection fallback."),
		Class: ClassAuthenticated,
	}, reg.getHistoryImportCapability)
}

// getHistoryImportCapability answers from constants and the wiring check, so
// it stays usable on a deployment whose import service cannot reach anything.
func (reg *Registry) getHistoryImportCapability(_ context.Context, _ *CapabilityInput) (*HistoryImportCapabilityOutput, error) {
	return &HistoryImportCapabilityOutput{
		CacheControl: cachePrivateNoCache,
		Body: HistoryImportCapability{
			Capability:             Capability{State: configuredCapabilityState(reg.deps.HistoryImports != nil)},
			PlexConnectionFallback: true,
			MaxPlexConnections:     historyimport.MaxPlexConnectionCandidates,
		},
	}, nil
}

// historyImports is the wired service, or the fail-closed problem.
func (reg *Registry) historyImports() (HistoryImportService, *Problem) {
	if reg.deps.HistoryImports == nil {
		return nil, unavailable("history import")
	}
	return reg.deps.HistoryImports, nil
}

func (reg *Registry) listHistoryImportSources(ctx context.Context, _ *struct{}) (*HistoryImportSourceCollectionOutput, error) {
	svc, p := reg.historyImports()
	if p != nil {
		return nil, p
	}
	sources, err := svc.ListImportSources(ctx)
	if err != nil {
		return nil, historyImportProblem(err)
	}
	items := make([]HistoryImportSource, 0, len(sources))
	for _, s := range sources {
		items = append(items, historyImportSourceOf(s))
	}
	return &HistoryImportSourceCollectionOutput{Body: HistoryImportSourceCollection{Collection: NewCollection(items)}}, nil
}

func (reg *Registry) listHistoryImportRuns(ctx context.Context, cursors *Cursors, in *HistoryImportRunListInput) (*HistoryImportRunCollectionOutput, error) {
	svc, p := reg.historyImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	scope := CursorScope{
		OperationID: opListHistoryImportRuns,
		Security:    strconv.Itoa(userID),
		Sort:        "-created_at,-id",
		Tiebreaker:  "id",
	}
	var after *historyimport.RunKey
	if in.Cursor != "" {
		var pos runPosition
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		after = &historyimport.RunKey{CreatedAt: pos.CreatedAt, ID: pos.ID}
	}
	runs, hasMore, err := svc.ListImportRunsPage(ctx, userID, after, in.Limit)
	if err != nil {
		return nil, historyImportProblem(err)
	}
	next := ""
	if hasMore && len(runs) > 0 {
		last := runs[len(runs)-1]
		next, err = cursors.Encode(scope, runPosition{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	items := make([]HistoryImportRun, 0, len(runs))
	for i := range runs {
		items = append(items, historyImportRunOf(&runs[i]))
	}
	return &HistoryImportRunCollectionOutput{Body: HistoryImportRunCollection{Collection: Paginated(items, next)}}, nil
}

func (reg *Registry) createHistoryImportRun(ctx context.Context, in *HistoryImportRunCreateInput) (*HistoryImportRunAcceptedOutput, error) {
	svc, p := reg.historyImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	input, p := in.Body.toInput()
	if p != nil {
		return nil, p
	}
	run, err := svc.CreateImportRun(ctx, userID, input)
	if err != nil {
		return nil, historyImportProblem(err)
	}
	return &HistoryImportRunAcceptedOutput{
		Location:   Prefix + "/history-imports/runs/" + run.ID,
		RetryAfter: historyImportPollSeconds,
		Body:       historyImportRunOf(run),
	}, nil
}

func (reg *Registry) getHistoryImportRun(ctx context.Context, in *HistoryImportRunGetInput) (*HistoryImportRunOutput, error) {
	svc, p := reg.historyImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	run, err := svc.GetImportRun(ctx, userID, in.ID)
	if err != nil {
		return nil, historyImportProblem(err)
	}
	out := &HistoryImportRunOutput{Body: historyImportRunOf(run), Location: Prefix + "/history-imports/runs/" + run.ID}
	data, _ := json.Marshal(out.Body)
	sum := sha256.Sum256(data)
	tag := EntityTag{Opaque: hex.EncodeToString(sum[:])}
	out.ETag = tag.String()
	if !out.Body.Terminal {
		out.RetryAfter = strconv.Itoa(historyImportPollSeconds)
	}
	// Ownership has already been checked by GetImportRun, before any validator
	// can reveal whether another account's run exists.
	if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
		return nil, p
	} else if matched {
		return NotModified(out, tag), nil
	}
	return out, nil
}

func (reg *Registry) createPlexPin(ctx context.Context, _ *struct{}) (*PlexPinOutput, error) {
	svc, p := reg.historyImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	pin, err := svc.CreatePlexPin(ctx, userID)
	if err != nil {
		return nil, historyImportProblem(err)
	}
	return &PlexPinOutput{Body: PlexPin{SessionID: pin.SessionID, PinCode: pin.PinCode, AuthURL: pin.AuthURL, ExpiresAt: NewInstant(pin.ExpiresAt)}}, nil
}

func (reg *Registry) checkPlexPin(ctx context.Context, in *PlexPinCheckInput) (*PlexPinStatusOutput, error) {
	svc, p := reg.historyImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	result, err := svc.CheckPlexPin(ctx, userID, in.Body.SessionID)
	if err != nil {
		return nil, historyImportProblem(err)
	}
	servers := make([]PlexServer, 0, len(result.Servers))
	for _, s := range result.Servers {
		servers = append(servers, PlexServer{Name: s.Name, ClientIdentifier: s.ClientIdentifier, Owned: s.Owned, HasRemoteURL: s.HasRemoteURL, HasLocalURL: s.HasLocalURL})
	}
	return &PlexPinStatusOutput{Body: PlexPinStatus{Authenticated: result.Authenticated, Servers: servers}}, nil
}

func (reg *Registry) loginEmbyConnect(ctx context.Context, in *EmbyConnectLoginInput) (*EmbyConnectSessionOutput, error) {
	svc, p := reg.historyImports()
	if p != nil {
		return nil, p
	}
	userID, p := actingUserID(ctx)
	if p != nil {
		return nil, p
	}
	session, err := svc.LoginEmbyConnect(ctx, userID, historyimport.LoginConnectInput{Username: in.Body.Username, Password: in.Body.Password})
	if err != nil {
		return nil, historyImportProblem(err)
	}
	servers := make([]EmbyConnectServer, 0, len(session.Servers))
	for _, s := range session.Servers {
		servers = append(servers, EmbyConnectServer{ServerID: s.ServerID, Name: s.Name, SystemID: s.SystemID, HasRemoteURL: s.HasRemoteURL, HasLocalAddress: s.HasLocalAddress})
	}
	return &EmbyConnectSessionOutput{Body: EmbyConnectSession{ConnectSessionID: session.ConnectSessionID, Servers: servers, ExpiresAt: NewInstant(session.ExpiresAt)}}, nil
}

// toInput converts the wire body to the service input. source_id is an
// opaque id on the wire and an integer key in the store.
func (b HistoryImportRunCreate) toInput() (historyimport.CreateRunInput, *Problem) {
	in := historyimport.CreateRunInput{
		ProfileID:        string(b.ProfileID),
		Source:           b.Source,
		ConnectSessionID: b.ConnectSessionID,
		ServerID:         b.ServerID,
		Username:         b.Username,
		Password:         b.Password,
		JellyfinBaseURL:  b.JellyfinBaseURL,
		JellyfinUsername: b.JellyfinUsername,
		JellyfinPassword: b.JellyfinPassword,
		PlexSessionID:    b.PlexSessionID,
		PlexServerID:     b.PlexServerID,
		PlexBaseURL:      b.PlexBaseURL,
		PlexBaseURLs:     b.PlexBaseURLs,
		PlexToken:        b.PlexToken,
		PlexAccountToken: b.PlexAccountToken,
	}
	if b.SourceID != nil {
		n, err := intOfID(*b.SourceID)
		if err != nil || n <= 0 {
			return in, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + ".source_id", Code: codeInvalid, Detail: "expected a source identifier"})
		}
		in.SourceID = n
	}
	return in, nil
}

// historyImportProblem maps the v1 decision onto problem types. A rejected
// request member is a validation failure on the body (v1 answered 400); a
// source server that refused the credentials is a validation failure too,
// since the Silo session is fine; a source server that could not answer is
// the fail-closed problem with a retry hint; the rest follow the status.
func historyImportProblem(err error) *Problem {
	switch {
	case errors.Is(err, historyimport.ErrPersonalAdmissionUncertain):
		return NewProblem(TypeDependencyUnavailable, historyimport.ErrPersonalAdmissionUncertain.Error())
	case errors.Is(err, historyimport.ErrPersonalCredentialsUnavailable):
		return NewProblem(TypeDependencyUnavailable, "Personal imports are unavailable. No import was accepted.")
	case errors.Is(err, historyimport.ErrPersonalSessionChanged), errors.Is(err, historyimport.ErrConnectSessionUsed), errors.Is(err, historyimport.ErrPlexSessionUsed), errors.Is(err, historyimport.ErrRunConfigurationChanged), errors.Is(err, historyimport.ErrSourceDisabled):
		return NewProblem(TypeConflict, "The import source or login session changed. Review the configuration and authenticate again.")
	}
	apiErr, ok := errors.AsType[*handlers.APIError](err)
	if !ok {
		return serviceProblem(err)
	}
	switch {
	case handlers.IsHistoryImportUpstreamError(err) && apiErr.Status < 500:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: apiErr.Message})
	case handlers.IsHistoryImportUpstreamError(err):
		return NewProblem(TypeDependencyUnavailable, apiErr.Message).WithRetryAfter(30)
	case apiErr.Status == http.StatusBadRequest:
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: apiErr.Message})
	}
	return serviceProblem(err)
}

func historyImportSourceOf(s historyimport.Source) HistoryImportSource {
	return HistoryImportSource{
		ID:            IDFromInt(int64(s.ID)),
		Name:          s.Name,
		SourceType:    s.SourceType,
		BaseURL:       s.BaseURL,
		SystemID:      s.SystemID,
		Enabled:       s.Enabled,
		SortOrder:     s.SortOrder,
		HasAdminToken: s.HasAdminToken,
		CreatedAt:     NewInstant(s.CreatedAt),
		UpdatedAt:     NewInstant(s.UpdatedAt),
	}
}

func historyImportRunOf(run *historyimport.Run) HistoryImportRun {
	samples := make([]HistoryImportUnmatchedSample, 0, len(run.UnmatchedSamples))
	for _, s := range run.UnmatchedSamples {
		samples = append(samples, HistoryImportUnmatchedSample{Kind: s.Kind, Title: s.Title, Year: s.Year, Reason: s.Reason})
	}
	var mapping *ID
	if run.MappingID != nil {
		mapping = new(IDFromInt(int64(*run.MappingID)))
	}
	out := HistoryImportRun{
		Terminal:          run.Status == historyimport.RunStatusCompleted || run.Status == historyimport.RunStatusFailed || run.Status == historyimport.RunStatusCancelled,
		ID:                run.ID,
		UserID:            IDFromInt(int64(run.UserID)),
		ProfileID:         ID(run.ProfileID),
		SourceType:        run.SourceType,
		ConnectionMode:    run.ConnectionMode,
		Status:            run.Status,
		MappingID:         mapping,
		Fetched:           run.Fetched,
		Matched:           run.Matched,
		Unmatched:         run.Unmatched,
		ProgressUpdated:   run.ProgressUpdated,
		HistoryCreated:    run.HistoryCreated,
		WatchlistAdded:    run.WatchlistAdded,
		FavoritesImported: run.FavoritesImported,
		Skipped:           run.Skipped,
		UnmatchedSamples:  samples,
		ErrorMessage:      run.ErrorMessage,
		CreatedAt:         NewInstant(run.CreatedAt),
		StartedAt:         instantPtr(run.StartedAt),
		CompletedAt:       instantPtr(run.CompletedAt),
	}
	if run.CancelRequested && !out.Terminal {
		out.Status = historyImportCanceling
	}
	if run.Status == historyimport.RunStatusCancelled {
		out.ErrorMessage = ""
	}
	switch out.ErrorMessage {
	case "", historyimport.ErrRunConfigurationChanged.Error(), historyimport.LegacyDispatchUnavailableMessage, historyimport.StaleRunInterruptedMessage, historyimport.ErrPersonalCredentialsUnavailable.Error():
	default:
		out.ErrorMessage = "The import failed. Review the source configuration before starting a new run."
	}
	out.Warnings = make([]string, 0, len(run.Warnings))
	for range run.Warnings {
		out.Warnings = append(out.Warnings, "An import item could not be processed.")
	}
	for i := range out.UnmatchedSamples {
		out.UnmatchedSamples[i].Reason = "No matching catalog item was imported."
	}
	return out
}
