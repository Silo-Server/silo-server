package middleware

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
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
)

// PermissionDecider is the narrow policy decision interface used by route
// gates. *policy.PDP satisfies it.
type PermissionDecider interface {
	CheckPermission(context.Context, policy.PermissionInput) (policy.PermissionDecision, policy.Meta, error)
}

// NewPolicyActingAdminMiddleware enforces the acting-admin gate through the
// policy PDP while preserving RequireActingAdmin's response contract.
func NewPolicyActingAdminMiddleware(pdp PermissionDecider, primaryChecker PrimaryProfileChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeUnauthorized(w, "Authentication required")
				return
			}

			if claims.Role != "admin" {
				writeForbidden(w, "Admin access required")
				return
			}

			declaredProfileID, actingAsPrimary, err := resolveActingAdminFacts(r, claims.UserID, primaryChecker)
			if err != nil {
				writeInternalError(w, activeProfileVerificationFailedMsg)
				return
			}
			if pdp == nil {
				writeInternalError(w, activeProfileVerificationFailedMsg)
				return
			}

			decision, _, err := pdp.CheckPermission(r.Context(), policy.PermissionInput{
				SchemaVersion:     1,
				UserID:            claims.UserID,
				Role:              claims.Role,
				UserEnabled:       true,
				Permission:        policy.PermissionActingAdmin,
				DeclaredProfileID: declaredProfileID,
				ActingAsPrimary:   actingAsPrimary,
				RequestTime:       policyRequestTime(),
				DeviceID:          policyDeviceID(r),
				ClientIP:          clientip.FromContext(r.Context()),
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
	pdp          PermissionDecider
	groups       access.GroupPolicyProvider
}

// NewPolicyPermissionMiddleware creates a PDP-backed permission middleware.
func NewPolicyPermissionMiddleware(
	users PermissionUserLoader,
	libraries MetadataTargetLibraryResolver,
	checkPrimary PrimaryProfileChecker,
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
			writeUnauthorized(w, "Authentication required")
			return
		}

		declaredProfileID, actingAsPrimary, err := resolveActingAdminFacts(r, claims.UserID, m.primaryChecker())
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, policyInternalErrorCode, activeProfileVerificationFailedMsg)
			return
		}
		if claims.Role == "admin" {
			actingAdmin, err := m.checkPermission(r, policy.PermissionInput{
				UserID:            claims.UserID,
				Role:              claims.Role,
				UserEnabled:       true,
				Permission:        policy.PermissionActingAdmin,
				DeclaredProfileID: declaredProfileID,
				ActingAsPrimary:   actingAsPrimary,
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
			writePermissionError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
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
			UserID:              user.ID,
			Role:                user.Role,
			UserEnabled:         user.Enabled,
			AssignedPermissions: slices.Clone(effective.Permissions),
			Permission:          policy.PermissionMetadataCuration,
			DeclaredProfileID:   declaredProfileID,
			ActingAsPrimary:     actingAsPrimary,
			TargetLibraryIDs:    []int{0},
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
			UserID:                  user.ID,
			Role:                    user.Role,
			UserEnabled:             user.Enabled,
			AssignedPermissions:     slices.Clone(effective.Permissions),
			Permission:              policy.PermissionMetadataCuration,
			DeclaredProfileID:       declaredProfileID,
			ActingAsPrimary:         actingAsPrimary,
			TargetLibraryIDs:        slices.Clone(targetLibraries),
			UserLibraryIDs:          slices.Clone(effective.LibraryIDs),
			UserLibrariesRestricted: effective.LibraryIDs != nil,
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
			writeUnauthorized(w, "Authentication required")
			return
		}
		if m == nil || m.users == nil || m.pdp == nil {
			writeForbidden(w, markerEditRequiredMsg)
			return
		}

		declaredProfileID, actingAsPrimary, err := resolveActingAdminFacts(r, claims.UserID, m.primaryChecker())
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
			UserID:              user.ID,
			Role:                user.Role,
			UserEnabled:         user.Enabled,
			AssignedPermissions: slices.Clone(effective.Permissions),
			Permission:          policy.PermissionMarkerEdit,
			DeclaredProfileID:   declaredProfileID,
			ActingAsPrimary:     actingAsPrimary,
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

func (m *PolicyPermissionMiddleware) primaryChecker() PrimaryProfileChecker {
	if m == nil {
		return nil
	}
	return m.checkPrimary
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

func resolveActingAdminFacts(r *http.Request, userID int, checkPrimary PrimaryProfileChecker) (string, bool, error) {
	profileID := declaredProfileID(r)
	if profileID == "" {
		return "", false, nil
	}
	if checkPrimary == nil {
		return profileID, true, nil
	}
	isPrimary, found, err := checkPrimary(r.Context(), userID, profileID)
	if err != nil {
		return profileID, false, err
	}
	return profileID, found && isPrimary, nil
}

func policyRequestTime() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func policyDeviceID(r *http.Request) string {
	return r.Header.Get(siloDeviceIDHeader)
}
