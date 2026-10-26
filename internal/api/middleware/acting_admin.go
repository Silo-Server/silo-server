package middleware

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ActingAdminRequest is what the acting-admin decision reads about one
// request: the caller, the profile it acts as ("" for none) and its device.
type ActingAdminRequest struct {
	Claims    *auth.Claims
	ProfileID string
	DeviceID  string
}

// ActingAdminDecider decides whether an admin-role caller may exercise admin
// powers as the profile it acts as: the account's primary profile, or no
// profile while the household does not require one. RequireActingAdmin
// enforces it on the admin route groups, and ActingAdmin answers it anywhere
// else. Callers check the admin role first.
type ActingAdminDecider func(ctx context.Context, in ActingAdminRequest) (bool, error)

// NewActingAdminDecider is the decision without the policy PDP (proxy and
// test wiring): actingAdminAllowed's rules over the given lookups.
func NewActingAdminDecider(checkPrimary PrimaryProfileChecker, requiresProfile HouseholdProfileRequirement) ActingAdminDecider {
	return func(ctx context.Context, in ActingAdminRequest) (bool, error) {
		return actingAdminAllowedFor(ctx, in.Claims, in.ProfileID, checkPrimary, requiresProfile)
	}
}

type actingAdminContextKey struct{}

type actingAdminSource struct {
	decide ActingAdminDecider
	// declaredProfileID and deviceID are the request's own headers, read
	// before any later middleware can change them.
	declaredProfileID string
	deviceID          string
}

// ProvideActingAdmin makes decide available to ActingAdmin for the rest of
// the request. The router mounts it ahead of every route with the decider the
// admin route gate uses, so a route outside the admin groups that grants
// admin powers answers the same question the gate does.
func ProvideActingAdmin(decide ActingAdminDecider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), actingAdminContextKey{}, &actingAdminSource{
				decide:            decide,
				declaredProfileID: r.Header.Get("X-Profile-Id"),
				deviceID:          policyDeviceID(r),
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SetActingAdminDecider installs decide in ctx as ProvideActingAdmin does,
// for handlers driven without the router.
func SetActingAdminDecider(ctx context.Context, decide ActingAdminDecider) context.Context {
	return context.WithValue(ctx, actingAdminContextKey{}, &actingAdminSource{decide: decide})
}

// ActingAdmin reports whether the request may exercise admin powers: the
// account holds the admin role and the acting-admin decision allows the
// profile the request acts as. The account role alone says nothing about the
// profile, and a child profile on an admin's account shares that role, so a
// route outside the admin groups that widens what an admin sees or may do
// must ask this rather than read claims.Role.
//
// A declared profile counts only once RequireViewerAccess has resolved and
// verified it in this request, so naming the primary profile without its PIN
// proof grants nothing. With no decider installed (ProvideActingAdmin) the
// answer is false. An error means the decision could not be made; callers
// fail closed.
func ActingAdmin(ctx context.Context) (bool, error) {
	profileID := GetProfileID(ctx)
	if profileID == "" {
		if source, _ := ctx.Value(actingAdminContextKey{}).(*actingAdminSource); source != nil {
			profileID = source.declaredProfileID
		}
	}
	if profileID != "" {
		scope, ok := access.GetScope(ctx)
		if !ok || scope.ProfileID != profileID || !scope.ProfileVerified {
			return false, nil
		}
	}
	return ActingAdminAs(ctx, profileID)
}

// ActingAdminAs is ActingAdmin for a profile the caller has already verified
// some other way, such as the profile a socket ticket was minted for behind
// the viewer gate; "" asks about acting with no profile.
func ActingAdminAs(ctx context.Context, profileID string) (bool, error) {
	claims := GetClaims(ctx)
	if claims == nil || claims.Role != models.RoleAdmin {
		return false, nil
	}
	source, _ := ctx.Value(actingAdminContextKey{}).(*actingAdminSource)
	if source == nil || source.decide == nil {
		return false, nil
	}
	allowed, err := source.decide(ctx, ActingAdminRequest{Claims: claims, ProfileID: profileID, DeviceID: source.deviceID})
	if err != nil {
		// A decider may report an allowance alongside its error (the legacy
		// one does when the household lookup fails); never pass it on.
		return false, err
	}
	return allowed, nil
}
