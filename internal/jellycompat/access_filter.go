package jellycompat

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ctxerr"
)

// ScopeResolver resolves a viewer request into an effective access scope.
// Implemented by *access.Resolver.
type ScopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

// NewScopeAccessFilter returns an AccessFilterResolver backed by the shared
// access scope resolver, so account-level library restrictions
// (users.library_ids), profile library restrictions, user-disabled
// libraries, content-rating ceilings, and playback-quality ceilings all
// apply to the Jellyfin-compat API exactly as they do to the native API.
//
// PIN verification is skipped because compat login already verified the
// profile PIN (password#pin convention). Resolution failures fail closed:
// the viewer gets an empty library allowlist rather than full access.
func NewScopeAccessFilter(resolver ScopeResolver) AccessFilterResolver {
	resolver = rememberRequestScope(resolver)
	return func(ctx context.Context, userID int, profileID string) catalog.AccessFilter {
		scope, err := resolver.Resolve(ctx, compatScopeInput(userID, profileID))
		if err != nil {
			slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "jellycompat: access scope resolution failed; denying library access", "component", "jellycompat",
				"user_id", userID,
				"profile_id", profileID,
				"error", err,
			)
			return catalog.AccessFilter{
				AllowedLibraryIDs: []int{},
				UserID:            userID,
				ProfileID:         profileID,
			}
		}
		return catalog.AccessFilter{
			AllowedLibraryIDs:         scope.AllowedLibraryIDs,
			DisabledLibraryIDs:        scope.DisabledLibraryIDs,
			MaturityLimits:            scope.MaturityLimits,
			MaxPlaybackQuality:        scope.MaxPlaybackQuality,
			ProfilePreferredLanguage:  scope.PreferredMetadataLanguage,
			MetadataLanguageOverrides: scope.MetadataLanguageOverrides,
			UserID:                    userID,
			ProfileID:                 profileID,
		}
	}
}

// compatScopeInput is the resolve input of a compat viewer: the session's
// account and profile, with the PIN already checked at login.
func compatScopeInput(userID int, profileID string) access.ResolveInput {
	return access.ResolveInput{UserID: userID, ProfileID: profileID, SkipPINVerification: true}
}

type requestScopeKey struct{}

// requestScope holds the access scope of the viewer a compat request
// authenticated as, once resolved. Item detail, season, image, and playback
// lookups within one request each need the scope, and remembering it spares
// each of them its own Postgres reads and policy evaluation. The session
// middleware creates one for each request, so the next request resolves
// again: a revoked library or an edited policy applies from the next request
// on, on every replica.
type requestScope struct {
	input access.ResolveInput

	mu       sync.Mutex
	scope    access.Scope
	resolved bool
}

// withRequestScope prepares ctx to remember the access scope of session's
// viewer for the rest of the request.
func withRequestScope(ctx context.Context, session *Session) context.Context {
	if session == nil {
		return ctx
	}
	return context.WithValue(ctx, requestScopeKey{}, &requestScope{input: compatScopeInput(session.StreamAppUserID, session.ProfileID)})
}

// rememberRequestScope wraps resolver so that, within a request prepared by
// withRequestScope, the request viewer's own scope is resolved once. Other
// inputs, and contexts outside such a request, resolve on every call. A
// failure is not remembered, so the next call in the request tries again. A
// nil resolver stays nil.
func rememberRequestScope(resolver ScopeResolver) ScopeResolver {
	switch resolver.(type) {
	case nil, requestScopeResolver:
		return resolver
	}
	return requestScopeResolver{next: resolver}
}

type requestScopeResolver struct {
	next ScopeResolver
}

func (r requestScopeResolver) Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
	remembered, _ := ctx.Value(requestScopeKey{}).(*requestScope)
	if remembered == nil || remembered.input != input {
		return r.next.Resolve(ctx, input)
	}
	if scope, ok := remembered.load(); ok {
		return scope, nil
	}
	scope, err := r.next.Resolve(ctx, input)
	if err != nil {
		return scope, err
	}
	remembered.store(scope)
	return scope, nil
}

// load returns a copy of the remembered scope, if there is one. Callers own
// what they get back, so neither they nor the remembered scope can change the
// other's library lists.
func (s *requestScope) load() (access.Scope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneScope(s.scope), s.resolved
}

func (s *requestScope) store(scope access.Scope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.resolved {
		s.scope, s.resolved = cloneScope(scope), true
	}
}

// cloneScope copies the slices and map of scope. Nil stays nil and empty
// stays empty: a nil AllowedLibraryIDs grants every library, an empty one
// none.
func cloneScope(scope access.Scope) access.Scope {
	scope.AllowedLibraryIDs = slices.Clone(scope.AllowedLibraryIDs)
	scope.DisabledLibraryIDs = slices.Clone(scope.DisabledLibraryIDs)
	scope.HiddenLibraryIDs = slices.Clone(scope.HiddenLibraryIDs)
	scope.MetadataLanguageOverrides = maps.Clone(scope.MetadataLanguageOverrides)
	return scope
}
