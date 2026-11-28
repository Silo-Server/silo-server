package jellycompat

import (
	"context"
	"maps"
	"slices"
)

func normalizeContentIDs(contentIDs []string) []string {
	if len(contentIDs) == 0 {
		return nil
	}

	result := make([]string, 0, len(contentIDs))
	seen := make(map[string]struct{}, len(contentIDs))
	for _, contentID := range contentIDs {
		if contentID == "" {
			continue
		}
		if _, ok := seen[contentID]; ok {
			continue
		}
		seen[contentID] = struct{}{}
		result = append(result, contentID)
	}
	return result
}

func resolveFavoritesForContentIDs(ctx context.Context, session *Session, userData UserDataService, contentIDs []string) (map[string]bool, error) {
	if userData == nil {
		return map[string]bool{}, nil
	}

	normalized := normalizeContentIDs(contentIDs)
	if len(normalized) == 0 {
		return map[string]bool{}, nil
	}

	favorites := make(map[string]bool, len(normalized))
	for chunk := range slices.Chunk(normalized, userStateLookupChunk) {
		found, err := userData.ListFavoritesByMediaItems(ctx, session, chunk)
		if err != nil {
			return nil, err
		}
		maps.Copy(favorites, found)
	}
	return favorites, nil
}

// userStateLookupChunk bounds the IDs one favorites or progress lookup names.
// A browse without Limit can list a whole library, and the SQLite user store
// binds one parameter per ID, which SQLite caps.
const userStateLookupChunk = 1000

func resolveProgressForContentIDs(ctx context.Context, session *Session, userData UserDataService, contentIDs []string) (map[string]*upstreamProgress, error) {
	if userData == nil {
		return map[string]*upstreamProgress{}, nil
	}

	normalized := normalizeContentIDs(contentIDs)
	if len(normalized) == 0 {
		return map[string]*upstreamProgress{}, nil
	}

	progress := make(map[string]*upstreamProgress, len(normalized))
	for chunk := range slices.Chunk(normalized, userStateLookupChunk) {
		found, err := userData.ListProgressByMediaItems(ctx, session, chunk)
		if err != nil {
			return nil, err
		}
		maps.Copy(progress, found)
	}
	return progress, nil
}

func resolveUserStateForContentIDs(ctx context.Context, session *Session, userData UserDataService, contentIDs []string) (map[string]bool, map[string]*upstreamProgress, error) {
	favorites, err := resolveFavoritesForContentIDs(ctx, session, userData, contentIDs)
	if err != nil {
		return nil, nil, err
	}
	progress, err := resolveProgressForContentIDs(ctx, session, userData, contentIDs)
	if err != nil {
		return nil, nil, err
	}
	return favorites, progress, nil
}
