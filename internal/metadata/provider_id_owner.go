package metadata

import "context"

// ProviderIDOwnerLookup reports the content item (if any) that already owns a
// given set of durable provider IDs. *catalog.ProviderIDRepository satisfies
// it.
//
// Enrichment uses this before claiming an ID: sibling volumes of one series
// routinely resolve to the same provider work, and without the check they all
// claim the same ID and the collision is invisible afterwards. Shared here so
// the ebook, manga and audiobook enrichers depend on one contract instead of
// maintaining drifting copies.
type ProviderIDOwnerLookup interface {
	FindContentIDByProviderIDs(
		ctx context.Context,
		providerIDs map[string]string,
		itemType string,
		excludeContentID string,
	) (string, error)
}
