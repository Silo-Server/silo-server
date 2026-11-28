package mdblist

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

const (
	// listItemsPageSize is the most items one /lists/{id}/items request asks
	// for, the largest page MDBList serves. Every page costs one request
	// against the key's daily quota.
	listItemsPageSize = 1000
	// maxResponseBytes bounds one API response body, as the feed reader does.
	maxResponseBytes = 4 << 20
	// maxErrorBodyBytes bounds the body read for an error answer.
	maxErrorBodyBytes = 64 << 10

	// The media types AccountList reports, as MDBList names them.
	mediaTypeMovie = "movie"
	mediaTypeShow  = "show"
)

// AccountList is a list of the account that owns the API key: one of its
// own, private ones included, or one it liked. A movie list and a show list
// that share a name and slug ("twins") share a URL, so they are one entry.
type AccountList struct {
	Name string
	// URL is the list's mdblist.com page, what a synced collection stores.
	URL string
	// MediaTypes holds "movie", "show", or both.
	MediaTypes []string
	Items      int
	Private    bool
	Dynamic    bool
	// Owned is false for a list the account liked.
	Owned bool
}

// accountListJSON is one entry of /lists/user or /lists/user/liked.
type accountListJSON struct {
	ID        flexInt `json:"id"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	UserName  string  `json:"user_name"`
	MediaType *string `json:"mediatype"`
	Items     flexInt `json:"items"`
	Type      string  `json:"type"`
	Dynamic   bool    `json:"dynamic"`
	Private   bool    `json:"private"`
}

// listItemJSON is one movie or show of a /lists/{id}/items page.
type listItemJSON struct {
	ID          flexInt `json:"id"`
	Rank        flexInt `json:"rank"`
	Title       string  `json:"title"`
	IMDbID      string  `json:"imdb_id"`
	TVDBID      flexInt `json:"tvdb_id"`
	ReleaseYear flexInt `json:"release_year"`
	IDs         struct {
		IMDb string  `json:"imdb"`
		TMDB flexInt `json:"tmdb"`
		TVDB flexInt `json:"tvdb"`
	} `json:"ids"`
}

type listItemsPage struct {
	Movies     []listItemJSON `json:"movies"`
	Shows      []listItemJSON `json:"shows"`
	Pagination *struct {
		NextCursor string `json:"next_cursor"`
		HasMore    *bool  `json:"has_more"`
	} `json:"pagination"`
}

// ListItems reads a list through the MDBList API with the configured key, so
// a private list the key's account owns can be read. A list named by user
// and slug is first resolved to its IDs: a movie + show pair has two. Each
// is read page by page, up to maxEntries items each (MDBListMaxEntries when
// maxEntries <= 0), and the entries are merged in MDBList's rank order and
// cut to maxEntries. A refusal from MDBList is a
// *collectionutil.MDBListReadError.
func (c *Client) ListItems(ctx context.Context, ref collectionutil.MDBListRef, maxEntries int) ([]collectionutil.MDBListEntry, error) {
	key := c.currentAPIKey()
	if key == "" {
		return nil, ErrNotConfigured
	}
	if maxEntries <= 0 || maxEntries > collectionutil.MDBListMaxEntries {
		maxEntries = collectionutil.MDBListMaxEntries
	}
	ids := []int64{ref.ID}
	if ref.ID <= 0 {
		resolved, err := c.resolveList(ctx, key, ref)
		if err != nil {
			return nil, err
		}
		ids = resolved
	}
	var entries []collectionutil.MDBListEntry
	for _, id := range ids {
		items, err := c.listItemsByID(ctx, key, id, maxEntries)
		if err != nil {
			return nil, err
		}
		entries = append(entries, items...)
	}
	slices.SortStableFunc(entries, func(a, b collectionutil.MDBListEntry) int { return cmp.Compare(a.Rank, b.Rank) })
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}
	return entries, nil
}

// resolveList returns the IDs of the list ref names by user and slug.
func (c *Client) resolveList(ctx context.Context, key string, ref collectionutil.MDBListRef) ([]int64, error) {
	path := "/lists/" + url.PathEscape(ref.User) + "/" + url.PathEscape(ref.Slug)
	var lists []accountListJSON
	if err := c.getJSON(ctx, key, path, nil, &lists); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(lists))
	for _, list := range lists {
		if list.ID > 0 && !slices.Contains(ids, int64(list.ID)) {
			ids = append(ids, int64(list.ID))
		}
	}
	if len(ids) == 0 {
		return nil, &collectionutil.MDBListReadError{Kind: collectionutil.MDBListListUnreadable}
	}
	return ids, nil
}

// listItemsByID reads up to maxEntries items of one list. It follows
// next_cursor when MDBList sends one, and otherwise pages by offset until
// has_more (or the X-Has-More header) says the list ended, or a page comes
// back empty or short.
func (c *Client) listItemsByID(ctx context.Context, key string, id int64, maxEntries int) ([]collectionutil.MDBListEntry, error) {
	path := "/lists/" + strconv.FormatInt(id, 10) + "/items"
	var entries []collectionutil.MDBListEntry
	cursor := ""
	// Every page asks for the same size: MDBList doesn't say whether a
	// cursor holds when the size changes, so the last page is trimmed below
	// instead of asked for short.
	limit := min(listItemsPageSize, maxEntries)
	for len(entries) < maxEntries {
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		} else if len(entries) > 0 {
			q.Set("offset", strconv.Itoa(len(entries)))
		}
		var page listItemsPage
		header, err := c.getJSONHeader(ctx, key, path, q, &page)
		if err != nil {
			return nil, err
		}
		fetched := len(page.Movies) + len(page.Shows)
		for _, item := range page.Movies {
			entries = append(entries, item.entry(mediaTypeMovie))
		}
		for _, item := range page.Shows {
			entries = append(entries, item.entry(mediaTypeShow))
		}
		if fetched == 0 {
			break
		}
		if next := pageCursor(page); next != "" {
			if next == cursor {
				return nil, errors.New("mdblist returned the same page cursor twice")
			}
			cursor = next
			continue
		}
		cursor = ""
		if !pageHasMore(page, header, fetched, limit) {
			break
		}
	}
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}
	return entries, nil
}

func pageCursor(page listItemsPage) string {
	if page.Pagination == nil {
		return ""
	}
	return strings.TrimSpace(page.Pagination.NextCursor)
}

func pageHasMore(page listItemsPage, header http.Header, fetched, limit int) bool {
	if page.Pagination != nil && page.Pagination.HasMore != nil {
		return *page.Pagination.HasMore
	}
	if more, err := strconv.ParseBool(strings.TrimSpace(header.Get("X-Has-More"))); err == nil {
		return more
	}
	return fetched >= limit
}

// entry maps an API list item onto the feed's entry shape. The array the
// item came in sets its media type; the ids block wins over the top-level
// fields, which mirror it on older answers.
func (i listItemJSON) entry(mediaType string) collectionutil.MDBListEntry {
	entry := collectionutil.MDBListEntry{
		ID:          int(i.IDs.TMDB),
		Rank:        int(i.Rank),
		IMDbID:      strings.TrimSpace(i.IDs.IMDb),
		MediaType:   mediaType,
		Title:       i.Title,
		ReleaseYear: int(i.ReleaseYear),
	}
	if entry.ID <= 0 {
		entry.ID = int(i.ID)
	}
	if entry.IMDbID == "" {
		entry.IMDbID = strings.TrimSpace(i.IMDbID)
	}
	tvdb := int(i.IDs.TVDB)
	if tvdb <= 0 {
		tvdb = int(i.TVDBID)
	}
	if tvdb > 0 {
		entry.TVDBID = &tvdb
	}
	return entry
}

// AccountLists returns the lists of the account that owns the configured
// key: its own (private ones included), then the ones it liked, without
// duplicates. External lists are left out; they sync through the feed.
func (c *Client) AccountLists(ctx context.Context) ([]AccountList, error) {
	key := c.currentAPIKey()
	if key == "" {
		return nil, ErrNotConfigured
	}
	var own, liked []accountListJSON
	if err := c.getJSON(ctx, key, "/lists/user", nil, &own); err != nil {
		return nil, err
	}
	if err := c.getJSON(ctx, key, "/lists/user/liked", nil, &liked); err != nil {
		return nil, err
	}
	username := ""
	if slices.ContainsFunc(own, func(l accountListJSON) bool { return strings.TrimSpace(l.UserName) == "" }) {
		var user struct {
			Username string `json:"username"`
		}
		if err := c.getJSON(ctx, key, "/user", nil, &user); err != nil {
			return nil, err
		}
		username = strings.TrimSpace(user.Username)
	}

	var lists []AccountList
	byURL := make(map[string]int)
	seenIDs := make(map[int64]bool)
	add := func(l accountListJSON, owned bool) {
		if l.ID <= 0 || seenIDs[int64(l.ID)] || strings.EqualFold(l.Type, "external") {
			return
		}
		seenIDs[int64(l.ID)] = true
		user := strings.TrimSpace(l.UserName)
		if user == "" && owned {
			user = username
		}
		listURL := canonicalListURL(user, strings.TrimSpace(l.Slug))
		if listURL == "" {
			return
		}
		mediaTypes := []string{mediaTypeMovie, mediaTypeShow}
		if l.MediaType != nil {
			switch strings.ToLower(strings.TrimSpace(*l.MediaType)) {
			case mediaTypeMovie:
				mediaTypes = []string{mediaTypeMovie}
			case mediaTypeShow:
				mediaTypes = []string{mediaTypeShow}
			}
		}
		if i, ok := byURL[listURL]; ok {
			twin := &lists[i]
			for _, mediaType := range mediaTypes {
				if !slices.Contains(twin.MediaTypes, mediaType) {
					twin.MediaTypes = append(twin.MediaTypes, mediaType)
				}
			}
			slices.Sort(twin.MediaTypes)
			twin.Items += int(l.Items)
			twin.Private = twin.Private || l.Private
			twin.Dynamic = twin.Dynamic || l.Dynamic
			return
		}
		byURL[listURL] = len(lists)
		lists = append(lists, AccountList{
			Name:       strings.TrimSpace(l.Name),
			URL:        listURL,
			MediaTypes: mediaTypes,
			Items:      int(l.Items),
			Private:    l.Private,
			Dynamic:    l.Dynamic,
			Owned:      owned,
		})
	}
	for _, l := range own {
		add(l, true)
	}
	for _, l := range liked {
		add(l, false)
	}
	return lists, nil
}

func (c *Client) getJSON(ctx context.Context, key, path string, q url.Values, out any) error {
	_, err := c.getJSONHeader(ctx, key, path, q, out)
	return err
}

// getJSONHeader reads one API answer into out and returns its headers. It
// sends key, the one key a whole read uses even if the setting changes
// meanwhile, and keeps it out of every error. A refusal is a
// *collectionutil.MDBListReadError.
func (c *Client) getJSONHeader(ctx context.Context, key, path string, q url.Values, out any) (http.Header, error) {
	query := url.Values{}
	for name, values := range q {
		query[name] = values
	}
	query.Set("apikey", key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating mdblist request: %w", sanitizeAPIKeyError(err, key))
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.apiHTTP().Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling mdblist: %w", sanitizeAPIKeyError(err, key))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBodyBytes))
		return nil, statusError(res, body, key)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading mdblist response: %w", sanitizeAPIKeyError(err, key))
	}
	// MDBList answers some refusals with status 200 and an error object,
	// which would otherwise decode as an empty page or user.
	if reason := errorReason(body, key); reason != "" {
		return nil, refusal(reason)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("parsing mdblist response: %w", err)
	}
	return res.Header, nil
}

// statusError classifies a non-2xx answer. 401 is a rejected key; 403 and
// 404 are a list the key can't read, unless MDBList's reason blames the key;
// 429 is the daily limit.
func statusError(res *http.Response, body []byte, key string) error {
	reason := errorReason(body, key)
	switch res.StatusCode {
	case http.StatusUnauthorized:
		return &collectionutil.MDBListReadError{Kind: collectionutil.MDBListKeyRejected, Reason: reason}
	case http.StatusForbidden, http.StatusNotFound:
		return refusal(reason)
	case http.StatusTooManyRequests:
		return &collectionutil.MDBListReadError{Kind: collectionutil.MDBListRateLimited, Reason: reason, RetryAfter: retryAfter(res.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("mdblist request failed with status %d", res.StatusCode)
	}
}

// errorReason returns MDBList's {"error": "..."} text, bounded, with secret
// assignments and the key masked; "" when body is not one.
func errorReason(body []byte, key string) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return ""
	}
	return redactAPIKey(collectionutil.MDBListBoundedReason(body), key)
}

// refusal is a refusal of the list, or of the key when MDBList's reason
// blames it.
func refusal(reason string) error {
	lower := strings.ToLower(reason)
	if strings.Contains(lower, "api key") || strings.Contains(lower, "apikey") {
		return &collectionutil.MDBListReadError{Kind: collectionutil.MDBListKeyRejected, Reason: reason}
	}
	return &collectionutil.MDBListReadError{Kind: collectionutil.MDBListListUnreadable, Reason: reason}
}

func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// apiHTTP is the client's HTTP client with redirects held to the API's own
// scheme and host, so an answer from MDBList can't send the request, and
// the key in its query, anywhere else.
func (c *Client) apiHTTP() *http.Client {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return c.http
	}
	clone := *c.http
	parent := c.http.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != base.Scheme || !strings.EqualFold(req.URL.Host, base.Host) {
			return errors.New("mdblist redirected to another host")
		}
		if parent != nil {
			return parent(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &clone
}

// flexInt decodes an integer MDBList sends as a number, a numeric string, or
// null.
type flexInt int64

// Anything else, such as an empty string, reads as 0 rather than failing
// the whole page.
func (n *flexInt) UnmarshalJSON(data []byte) error {
	*n = 0
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		*n = flexInt(value)
	}
	return nil
}
