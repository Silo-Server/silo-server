package watchsync

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// maxCustomListPages bounds one list read, so a plugin that keeps
	// answering with new page tokens can't hold a sync forever.
	maxCustomListPages = 100
	// maxCustomListItems bounds the items one read keeps before ranking: two
	// lists (a movie and a show twin) at the collection sync's own ceiling.
	maxCustomListItems = 20000
	// maxCustomLists bounds the lists one account listing keeps.
	maxCustomLists = 1000
)

// The kinds a CustomListItem carries.
const (
	customListKindMovie  = "movie"
	customListKindSeries = "series"
)

// errCustomListsUnimplemented means the plugin does not serve the custom
// list service, whatever its descriptor said.
var errCustomListsUnimplemented = errors.New("watch sync plugin does not serve custom lists")

// CustomList is one list of the connected provider account: its own or one
// it follows or liked.
type CustomList struct {
	URL        string
	Name       string
	MediaTypes []pluginv1.WatchSyncMediaType
	Items      int
	Private    bool
	Dynamic    bool
	Owned      bool
}

// CustomListItem is one movie or series of a provider list.
type CustomListItem struct {
	// Kind is "movie" or "series".
	Kind   string
	Title  string
	Year   int
	IMDbID string
	TMDBID string
	TVDBID string
	Rank   int
}

// customListProvider is a provider whose plugin reads the connected
// account's own lists, private ones included, with the connection's
// credentials.
type customListProvider interface {
	ProvidesCustomLists() bool
	ListCustomLists(ctx context.Context, conn Connection) ([]CustomList, error)
	ListCustomListItems(ctx context.Context, conn Connection, listURL string, maxItems int) ([]CustomListItem, error)
}

// ProvidesCustomLists reports whether the plugin's descriptor sets
// provides_custom_lists. A plugin built before it reads as false.
func (p *PluginProvider) ProvidesCustomLists() bool {
	return p.descriptor.GetProvidesCustomLists()
}

// ListCustomLists returns the lists of the connection's account.
func (p *PluginProvider) ListCustomLists(ctx context.Context, conn Connection) ([]CustomList, error) {
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, watchSyncUnavailableError()
	}
	conn, err = p.latestCredentials(ctx, conn)
	if err != nil {
		return nil, err
	}
	authContext, err := p.authenticatedContext(ctx, conn)
	if err != nil {
		return nil, err
	}
	response, err := client.ListCustomLists(ctx, &pluginv1.WatchSyncListCustomListsRequest{Context: authContext})
	if err != nil {
		return nil, customListRPCError(err)
	}
	if response.GetUpdatedCredentials() != nil {
		conn, err = p.persistUpdatedCredentials(ctx, conn, response.GetUpdatedCredentials())
		if err != nil {
			return nil, err
		}
	}
	if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken); err != nil {
		return nil, err
	}
	lists := make([]CustomList, 0, min(len(response.GetLists()), maxCustomLists))
	for _, list := range response.GetLists() {
		listURL := strings.TrimSpace(list.GetUrl())
		if listURL == "" {
			continue
		}
		if len(lists) == maxCustomLists {
			break
		}
		lists = append(lists, CustomList{
			URL:        listURL,
			Name:       normalizeWatchSyncText(list.GetName()),
			MediaTypes: slices.Clone(list.GetMediaTypes()),
			Items:      max(0, int(list.GetItemCount())),
			Private:    list.GetPrivate(),
			Dynamic:    list.GetDynamic(),
			Owned:      list.GetOwned(),
		})
	}
	return lists, nil
}

// ListCustomListItems reads every page of the list at listURL with the
// connection's credentials and returns its movies and series in the list's
// rank order, at most maxItems of them when maxItems > 0.
func (p *PluginProvider) ListCustomListItems(ctx context.Context, conn Connection, listURL string, maxItems int) ([]CustomListItem, error) {
	client, err := p.resolveClient(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return nil, watchSyncUnavailableError()
	}
	conn, err = p.latestCredentials(ctx, conn)
	if err != nil {
		return nil, err
	}
	var items []CustomListItem
	pageToken := ""
	seenPageTokens := make(map[string]struct{})
	for page := 0; ; page++ {
		if page == maxCustomListPages {
			return nil, errors.New("watch sync plugin returned too many custom list pages")
		}
		authContext, err := p.authenticatedContext(ctx, conn)
		if err != nil {
			return nil, err
		}
		response, err := client.ListCustomListItems(ctx, &pluginv1.WatchSyncListCustomListItemsRequest{
			Context:   authContext,
			ListUrl:   listURL,
			PageToken: pageToken,
			MaxItems:  int32(max(0, maxItems)), //nolint:gosec // bounded by the collection sync's entry limit
		})
		if err != nil {
			return nil, customListRPCError(err)
		}
		if response.GetUpdatedCredentials() != nil {
			conn, err = p.persistUpdatedCredentials(ctx, conn, response.GetUpdatedCredentials())
			if err != nil {
				return nil, err
			}
		}
		if err := watchSyncFaultError(p.Key(), response.GetFault(), conn.AccessToken, conn.RefreshToken); err != nil {
			return nil, err
		}
		for _, item := range response.GetItems() {
			if mapped, ok := customListItemFromProto(item); ok {
				items = append(items, mapped)
			}
		}
		if len(items) > maxCustomListItems {
			return nil, errors.New("watch sync plugin exceeded the custom list item limit")
		}
		next := response.GetNextPageToken()
		if next == "" {
			break
		}
		if _, repeated := seenPageTokens[next]; repeated {
			return nil, errors.New("watch sync plugin repeated a custom list page token")
		}
		seenPageTokens[next] = struct{}{}
		pageToken = next
	}
	// Twins (a movie list and a show list read as one) each rank from 1, so
	// the merged order interleaves them by rank.
	slices.SortStableFunc(items, func(a, b CustomListItem) int { return cmp.Compare(a.Rank, b.Rank) })
	if maxItems > 0 && len(items) > maxItems {
		items = items[:maxItems]
	}
	return items, nil
}

func customListItemFromProto(item *pluginv1.WatchSyncCustomListItem) (CustomListItem, bool) {
	media := item.GetMedia()
	var kind string
	switch media.GetMediaType() {
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE:
		kind = customListKindMovie
	case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES:
		kind = customListKindSeries
	default:
		return CustomListItem{}, false
	}
	ids := media.GetExternalIds()
	mapped := CustomListItem{
		Kind:   kind,
		Title:  normalizeWatchSyncText(media.GetTitle()),
		Year:   int(media.GetYear()),
		IMDbID: strings.TrimSpace(ids["imdb"]),
		TMDBID: numericWatchID(ids["tmdb"]),
		TVDBID: numericWatchID(ids["tvdb"]),
		Rank:   int(item.GetRank()),
	}
	if mapped.IMDbID == "" && mapped.TMDBID == "" && mapped.TVDBID == "" {
		return CustomListItem{}, false
	}
	return mapped, true
}

// numericWatchID returns id when it is a positive integer, else "".
func numericWatchID(id string) string {
	id = strings.TrimSpace(id)
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
		return ""
	}
	return id
}

func customListRPCError(err error) error {
	if status.Code(err) == codes.Unimplemented {
		return errCustomListsUnimplemented
	}
	return watchSyncRPCError()
}
