package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/progresssync"
)

const (
	opProgressBootstrapCapabilities = "getProgressBootstrapCapabilities"
	opCreateProgressSnapshot        = "createProgressBootstrapSnapshot"
	opGetProgressSnapshot           = "getProgressBootstrapSnapshot"
	progressSnapshotPath            = Prefix + "/sync/progress/snapshots"
	progressReplacementMode         = "full_replace"
	progressBootstrapCacheControl   = "no-store"
)

type ProgressBootstrapService interface {
	CheckSnapshotVisibility(context.Context, progresssync.Actor, string) error
	Capabilities(context.Context, progresssync.Actor) (progresssync.Support, error)
	CreateSnapshot(context.Context, progresssync.Actor, string, int) (progresssync.Page, error)
	ReadSnapshot(context.Context, progresssync.Actor, progresssync.Position) (progresssync.Page, error)
}

type ProgressBootstrapHeaders struct {
	Authorization string `header:"Authorization"`
	ProfileToken  string `header:"X-Profile-Token"`
}
type ProgressBootstrapCapabilitiesInput struct {
	ProgressBootstrapHeaders
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type ProgressBootstrapCapabilities struct {
	Capability
	Mode                         string `json:"mode" enum:"full_replace"`
	Incremental                  bool   `json:"incremental"`
	InstallationID               string `json:"installation_id,omitempty"`
	Generation                   string `json:"generation,omitempty"`
	MaxPageSize                  int    `json:"max_page_size"`
	MaxSnapshotItems             int    `json:"max_snapshot_items"`
	MaxSnapshotBytes             int64  `json:"max_snapshot_bytes"`
	SnapshotTTLSeconds           int    `json:"snapshot_ttl_seconds"`
	MaxActiveSnapshotsPerAccount int    `json:"max_active_snapshots_per_account"`
}
type ProgressBootstrapCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ProgressBootstrapCapabilities
}
type ProgressSnapshotCreateInput struct {
	ProgressBootstrapHeaders
	Body struct {
		RequestID string `json:"request_id" format:"uuid"`
		Limit     int    `json:"limit,omitempty" minimum:"1" maximum:"200" default:"200"`
	}
}
type ProgressSnapshotReadInput struct {
	ProgressBootstrapHeaders
	SnapshotID string `path:"snapshot_id" format:"uuid"`
	Cursor     string `query:"cursor" required:"true" maxLength:"8192"`
}
type ProgressSnapshot struct {
	SnapshotID      string          `json:"snapshot_id"`
	InstallationID  string          `json:"installation_id"`
	AccountID       ID              `json:"account_id"`
	ProfileID       ID              `json:"profile_id"`
	Generation      string          `json:"generation"`
	Mode            string          `json:"mode" enum:"full_replace"`
	CapturedAt      Instant         `json:"captured_at"`
	ExpiresAt       Instant         `json:"expires_at"`
	ItemCount       int             `json:"item_count" minimum:"0"`
	Items           []ProgressEntry `json:"items"`
	Page            PageInfo        `json:"page"`
	Complete        bool            `json:"complete"`
	CompletionToken string          `json:"completion_token,omitempty" doc:"Receipt of this complete full replacement; never a continuation or incremental checkpoint"`
}
type ProgressSnapshotOutput struct {
	CacheControl string `header:"Cache-Control"`
	Location     string `header:"Location"`
	Body         ProgressSnapshot
}
type progressSnapshotToken struct {
	Position  progresssync.Position `json:"position"`
	ExpiresAt time.Time             `json:"expires_at"`
	Mode      string                `json:"mode"`
}

