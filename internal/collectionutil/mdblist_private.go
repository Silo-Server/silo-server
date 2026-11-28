package collectionutil

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MDBListEntry is one title of an MDBList list, in the shape of the public
// /json feed. The MDBList API reader maps its list items onto it, so a sync
// matches feed and API entries the same way.
type MDBListEntry struct {
	// ID is the title's TMDB ID.
	ID          int    `json:"id"`
	Rank        int    `json:"rank"`
	TVDBID      *int   `json:"tvdbid"`
	IMDbID      string `json:"imdb_id"`
	MediaType   string `json:"mediatype"`
	Title       string `json:"title"`
	ReleaseYear int    `json:"release_year"`
}

// ItemType maps the entry's media type onto Silo's "movie" and "series" item
// types.
func (e MDBListEntry) ItemType() string {
	switch strings.ToLower(e.MediaType) {
	case "show", "tv", "series":
		return "series"
	default:
		return "movie"
	}
}

// MDBListFeedError is the {"error": "..."} object the public /json feed
// answers with status 200 in place of a list, such as "This List is Private".
// Message is MDBList's text, bounded and with secret assignments masked.
type MDBListFeedError struct{ Message string }

func (e *MDBListFeedError) Error() string { return "mdblist: " + e.Message }

// MDBListRef names a list for the MDBList API: by its owner's username and
// its slug, or by its numeric ID.
type MDBListRef struct {
	User string
	Slug string
	ID   int64
}

// ParseMDBListRef reads the list a stored MDBList URL names. It accepts the
// list page or its /json feed form: /lists/{user}/{slug} names a list by
// name and /lists/{id} by ID. Any other path, such as an external list's
// /lists/{user}/external/{id}, names no list the API reads by URL.
func ParseMDBListRef(rawURL string) (MDBListRef, bool) {
	canonical, err := CanonicalMDBListURL(rawURL)
	if err != nil {
		return MDBListRef{}, false
	}
	parsed, err := url.Parse(canonical)
	if err != nil {
		return MDBListRef{}, false
	}
	path := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/lists/"), "/json")
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			return MDBListRef{}, false
		}
	}
	switch len(segments) {
	case 1:
		id, err := strconv.ParseInt(segments[0], 10, 64)
		if err != nil || id <= 0 {
			return MDBListRef{}, false
		}
		return MDBListRef{ID: id}, true
	case 2:
		// The API serves a user's set of lists at /lists/user/{name}, so no
		// list is read by name under the user "user".
		if strings.EqualFold(segments[0], "user") {
			return MDBListRef{}, false
		}
		return MDBListRef{User: segments[0], Slug: segments[1]}, true
	default:
		return MDBListRef{}, false
	}
}

// URL returns the mdblist.com page of the list r names.
func (r MDBListRef) URL() string {
	if r.ID > 0 {
		return "https://mdblist.com/lists/" + strconv.FormatInt(r.ID, 10)
	}
	return "https://mdblist.com/lists/" + url.PathEscape(r.User) + "/" + url.PathEscape(r.Slug)
}

// Why a profile's own MDBList connection can't read a list for a personal
// collection, before MDBList is asked.
var (
	// ErrMDBListNoConnection means the profile has not connected MDBList.
	ErrMDBListNoConnection = errors.New("this profile has no MDBList connection")
	// ErrMDBListNoProvider means the server has no MDBList watch provider.
	ErrMDBListNoProvider = errors.New("the MDBList watch provider is not installed")
	// ErrMDBListProviderTooOld means the installed MDBList watch provider
	// plugin predates reading lists for collections.
	ErrMDBListProviderTooOld = errors.New("the MDBList watch provider plugin can't read lists for collections")
)

// MDBListReadErrorKind classifies an MDBList API refusal to read a list.
type MDBListReadErrorKind int

const (
	// MDBListKeyRejected means MDBList did not accept the API key.
	MDBListKeyRejected MDBListReadErrorKind = iota + 1
	// MDBListListUnreadable means the key works but MDBList returned no list
	// it can read at that address: a private list of another account, or
	// none at all.
	MDBListListUnreadable
	// MDBListRateLimited means the key's daily API limit is used up.
	MDBListRateLimited
)

// MDBListReadError is an MDBList API refusal to read a list with a key.
type MDBListReadError struct {
	Kind MDBListReadErrorKind
	// Reason is MDBList's own short explanation, bounded and with secrets
	// masked like MDBListFeedError.Message; empty when it gave none.
	Reason string
	// RetryAfter is how long MDBList asked a rate-limited key to wait, when
	// it said.
	RetryAfter time.Duration
}

func (e *MDBListReadError) Error() string {
	switch e.Kind {
	case MDBListKeyRejected:
		return "mdblist rejected the api key"
	case MDBListRateLimited:
		return "mdblist rate limit exceeded"
	default:
		if e.Reason != "" {
			return "mdblist: " + e.Reason
		}
		return "mdblist returned no list it can read at this address"
	}
}

// MDBListBoundedReason returns MDBList's error text the way MDBListFeedError
// stores it: trimmed, secret assignments masked, and cut to a bounded
// length. It is for readers outside this package that map MDBList's
// {"error": "..."} answers onto MDBListReadError.Reason.
func MDBListBoundedReason(body []byte) string {
	return mdblistFeedError(body)
}

// MDBListPrivateRead reads a list the public feed refused. feedErr is the
// feed's refusal, for the message to show when the list can't be read
// another way. It returns the entries, or the error the sync stores.
type MDBListPrivateRead func(ref MDBListRef, feedErr *MDBListFeedError) ([]MDBListEntry, error)

// FetchMDBListEntries reads a list's public feed, trying each candidate URL
// in order as FetchMDBListWithFallback does. When every candidate fails and
// the feed refused one of them with its error object (as it does for a
// private list), it reads that candidate's list with private instead and
// returns that answer. A nil private, or a refused candidate that names no
// list, keeps the last candidate's error. Transport and HTTP failures never
// reach private: the API would fail the same way, at a cost to the key's
// daily quota.
func FetchMDBListEntries(ctx context.Context, client *http.Client, urls []string, maxEntries int, private MDBListPrivateRead) ([]MDBListEntry, error) {
	var (
		lastErr error
		refused *MDBListFeedError
		ref     MDBListRef
	)
	for _, listURL := range urls {
		entries, err := FetchMDBListJSON[MDBListEntry](ctx, client, listURL, maxEntries)
		if err == nil {
			return entries, nil
		}
		lastErr = err
		var feedErr *MDBListFeedError
		if refused == nil && errors.As(err, &feedErr) {
			if parsed, ok := ParseMDBListRef(listURL); ok {
				refused, ref = feedErr, parsed
			}
		}
	}
	if refused == nil || private == nil {
		return nil, lastErr
	}
	return private(ref, refused)
}
