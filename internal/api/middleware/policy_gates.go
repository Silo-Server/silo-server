package middleware

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/go-chi/chi/v5"
)

const (
	siloDeviceIDHeader                 = "X-Silo-Device-Id"
	policyInternalErrorCode            = "internal_error"
	activeProfileVerificationFailedMsg = "Failed to verify active profile"
	metadataCurationRequiredMsg        = "Metadata curation permission required"
	markerEditRequiredMsg              = "Marker editing permission required"
	itemIDRequiredMsg                  = "Item ID is required"
)

// PermissionDecider is the narrow policy decision interface used by route
// gates. *policy.PDP satisfies it.
type PermissionDecider interface {
	CheckPermission(context.Context, policy.PermissionInput) (policy.PermissionDecision, policy.Meta, error)
}

// NewPolicyActingAdminMiddleware enforces the acting-admin gate through the
// policy PDP while preserving RequireActingAdmin's response contract.
func NewPolicyActingAdminMiddleware(pdp PermissionDecider, primaryChecker PrimaryProfileChecker, household HouseholdProfileRequirement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeUnauthorized(w, "Authentication required", ReasonAuthenticationRequired)
				return
			}

			if claims.Role != "admin" {
				writeForbidden(w, "Admin access required")
				return
			}

			facts, err := resolveActingAdminFacts(r, claims, primaryChecker, household)
			if err != nil {
				writeInternalError(w, activeProfileVerificationFailedMsg)
				return
			}
			if pdp == nil {
				writeInternalError(w, activeProfileVerificationFailedMsg)
				return
			}

			decision, _, err := pdp.CheckPermission(r.Context(), policy.PermissionInput{
				SchemaVersion:            1,
				UserID:                   claims.UserID,
				Role:                     claims.Role,
				UserEnabled:              true,
				Permission:               policy.PermissionActingAdmin,
				DeclaredProfileID:        facts.declaredProfileID,
				ActingAsPrimary:          facts.actingAsPrimary,
				HouseholdRequiresProfile: facts.householdRequiresProfile,
				RequestTime:              policyRequestTime(),
				DeviceID:                 policyDeviceID(r),
				ClientIP:                 clientip.FromContext(r.Context()),
			})
			if err != nil {
				writeInternalError(w, activeProfileVerificationFailedMsg)
				return
			}
			if !decision.Allowed {
				writeForbidden(w, "Admin access requires the account's primary profile")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// PolicyPermissionMiddleware mirrors PermissionMiddleware with authorization
// decisions evaluated by the policy PDP.
type PolicyPermissionMiddleware struct {
	users        PermissionUserLoader
	libraries    MetadataTargetLibraryResolver
	checkPrimary PrimaryProfileChecker
	household    HouseholdProfileRequirement
	pdp          PermissionDecider
	groups       access.GroupPolicyProvider
}

// NewPolicyPermissionMiddleware creates a PDP-backed permission middleware.
func NewPolicyPermissionMiddleware(
	users PermissionUserLoader,
	libraries MetadataTargetLibraryResolver,
	checkPrimary PrimaryProfileChecker,
	household HouseholdProfileRequirement,
	pdp PermissionDecider,
	groups ...access.GroupPolicyProvider,
) *PolicyPermissionMiddleware {
	var groupProvider access.GroupPolicyProvider
	if len(groups) > 0 {
		groupProvider = groups[0]
	}
	return &PolicyPermissionMiddleware{
		users:        users,
		libraries:    libraries,
		checkPrimary: checkPrimary,
		household:    household,
		pdp:          pdp,
		groups:       groupProvider,
	}
}

// RequireMetadataCurationForItem allows acting admins or users with
// metadata_curation permission when every library containing the target item is
// within the user's assigned libraries. It preserves the legacy middleware's
// lookup ordering and outward error mapping.
func (m *PolicyPermissionMiddleware) RequireMetadataCurationForItem(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			writeUnauthorized(w, "Authentication required", ReasonAuthenticationRequired)
			return
		}

		facts, err := m.actingAdminFacts(r, claims)
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, activeProfileVerificationFailedMsg)
			return
		}
		if claims.Role == "admin" {
			actingAdmin, err := m.checkPermission(r, policy.PermissionInput{
				UserID:                   claims.UserID,
				Role:                     claims.Role,
				UserEnabled:              true,
				Permission:               policy.PermissionActingAdmin,
				DeclaredProfileID:        facts.declaredProfileID,
				ActingAsPrimary:          facts.actingAsPrimary,
				HouseholdRequiresProfile: facts.householdRequiresProfile,
			})
			if err != nil {
				writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, activeProfileVerificationFailedMsg)
				return
			}
			if actingAdmin.Allowed {
				next.ServeHTTP(w, r)
				return
			}
		}
		if m == nil || m.users == nil || m.libraries == nil || m.pdp == nil {
			writeForbidden(w, metadataCurationRequiredMsg)
			return
		}

		contentID := chi.URLParam(r, "id")
		if contentID == "" {
			writePermissionErrorReason(w, http.StatusBadRequest, "bad_request", itemIDRequiredMsg, ReasonItemIDRequired)
			return
		}

		user, err := m.users.GetByID(r.Context(), claims.UserID)
		if err != nil || user == nil || !user.Enabled {
			writeForbidden(w, metadataCurationRequiredMsg)
			return
		}
		effective, err := access.EffectivePolicyForUser(r.Context(), user, m.groups)
		if err != nil {
			writeForbidden(w, metadataCurationRequiredMsg)
			return
		}

		permissionOnlyInput := policy.PermissionInput{
			UserID:                   user.ID,
			Role:                     user.Role,
			UserEnabled:              user.Enabled,
			AssignedPermissions:      slices.Clone(effective.Permissions),
			Permission:               policy.PermissionMetadataCuration,
			DeclaredProfileID:        facts.declaredProfileID,
			ActingAsPrimary:          facts.actingAsPrimary,
			HouseholdRequiresProfile: facts.householdRequiresProfile,
			TargetLibraryIDs:         []int{0},
		}
		permissionOnly, err := m.checkPermission(r, permissionOnlyInput)
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, "Failed to verify metadata curation permission")
			return
		}
		if !permissionOnly.Allowed {
			writeForbidden(w, metadataCurationRequiredMsg)
			return
		}

		targetLibraries, err := m.libraries.ResolveMetadataTargetLibraryIDs(r.Context(), contentID)
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, "Failed to resolve item libraries")
			return
		}
		if len(targetLibraries) == 0 {
			writePermissionError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}

		decision, err := m.checkPermission(r, policy.PermissionInput{
			UserID:                   user.ID,
			Role:                     user.Role,
			UserEnabled:              user.Enabled,
			AssignedPermissions:      slices.Clone(effective.Permissions),
			Permission:               policy.PermissionMetadataCuration,
			DeclaredProfileID:        facts.declaredProfileID,
			ActingAsPrimary:          facts.actingAsPrimary,
			HouseholdRequiresProfile: facts.householdRequiresProfile,
			TargetLibraryIDs:         slices.Clone(targetLibraries),
			UserLibraryIDs:           slices.Clone(effective.LibraryIDs),
			UserLibrariesRestricted:  effective.LibraryIDs != nil,
		})
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, "Failed to verify metadata curation permission")
			return
		}
		if !decision.Allowed {
			if decision.ReasonCode == policy.ReasonCodeItemOutsideUserLibraries {
				writeForbidden(w, "Item is outside your assigned libraries")
				return
			}
			writeForbidden(w, metadataCurationRequiredMsg)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RequireMarkerEdit gates manual marker writes through the policy PDP. Unlike
// the legacy handler check, admins are not short-circuited: the decision runs
// through the policy so group masks and custom overrides can tighten it.
func (m *PolicyPermissionMiddleware) RequireMarkerEdit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			writeUnauthorized(w, "Authentication required", ReasonAuthenticationRequired)
			return
		}
		if m == nil || m.users == nil || m.pdp == nil {
			writeForbidden(w, markerEditRequiredMsg)
			return
		}

		// marker_edit reads no household fact (its admin grant is not an
		// acting-admin decision, #1911), so skip that lookup.
		facts, err := resolveActingAdminFacts(r, claims, m.checkPrimary, nil)
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, activeProfileVerificationFailedMsg)
			return
		}
		user, err := m.users.GetByID(r.Context(), claims.UserID)
		if err != nil || user == nil || !user.Enabled {
			writeForbidden(w, markerEditRequiredMsg)
			return
		}
		effective, err := access.EffectivePolicyForUser(r.Context(), user, m.groups)
		if err != nil {
			writeForbidden(w, markerEditRequiredMsg)
			return
		}

		decision, err := m.checkPermission(r, policy.PermissionInput{
			UserID:                   user.ID,
			Role:                     user.Role,
			UserEnabled:              user.Enabled,
			AssignedPermissions:      slices.Clone(effective.Permissions),
			Permission:               policy.PermissionMarkerEdit,
			DeclaredProfileID:        facts.declaredProfileID,
			ActingAsPrimary:          facts.actingAsPrimary,
			HouseholdRequiresProfile: facts.householdRequiresProfile,
		})
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, "Failed to verify marker edit permission")
			return
		}
		if !decision.Allowed {
			writeForbidden(w, markerEditRequiredMsg)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (m *PolicyPermissionMiddleware) actingAdminFacts(r *http.Request, claims *auth.Claims) (actingAdminFacts, error) {
	if m == nil {
		return resolveActingAdminFacts(r, claims, nil, nil)
	}
	return resolveActingAdminFacts(r, claims, m.checkPrimary, m.household)
}

func (m *PolicyPermissionMiddleware) checkPermission(r *http.Request, input policy.PermissionInput) (policy.PermissionDecision, error) {
	if m == nil || m.pdp == nil {
		return policy.PermissionDecision{}, errMissingPolicyDecider{}
	}
	input.SchemaVersion = 1
	input.RequestTime = policyRequestTime()
	input.DeviceID = policyDeviceID(r)
	input.ClientIP = clientip.FromContext(r.Context())
	decision, _, err := m.pdp.CheckPermission(r.Context(), input)
	return decision, err
}

type errMissingPolicyDecider struct{}

func (errMissingPolicyDecider) Error() string {
	return "missing policy permission decider"
}

// actingAdminFacts are the Go-side lookups the acting-admin policy rule reads:
// Rego never touches the database.
type actingAdminFacts struct {
	declaredProfileID        string
	actingAsPrimary          bool
	householdRequiresProfile bool
}

// resolveActingAdminFacts precomputes the acting-admin inputs. With a declared
// profile it reports whether that profile is the account's primary. Without
// one it reports, for an admin login session, whether the household requires
// a declared profile (profileLessAdminRefused); API keys and non-admins never
// pay that lookup.
func resolveActingAdminFacts(r *http.Request, claims *auth.Claims, checkPrimary PrimaryProfileChecker, household HouseholdProfileRequirement) (actingAdminFacts, error) {
	profileID := declaredProfileID(r)
	if profileID == "" {
		if claims == nil || claims.Role != "admin" {
			return actingAdminFacts{}, nil
		}
		refused, err := profileLessAdminRefused(r.Context(), claims, household)
		return actingAdminFacts{householdRequiresProfile: refused}, err
	}
	if checkPrimary == nil {
		return actingAdminFacts{declaredProfileID: profileID, actingAsPrimary: true}, nil
	}
	isPrimary, found, err := checkPrimary(r.Context(), claims.UserID, profileID)
	if err != nil {
		return actingAdminFacts{declaredProfileID: profileID}, err
	}
	return actingAdminFacts{declaredProfileID: profileID, actingAsPrimary: found && isPrimary}, nil
}

func policyRequestTime() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func policyDeviceID(r *http.Request) string {
	return r.Header.Get(siloDeviceIDHeader)
}
