package metadata

import (
	"context"
	"fmt"
	"strings"
)

// A refresh that names no library still belongs to the libraries that hold the
// item: the scheduled refresh, the on-demand stale refresh a detail view
// starts, a trailer request's recovery and an admin's item refresh all run with
// folder 0. Folder 0 has no chain rows of its own, so ResolveChain would fall
// back to every enabled provider and pull a provider the item's library left
// out (issue #4605). These refreshes scope chain resolution to the item's
// libraries instead, the same chain a manual per-library refresh uses.
//
// The scope travels on the context because a refresh resolves chains in many
// places below Process and refreshSeriesChildTarget (item, season and episode
// levels, artwork, sidecars) and they all receive folder 0.

type itemChainScopeKey struct{}

// withItemChainScope scopes chain resolution under ctx to the libraries that
// hold contentID, for a refresh that names no library. An item in no library
// keeps the all-providers fallback. A named library (folderID > 0) is already
// its own scope and needs none.
func (s *MetadataService) withItemChainScope(ctx context.Context, contentID string, folderID int) (context.Context, error) {
	contentID = strings.TrimSpace(contentID)
	if folderID > 0 || contentID == "" || s == nil || s.libraryRepo == nil {
		return ctx, nil
	}
	folderIDs, err := s.libraryRepo.GetFolderIDsForItem(ctx, contentID)
	if err != nil {
		// Falling back to every provider here is the bug this scope fixes, so a
		// lookup failure fails the refresh and leaves it to retry.
		return ctx, fmt.Errorf("loading libraries for the provider chain: %w", err)
	}
	folderIDs, err = s.enabledChainScopeFolders(ctx, folderIDs)
	if err != nil {
		return ctx, err
	}
	// An empty scope still replaces an outer one: this item's libraries decide.
	return context.WithValue(ctx, itemChainScopeKey{}, folderIDs), nil
}

// enabledChainScopeFolders leaves a disabled library out of an item's scope
// when an enabled one also holds the item. A disabled library's own refresh is
// refused and the refresh queue only claims targets an enabled library holds,
// so its chain has no say in what an enabled library receives. An item held
// only by disabled or deleted libraries keeps them all rather than widening to
// every provider.
func (s *MetadataService) enabledChainScopeFolders(ctx context.Context, folderIDs []int) ([]int, error) {
	if len(folderIDs) < 2 || s.folderRepo == nil {
		return folderIDs, nil
	}
	folders, err := s.folderRepo.ListByIDs(ctx, folderIDs)
	if err != nil {
		return nil, fmt.Errorf("loading libraries for the provider chain: %w", err)
	}
	isEnabled := make(map[int]bool, len(folders))
	for _, folder := range folders {
		if folder != nil && folder.Enabled {
			isEnabled[folder.ID] = true
		}
	}
	// Keep the item's library order; ListByIDs sorts by display order.
	enabled := make([]int, 0, len(folders))
	for _, folderID := range folderIDs {
		if isEnabled[folderID] {
			enabled = append(enabled, folderID)
		}
	}
	if len(enabled) == 0 {
		return folderIDs, nil
	}
	return enabled, nil
}

func itemChainScopeFolders(ctx context.Context) []int {
	folderIDs, _ := ctx.Value(itemChainScopeKey{}).([]int)
	return folderIDs
}

// resolveLibrariesChain merges the chains of an item's libraries for one
// content level. Libraries are taken in ID order and each provider keeps its
// first position, so the oldest library's order leads and a provider only
// another library names still runs, as it would on that library's own refresh.
// The local tier stays ahead of every remote provider, as it does within one
// chain (see local_tier.go), even when only a later library enables it.
func (s *MetadataService) resolveLibrariesChain(ctx context.Context, folderIDs []int, contentLevel string) ([]Provider, error) {
	if len(folderIDs) == 1 {
		return s.resolveChainCached(ctx, folderIDs[0], contentLevel)
	}
	var local, remote []Provider
	seen := make(map[capabilityKey]bool)
	for _, folderID := range folderIDs {
		chain, err := s.resolveChainCached(ctx, folderID, contentLevel)
		if err != nil {
			return nil, err
		}
		for _, provider := range chain {
			key := chainProviderKey(provider)
			if seen[key] {
				continue
			}
			seen[key] = true
			// A resolved chain holds plugin providers and builtin (local)
			// ones, as providersFromCapabilities builds them.
			if _, isPlugin := provider.(*PluginProvider); isPlugin {
				remote = append(remote, provider)
			} else {
				local = append(local, provider)
			}
		}
	}
	return append(local, remote...), nil
}

// chainProviderKey identifies a provider across chains. Two plugin
// installations may share a capability ID, so a plugin is keyed by both.
func chainProviderKey(provider Provider) capabilityKey {
	if plugin, ok := provider.(*PluginProvider); ok {
		return capabilityKey{installationID: plugin.installationID, capabilityID: plugin.capabilityID}
	}
	return capabilityKey{capabilityID: provider.Slug()}
}
