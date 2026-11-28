package watchsync

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/mdblist"
)

// The media types of MDBList's own vocabulary, which collection entries
// and the account lists carry.
const (
	mdblistMediaTypeMovie = "movie"
	mdblistMediaTypeShow  = "show"
)

// MDBListProfileLists reads MDBList lists for a personal collection with
// the owning profile's own MDBList connection, through the MDBList watch
// provider plugin. It never uses the server's MDBList API key: a profile
// reads only the private lists its own MDBList account can see.
type MDBListProfileLists struct {
	service *Service
}

// MDBListProfileLists returns the reader personal collection syncs use.
func (s *Service) MDBListProfileLists() *MDBListProfileLists {
	return &MDBListProfileLists{service: s}
}

// Check reports whether profile can read its MDBList lists: nil when it has
// an MDBList connection and the installed plugin reads lists,
// collectionutil.ErrMDBListNoConnection when only the connection is missing,
// and another error when the server can't read lists with it. A connection
// waiting out MDBList's daily limit still passes: reads then say so, rather
// than the editor hiding the profile's lists until the limit resets.
func (r *MDBListProfileLists) Check(ctx context.Context, userID int, profileID string) error {
	_, _, err := r.connected(ctx, userID, profileID)
	return err
}

// ListItems reads the list at listURL with the profile's MDBList
// connection, in MDBList's rank order, at most maxEntries entries
// (MDBListMaxEntries when maxEntries <= 0). A refusal is a
// *collectionutil.MDBListReadError, or one of the collectionutil
// ErrMDBListNo… errors when the profile can't read lists at all.
func (r *MDBListProfileLists) ListItems(ctx context.Context, userID int, profileID, listURL string, maxEntries int) ([]collectionutil.MDBListEntry, error) {
	if maxEntries <= 0 || maxEntries > collectionutil.MDBListMaxEntries {
		maxEntries = collectionutil.MDBListMaxEntries
	}
	provider, conn, err := r.connection(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	items, err := provider.ListCustomListItems(ctx, conn, listURL, maxEntries)
	if err != nil {
		return nil, r.readError(ctx, conn, err)
	}
	entries := make([]collectionutil.MDBListEntry, 0, len(items))
	for _, item := range items {
		entry := collectionutil.MDBListEntry{
			Rank:        item.Rank,
			IMDbID:      item.IMDbID,
			MediaType:   mdblistMediaTypeMovie,
			Title:       item.Title,
			ReleaseYear: item.Year,
		}
		if item.Kind == customListKindSeries {
			entry.MediaType = mdblistMediaTypeShow
		}
		entry.ID, _ = strconv.Atoi(item.TMDBID)
		if tvdb, err := strconv.Atoi(item.TVDBID); err == nil && tvdb > 0 {
			entry.TVDBID = &tvdb
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// AccountLists returns the lists of the profile's MDBList account: its own,
// private ones included, and the ones it liked. Lists whose URL is not an
// MDBList list page are left out, since a collection could not import them.
func (r *MDBListProfileLists) AccountLists(ctx context.Context, userID int, profileID string) ([]mdblist.AccountList, error) {
	provider, conn, err := r.connection(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	lists, err := provider.ListCustomLists(ctx, conn)
	if err != nil {
		return nil, r.readError(ctx, conn, err)
	}
	out := make([]mdblist.AccountList, 0, len(lists))
	for _, list := range lists {
		pageURL := strings.TrimSuffix(strings.TrimRight(list.URL, "/"), "/json")
		if collectionutil.ValidateMDBListURL(pageURL) != nil || strings.ContainsAny(pageURL, "?#") {
			continue
		}
		var mediaTypes []string
		for _, mediaType := range list.MediaTypes {
			switch mediaType {
			case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE:
				mediaTypes = append(mediaTypes, mdblistMediaTypeMovie)
			case pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES:
				mediaTypes = append(mediaTypes, mdblistMediaTypeShow)
			}
		}
		if len(mediaTypes) == 0 {
			mediaTypes = []string{mdblistMediaTypeMovie, mdblistMediaTypeShow}
		}
		out = append(out, mdblist.AccountList{
			Name: list.Name, URL: pageURL, MediaTypes: mediaTypes, Items: list.Items,
			Private: list.Private, Dynamic: list.Dynamic, Owned: list.Owned,
		})
	}
	return out, nil
}

// connection returns the MDBList provider and the profile's connection to
// it, or why lists can't be read with it now.
func (r *MDBListProfileLists) connection(ctx context.Context, userID int, profileID string) (customListProvider, Connection, error) {
	provider, conn, err := r.connected(ctx, userID, profileID)
	if err != nil {
		return nil, Connection{}, err
	}
	if until := conn.RateLimitedUntil; until != nil && until.After(r.service.now()) {
		return nil, Connection{}, &collectionutil.MDBListReadError{Kind: collectionutil.MDBListRateLimited, RetryAfter: until.Sub(r.service.now())}
	}
	return provider, conn, nil
}

// connected returns the MDBList provider and the profile's connection to it,
// or why the profile has none the server can read lists with.
func (r *MDBListProfileLists) connected(ctx context.Context, userID int, profileID string) (customListProvider, Connection, error) {
	registered, ok := r.service.registry.Get(mdblistProviderKey)
	if !ok {
		return nil, Connection{}, collectionutil.ErrMDBListNoProvider
	}
	provider, ok := registered.(customListProvider)
	if !ok || !provider.ProvidesCustomLists() {
		return nil, Connection{}, collectionutil.ErrMDBListProviderTooOld
	}
	conn, connected, err := r.service.repo.GetConnection(ctx, mdblistProviderKey, userID, profileID)
	if err != nil {
		return nil, Connection{}, err
	}
	if !connected || strings.TrimSpace(conn.AccessToken) == "" {
		return nil, Connection{}, collectionutil.ErrMDBListNoConnection
	}
	return provider, conn, nil
}

// readError maps a plugin failure onto the collection sync's error kinds. A
// rate limit defers the connection's own syncs too: the profile's key is
// shared with its watch sync. A rejected key is reported, not disconnected;
// the watch sync decides that on its own run.
func (r *MDBListProfileLists) readError(ctx context.Context, conn Connection, err error) error {
	if errors.Is(err, errCustomListsUnimplemented) {
		return collectionutil.ErrMDBListProviderTooOld
	}
	if rle, ok := AsRateLimited(err); ok {
		if deferErr := r.service.deferRateLimitedConnection(ctx, conn, rle); deferErr != nil {
			slog.WarnContext(ctx, "deferring rate-limited MDBList connection failed", "component", "watchsync",
				"connection_id", conn.ID, "error", deferErr)
		}
		return &collectionutil.MDBListReadError{Kind: collectionutil.MDBListRateLimited, RetryAfter: rle.RetryAfter}
	}
	var fault watchSyncProviderFaultError
	if errors.As(err, &fault) {
		switch fault.code {
		case pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL:
			return &collectionutil.MDBListReadError{Kind: collectionutil.MDBListKeyRejected, Reason: fault.message}
		case pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_PERMISSION_DENIED,
			pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_REQUEST:
			return &collectionutil.MDBListReadError{Kind: collectionutil.MDBListListUnreadable, Reason: fault.message}
		}
	}
	return err
}
