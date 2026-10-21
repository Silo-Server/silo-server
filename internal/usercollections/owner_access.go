package usercollections

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// ScopeResolver resolves a profile's access scope. Both the policy-backed
// viewer resolver and the legacy access.Resolver satisfy it.
type ScopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

// OwnerAccess resolves the content access of a personal collection's owner
// (catalog.PersonalCollectionAccess), so another profile on the login sees
// only the collection's titles the owner can access too. Each call resolves
// afresh; nothing is cached across requests.
type OwnerAccess struct {
	resolver ScopeResolver
}

var _ catalog.PersonalCollectionAccess = (*OwnerAccess)(nil)

// NewOwnerAccess builds owner access over the resolver the request gates use.
func NewOwnerAccess(resolver ScopeResolver) *OwnerAccess {
	return &OwnerAccess{resolver: resolver}
}

// OwnerFilter answers the owner profile's allowed libraries and maturity
// limits. The owner is not the one asking, so its PIN does not apply, and its
// hidden libraries are left out: they are a browsing preference, not access.
func (o *OwnerAccess) OwnerFilter(ctx context.Context, userID int, ownerProfileID string) (catalog.AccessFilter, error) {
	if o == nil || o.resolver == nil {
		return catalog.AccessFilter{}, errors.New("personal collection owner access is not configured")
	}
	scope, err := o.resolver.Resolve(ctx, access.ResolveInput{
		UserID:              userID,
		ProfileID:           ownerProfileID,
		SkipPINVerification: true,
		ContentAccessOnly:   true,
	})
	if err != nil {
		return catalog.AccessFilter{}, fmt.Errorf("resolving collection owner %s: %w", ownerProfileID, err)
	}
	return catalog.AccessFilter{
		AllowedLibraryIDs: scope.AllowedLibraryIDs,
		MaturityLimits:    scope.MaturityLimits,
	}, nil
}