func bootstrapScope(operation string, userID int, profileID, snapshotID string) CursorScope {
	return CursorScope{OperationID: operation, Security: strconv.Itoa(userID) + "/" + profileID, Filter: snapshotID, Sort: progressReplacementMode, Tiebreaker: "ordinal"}
}
func registerProgressBootstrap(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/sync/progress/capabilities", opProgressBootstrapCapabilities, "progress", "Discover full-replacement progress bootstrap support for this profile."), Class: ClassProfileScoped, ServiceBacked: true}, reg.progressBootstrapCapabilities)
	create := humaOp(http.MethodPost, progressSnapshotPath, opCreateProgressSnapshot, "progress", "Capture an immutable full-replacement snapshot. Reuse request_id only for the same admission intent within the 24-hour metadata retention horizon.")
	create.DefaultStatus = http.StatusCreated
	create.Errors = []int{http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests, http.StatusServiceUnavailable}
	Register(reg, Operation{Operation: create, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyDomainIdentity}, func(ctx context.Context, in *ProgressSnapshotCreateInput) (*ProgressSnapshotOutput, error) {
		if reg.deps.ProgressBootstrap == nil {
			return nil, unavailable("progress bootstrap")
		}
		actor, p := reg.bootstrapActor(ctx, in.ProgressBootstrapHeaders, http.MethodPost, progressSnapshotPath)
		if p != nil {
			return nil, p
		}
		page, err := reg.deps.ProgressBootstrap.CreateSnapshot(ctx, actor, in.Body.RequestID, in.Body.Limit)
		if err != nil {
			return nil, bootstrapProblem(err)
		}
		return renderProgressSnapshot(cursors, page)
	})
	read := humaOp(http.MethodGet, progressSnapshotPath+"/{snapshot_id}", opGetProgressSnapshot, "progress", "Read the next immutable snapshot page using its signed continuation.")
	read.Errors = []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable}
	Register(reg, Operation{Operation: read, Class: ClassProfileScoped, ServiceBacked: true}, func(ctx context.Context, in *ProgressSnapshotReadInput) (*ProgressSnapshotOutput, error) {
		if reg.deps.ProgressBootstrap == nil {
			return nil, unavailable("progress bootstrap")
		}
		actor, p := reg.bootstrapActor(ctx, in.ProgressBootstrapHeaders, http.MethodGet, progressSnapshotPath+"/"+in.SnapshotID)
		if p != nil {
			return nil, p
		}
		if err := reg.deps.ProgressBootstrap.CheckSnapshotVisibility(ctx, actor, in.SnapshotID); err != nil {
			return nil, bootstrapProblem(err)
		}
		var token progressSnapshotToken
		if p = cursors.Decode(bootstrapScope(opGetProgressSnapshot, actor.Input.UserID, actor.Input.ProfileID, in.SnapshotID), in.Cursor, &token); p != nil {
			return nil, p
		}
		if token.Mode != progressReplacementMode || token.Position.SnapshotID != in.SnapshotID || token.Position.UserID != actor.Input.UserID || token.Position.ProfileID != actor.Input.ProfileID {
			return nil, NewProblem(TypeInvalidCursor, "The cursor belongs to a different snapshot.")
		}
		if token.ExpiresAt.IsZero() || !time.Now().Before(token.ExpiresAt) {
			return nil, NewProblem(TypeSyncResetRequired, "Start a new snapshot with a new request_id.")
		}
		page, err := reg.deps.ProgressBootstrap.ReadSnapshot(ctx, actor, token.Position)
		if err != nil {
			return nil, bootstrapProblem(err)
		}
		return renderProgressSnapshot(cursors, page)
	})
}
func (reg *Registry) bootstrapActor(ctx context.Context, headers ProgressBootstrapHeaders, method, path string) (progresssync.Actor, *Problem) {
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return progresssync.Actor{}, p
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return progresssync.Actor{}, NewProblem(TypeAuthenticationRequired, "Authentication required.")
	}
	captured := *claims
	input := access.ResolveInput{UserID: userID, ProfileID: profileID, SessionID: captured.SessionID, ProfileToken: headers.ProfileToken, SkipPINVerification: captured.TokenType == auth.TokenTypeAPIKey}
	actor := progresssync.Actor{Input: input, Recheck: func(ctx context.Context) (access.Scope, error) {
		if err := reg.deps.Auth.RevalidateCurrent(ctx, headers.Authorization, &captured, method, path); err != nil {
			return access.Scope{}, err
		}
		return reg.deps.ViewerAccess.ResolveCurrent(ctx, input)
	}}
	return actor, nil
}
func (reg *Registry) progressBootstrapCapabilities(ctx context.Context, in *ProgressBootstrapCapabilitiesInput) (*ProgressBootstrapCapabilitiesOutput, error) {
	actor, p := reg.bootstrapActor(ctx, in.ProgressBootstrapHeaders, http.MethodGet, Prefix+"/sync/progress/capabilities")
	if p != nil {
		return nil, p
	}
	body := ProgressBootstrapCapabilities{Capability: Capability{State: StateNotConfigured, Allowed: new(false)}, Mode: progressReplacementMode, MaxPageSize: progresssync.MaxPageSize, MaxSnapshotItems: progresssync.MaxSnapshotItems, MaxSnapshotBytes: progresssync.MaxSnapshotBytes, SnapshotTTLSeconds: int(progresssync.SnapshotTTL / time.Second), MaxActiveSnapshotsPerAccount: progresssync.MaxActiveSnapshots}
	if reg.deps.ProgressBootstrap != nil {
		support, err := reg.deps.ProgressBootstrap.Capabilities(ctx, actor)
		switch {
		case errors.Is(err, progresssync.ErrUnsupported):
			body.State = StateUnsupported
		case errors.Is(err, progresssync.ErrNotConfigured):
		case err != nil:
			return nil, bootstrapProblem(err)
		default:
			body.State = StateAvailable
			body.Allowed = new(true)
			body.InstallationID = support.InstallationID
			body.Generation = support.Generation
		}
	}
	return &ProgressBootstrapCapabilitiesOutput{CacheControl: progressBootstrapCacheControl, Body: body}, nil
}
func renderProgressSnapshot(cursors *Cursors, page progresssync.Page) (*ProgressSnapshotOutput, error) {
	s := page.Snapshot
	out := &ProgressSnapshotOutput{CacheControl: progressBootstrapCacheControl, Location: progressSnapshotPath + "/" + s.ID, Body: ProgressSnapshot{SnapshotID: s.ID, InstallationID: s.InstallationID, AccountID: ID(strconv.Itoa(s.UserID)), ProfileID: ID(s.ProfileID), Generation: s.Generation, Mode: progressReplacementMode, CapturedAt: Instant{Time: s.CapturedAt}, ExpiresAt: Instant{Time: s.ExpiresAt}, ItemCount: s.ItemCount, Items: make([]ProgressEntry, 0, len(page.Items)), Complete: page.Next == nil}}
	for _, entry := range page.Items {
		out.Body.Items = append(out.Body.Items, ProgressEntry{MediaItemID: ID(entry.MediaItemID), PositionSeconds: entry.PositionSeconds, DurationSeconds: entry.DurationSeconds, Completed: entry.Completed, UpdatedAt: Instant{Time: entry.UpdatedAt}})
	}
	position := progresssync.Position{SnapshotID: s.ID, InstallationID: s.InstallationID, Generation: s.Generation, AccessDigest: s.AccessDigest, Identity: s.Identity, PageSize: s.PageSize, After: s.ItemCount}
	operation := opGetProgressSnapshot + "/completion"
	if page.Next != nil {
		position = *page.Next
		operation = opGetProgressSnapshot
	}
	token, err := cursors.Encode(bootstrapScope(operation, s.UserID, s.ProfileID, s.ID), progressSnapshotToken{Position: position, ExpiresAt: s.ExpiresAt, Mode: progressReplacementMode})
	if err != nil {
		return nil, serviceProblem(err)
	}
	if page.Next != nil {
		out.Body.Page = PageInfo{HasMore: true, NextCursor: token}
	} else {
		out.Body.CompletionToken = token
	}
	return out, nil
}
func bootstrapProblem(err error) *Problem {
	switch {
	case errors.Is(err, progresssync.ErrUnsupported):
		return CapabilityProblem(StateUnsupported, "progress bootstrap")
	case errors.Is(err, progresssync.ErrNotConfigured):
		return NewProblem(TypeDependencyUnavailable, "Progress bootstrap is not configured.")
	case errors.Is(err, progresssync.ErrUnauthenticated), errors.Is(err, apimw.ErrCurrentCredentialInvalid):
		return NewProblem(TypeAuthenticationRequired, "Authentication is no longer valid.")
	case errors.Is(err, apimw.ErrCurrentCredentialForbidden):
		return NewProblem(TypePermissionDenied, "The credential no longer permits this operation.")
	case errors.Is(err, access.ErrProfileUnverified):
		return NewProblem(TypeProfileVerificationRequired, "Profile verification is required.")
	case errors.Is(err, access.ErrProfileNotFound), errors.Is(err, progresssync.ErrNotFound):
		return NewProblem(TypeNotFound, "Snapshot not found.")
	case errors.Is(err, progresssync.ErrInvalid):
		return NewProblem(TypeValidationFailed, "The snapshot input is invalid.")
	case errors.Is(err, progresssync.ErrResetRequired):
		return NewProblem(TypeSyncResetRequired, "Start a new snapshot with a new request_id.")
	case errors.Is(err, progresssync.ErrRequestConflict):
		return NewProblem(TypeSnapshotRequestConflict, "The request_id was used with different input.")
	case errors.Is(err, progresssync.ErrTooLarge):
		return NewProblem(TypeProgressSnapshotTooLarge, "The full snapshot exceeds the admission limits.")
	case errors.Is(err, progresssync.ErrQuota):
		return NewProblem(TypeRateLimited, "Too many active snapshots. Retry after a snapshot expires.").WithHeader("Retry-After", "30")
	default:
		return NewProblem(TypeDependencyUnavailable, "Progress bootstrap is temporarily unavailable.")
	}
}
