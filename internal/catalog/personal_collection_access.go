package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
)

// ErrPersonalCollectionOwnerAccess reports that the owner of another
// profile's personal collection could not be resolved, so the collection
// cannot be read: its members are never shown under the viewer's access alone.
var ErrPersonalCollectionOwnerAccess = errors.New("personal collection owner access is unavailable")

// PersonalCollectionAccess resolves the content access of a personal
// collection's owner: the libraries and maturity limits the owning profile
// on the same login may see. It is resolved on every read and never cached
// across requests, so an owner's access change applies on every node at once.
type PersonalCollectionAccess interface {
	OwnerFilter(ctx context.Context, userID int, ownerProfileID string) (AccessFilter, error)
}

// PersonalCollectionFilter is the filter viewerProfileID reads a personal
// collection owned by ownerProfileID with: the viewer's filter, further
// limited to what the owner can access (see IntersectAccess). The owner reads
// its own collections with its own filter and no lookup. Any failure to
// resolve another owner is ErrPersonalCollectionOwnerAccess; it never falls
// back to the viewer's filter.
func PersonalCollectionFilter(ctx context.Context, owners PersonalCollectionAccess, viewer AccessFilter, userID int, viewerProfileID, ownerProfileID string) (AccessFilter, error) {
	if strings.TrimSpace(ownerProfileID) == "" {
		return AccessFilter{}, fmt.Errorf("%w: the collection has no owner", ErrPersonalCollectionOwnerAccess)
	}
	if ownerProfileID == viewerProfileID {
		return viewer, nil
	}
	if owners == nil || userID <= 0 {
		return AccessFilter{}, ErrPersonalCollectionOwnerAccess
	}
	owner, err := owners.OwnerFilter(ctx, userID, ownerProfileID)
	if err != nil {
		return AccessFilter{}, fmt.Errorf("%w: %w", ErrPersonalCollectionOwnerAccess, err)
	}
	return IntersectAccess(viewer, owner), nil
}

// IntersectAccess limits a viewer's filter to what owner can access as
// well. Only content access combines: allowed libraries intersect (nil is
// unrestricted, a non-nil empty list denies everything), the stricter content
// rating ceiling and the smaller advisory-age limit apply, and either side
// requiring an advisory age hides unadvised titles. Everything else stays the
// viewer's: identity, the viewer's own hidden libraries, presentation,
// playback quality and excluded media types, and the server-wide unrated-title
// policy. The owner's hidden libraries are a browsing preference, not access,
// so they never limit what others see.
//
// Library lists intersect as lists, not per title: a title in two libraries,
// one allowed only to the viewer and the other only to the owner, is hidden
// although each profile can open it. That errs toward hiding; a per-title
// check would need a second library predicate on every catalog read path.
func IntersectAccess(viewer, owner AccessFilter) AccessFilter {
	out := viewer
	switch {
	case owner.AllowedLibraryIDs == nil:
	case viewer.AllowedLibraryIDs == nil:
		out.AllowedLibraryIDs = append([]int{}, owner.AllowedLibraryIDs...)
	default:
		out.AllowedLibraryIDs = intersectInts(viewer.AllowedLibraryIDs, owner.AllowedLibraryIDs)
		if out.AllowedLibraryIDs == nil {
			out.AllowedLibraryIDs = []int{}
		}
	}
	out.MaturityLimits = intersectMaturityLimits(viewer.MaturityLimits, owner.MaturityLimits)
	return out
}

func intersectMaturityLimits(viewer, owner access.MaturityLimits) access.MaturityLimits {
	out := access.MaturityLimits{
		MaxContentRating:    access.StricterCeiling(viewer.MaxContentRating, owner.MaxContentRating),
		AllowUnratedContent: viewer.AllowUnratedContent,
		MaxAdvisoryAge:      viewer.MaxAdvisoryAge,
	}
	if owner.MaxAdvisoryAge > 0 && (out.MaxAdvisoryAge == 0 || owner.MaxAdvisoryAge < out.MaxAdvisoryAge) {
		out.MaxAdvisoryAge = owner.MaxAdvisoryAge
	}
	out.RequireAdvisoryAge = viewer.HidesUnadvised() || owner.HidesUnadvised()
	return out
}
