package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

type socketAPIKeyReader interface {
	GetMetadataByID(context.Context, int64) (*models.APIKeyMetadata, error)
}

// SocketAuthorityV1 applies the current login or API-key authority to the
// frozen bridge sockets. It retains no bearer token or API-key secret.
type SocketAuthorityV1 struct {
	login         EventsSocketValidator
	keys          socketAPIKeyReader
	users         access.UserRepository
	resolver      apimw.ViewerResolver
	actingAdmin   apimw.ActingAdminDecider
	checkInterval time.Duration
}

func NewSocketAuthorityV1(sessions eventsSessionValidator, users access.UserRepository, keys socketAPIKeyReader, resolver apimw.ViewerResolver, actingAdmin apimw.ActingAdminDecider) *SocketAuthorityV1 {
	return &SocketAuthorityV1{
		login: refuseRemotePlaybackSocket(newSocketAuthorityValidator(sessions, users, resolver, actingAdmin)),
		keys:  keys, users: users, resolver: resolver, actingAdmin: actingAdmin,
	}
}

func (a *SocketAuthorityV1) validate(ctx context.Context, opening *auth.Claims, identity evt.SocketIdentity) (context.Context, *auth.Claims, error) {
	if opening.TokenType != auth.TokenTypeAPIKey {
		if opening.TokenType != auth.TokenTypeAccess || a.login == nil {
			return ctx, nil, evt.ErrSocketTicket
		}
		return a.login(ctx, identity)
	}
	if a.keys == nil || a.users == nil || a.resolver == nil || a.actingAdmin == nil || opening.APIKeyID <= 0 {
		return ctx, nil, evt.ErrSocketTicket
	}
	key, err := a.keys.GetMetadataByID(ctx, opening.APIKeyID)
	// None of these v1 sockets is on the scoped-key allowlist. Read current
	// metadata, never the secret, and never let another owner inherit a socket.
	if err != nil || key == nil || key.UserID != opening.UserID || len(key.Scopes) != 0 {
		return ctx, nil, evt.ErrSocketTicket
	}
	user, err := a.users.GetByID(ctx, opening.UserID)
	if err != nil || user == nil || !user.Enabled || user.ID != opening.UserID || user.Role != opening.Role {
		return ctx, nil, evt.ErrSocketTicket
	}
	scope, err := a.resolver.Resolve(ctx, access.ResolveInput{
		UserID: opening.UserID, ProfileID: identity.ProfileID,
		ProfileToken: identity.ProfileToken, SkipPINVerification: true,
	})
	if err != nil || !scope.ProfileVerified {
		return ctx, nil, evt.ErrSocketTicket
	}
	current := *opening
	if current.Role == eventsAdminRole {
		acting, err := a.actingAdmin(ctx, apimw.ActingAdminRequest{Claims: &current, ProfileID: identity.ProfileID, DeviceID: identity.DeviceID})
		if err != nil {
			return ctx, nil, evt.ErrSocketTicket
		}
		if !acting {
			current.Role = models.RoleUser
		}
	}
	ctx = access.SetScope(ctx, scope)
	ctx = apimw.SetClaims(ctx, &current)
	ctx = apimw.SetProfileID(ctx, identity.ProfileID)
	return ctx, &current, nil
}

// bind bounds every connection, including standalone handlers. The router
// always supplies an authority validator; missing dependencies then refuse the
// upgrade. extra repeats the surface's ownership check.
func (a *SocketAuthorityV1) bind(r *http.Request, profileID, profileToken string, lifetime time.Duration, proofExpiry time.Time, expectedRole string, extra func(context.Context) error) (*http.Request, context.CancelFunc, error) {
	claims := apimw.GetClaims(r.Context())
	deadline := time.Now().Add(lifetime)
	if claims != nil && claims.ExpiresAt != nil && claims.ExpiresAt.Before(deadline) {
		deadline = claims.ExpiresAt.Time
	}
	if !proofExpiry.IsZero() && proofExpiry.Before(deadline) {
		deadline = proofExpiry
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	if a == nil {
		return r.WithContext(ctx), cancel, nil
	}
	if claims == nil || !deadline.After(time.Now()) {
		cancel()
		return r, func() {}, evt.ErrSocketTicket
	}
	opening := *claims
	identity := evt.SocketIdentity{
		UserID: opening.UserID, SessionID: opening.SessionID, Role: opening.Role,
		ProfileID: profileID, ProfileToken: profileToken,
		ImpersonatorUserID: opening.ImpersonatorUserID, DeviceID: r.Header.Get("X-Silo-Device-Id"),
	}
	if opening.ExpiresAt != nil {
		identity.AccessExpiresAt = opening.ExpiresAt.Time
	}
	check := func() (access.Scope, string, error) {
		checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		validated, current, err := a.validate(checkCtx, &opening, identity)
		if err != nil || current == nil {
			return access.Scope{}, "", evt.ErrSocketTicket
		}
		scope, ok := access.GetScope(validated)
		if !ok {
			return access.Scope{}, "", evt.ErrSocketTicket
		}
		if extra != nil {
			if err := extra(validated); err != nil {
				return access.Scope{}, "", err
			}
		}
		return scope, current.Role, nil
	}
	scope, role, err := check()
	// A fingerprint of fallback preferences would end the connection once the
	// read recovers, so admission refuses it as the v2 ticket mint does.
	if err != nil || scope.PreferencesDegraded || (expectedRole != "" && role != expectedRole) {
		cancel()
		return r, func() {}, evt.ErrSocketTicket
	}
	fingerprint := socketAccessFingerprintV1(scope)
	interval := a.checkInterval
	if interval <= 0 {
		interval = eventsSessionCheckInterval
	}
	go func() {
		for tick := time.Tick(interval); ; {
			select {
			case <-ctx.Done():
				return
			case <-tick:
				current, currentRole, err := check()
				// Fallback preferences say nothing about whether access
				// changed; that round skips the comparison, as on v2.
				changed := !current.PreferencesDegraded && socketAccessFingerprintV1(current) != fingerprint
				if err != nil || changed || (expectedRole != "" && currentRole != expectedRole) {
					cancel()
					return
				}
			}
		}
	}()
	return r.WithContext(access.SetScope(ctx, scope)), cancel, nil
}

func socketAccessFingerprintV1(scope access.Scope) string {
	// Presentation changes do not revoke authority. Keep library, maturity,
	// playback limits, PIN state and policy revision in the comparison.
	scope.PreferredMetadataLanguage = ""
	scope.MetadataLanguageOverrides = nil
	return eventsScopeFingerprint(scope)
}
