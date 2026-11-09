package historyimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

const (
	jellyfinPageSize    = 200
	jellyfinIDChunkSize = 100
	// jellyfinItemFields requests only the optional field the importer reads;
	// episode numbers, series IDs, runtime, and user data are default DTO
	// members. MediaSources and Path would make every page much heavier.
	jellyfinItemFields = "ProviderIds"
	// Jellyfin favorites cover more kinds than Silo can store: seasons have no
	// favorite in Silo, so only movies, shows, and episodes are requested.
	jellyfinPlayableItemTypes = "Movie,Episode"
	jellyfinFavoriteItemTypes = "Movie,Series,Episode"
	jellyfinVideoMediaType    = "Video"
)

type JellyfinClient struct {
	httpClient *http.Client
	// listingClient reads listing pages. Jellyfin evaluates a filter such as
	// IsPlayed across the whole library for every page, so one page from a
	// large library can take well over the 30 seconds that sign-in and user
	// lookups are allowed, and a single slow page would fail the whole import.
	listingClient *http.Client
	limiter       *upstreamRateLimiter
}

func NewJellyfinClient() *JellyfinClient {
	return &JellyfinClient{
		httpClient:    netguard.NewClient(30 * time.Second),
		listingClient: netguard.NewClient(5 * time.Minute),
		limiter:       sharedHistoryImportUpstreamLimiter,
	}
}

type jellyfinServerAuthResponse struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID string `json:"Id"`
	} `json:"User"`
}

type jellyfinItemsResponse struct {
	Items            []jellyfinItem `json:"Items"`
	TotalRecordCount int            `json:"TotalRecordCount"`
}

type jellyfinItem struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	ProductionYear    int               `json:"ProductionYear"`
	RunTimeTicks      int64             `json:"RunTimeTicks"`
	SeriesName        string            `json:"SeriesName"`
	SeriesID          string            `json:"SeriesId"`
	ProviderIDs       map[string]string `json:"ProviderIds"`
	IndexNumber       int               `json:"IndexNumber"`
	ParentIndexNumber int               `json:"ParentIndexNumber"`
	UserData          jellyfinUserData  `json:"UserData"`
}

type jellyfinUserData struct {
	PlaybackPositionTicks int64      `json:"PlaybackPositionTicks"`
	PlayCount             int        `json:"PlayCount"`
	LastPlayedDate        *time.Time `json:"LastPlayedDate"`
	Played                bool       `json:"Played"`
	IsFavorite            bool       `json:"IsFavorite"`
}

func (item jellyfinItem) retainedSize() int64 {
	size := sizeOf[jellyfinItem]() + stringBytes(item.ID, item.Name, item.Type, item.SeriesName, item.SeriesID) + stringMapBytes(item.ProviderIDs)
	if item.UserData.LastPlayedDate != nil {
		size += sizeOf[time.Time]()
	}
	return size
}

type jellyfinLocalAuth struct{ BaseURL, UserID, AccessToken string }

// jellyfinBaseURL normalizes a configured server address. Jellyfin answers
// "//Users/..." with 404, so a trailing slash must not reach request paths.
func jellyfinBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (a jellyfinLocalAuth) endpoint(path string) string {
	return jellyfinBaseURL(a.BaseURL) + path
}

// AuthenticateServerUser signs in once at the given address. Unlike Emby,
// Jellyfin gets no "/emby" retry: Jellyfin 12 removed that route prefix, and
// on older servers the retry repeats a rejected password as a second failed
// login.
func (c *JellyfinClient) AuthenticateServerUser(ctx context.Context, baseURL, username, password string) (*jellyfinLocalAuth, error) {
	base := jellyfinBaseURL(baseURL)
	body, _ := json.Marshal(map[string]string{"Username": username, "Pw": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/Users/AuthenticateByName", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("authenticating against Jellyfin server: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeader())
	var resp jellyfinServerAuthResponse
	if err := c.doJSON(req, &resp); err != nil {
		return nil, fmt.Errorf("authenticating against Jellyfin server: %w", err)
	}
	if strings.TrimSpace(resp.User.ID) == "" || strings.TrimSpace(resp.AccessToken) == "" {
		return nil, fmt.Errorf("authenticating against Jellyfin server: incomplete auth response")
	}
	return &jellyfinLocalAuth{BaseURL: base, UserID: resp.User.ID, AccessToken: resp.AccessToken}, nil
}

// itemsQuery is the listing of the user's items of the given types that match
// a Jellyfin filter such as IsPlayed or IsFavorite. It uses /Items?userId=
// because Jellyfin 12 dropped /Users/{userId}/Items from its API contract.
func itemsQuery(auth jellyfinLocalAuth, filter, includeItemTypes string) url.Values {
	query := url.Values{}
	query.Set("UserId", auth.UserID)
	query.Set("Filters", filter)
	query.Set("IncludeItemTypes", includeItemTypes)
	query.Set("Recursive", "true")
	query.Set("EnableUserData", "true")
	query.Set("Fields", jellyfinItemFields)
	return query
}

// forEachItemsPage visits a filtered item listing page by page from startIndex.
func (c *JellyfinClient) forEachItemsPage(ctx context.Context, auth jellyfinLocalAuth, filter, includeItemTypes string, startIndex int, visit func(items []jellyfinItem, pageStart int) error) error {
	err := c.forEachPage(ctx, auth, auth.endpoint("/Items"), itemsQuery(auth, filter, includeItemTypes), startIndex, visit)
	if err != nil && !isSinkError(err) {
		return fmt.Errorf("fetching Jellyfin items with filter %s: %w", filter, err)
	}
	return err
}

func (c *JellyfinClient) FetchItemsByIDs(ctx context.Context, auth jellyfinLocalAuth, ids []string, includeItemTypes string) ([]jellyfinItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var allItems []jellyfinItem
	for start := 0; start < len(ids); start += jellyfinIDChunkSize {
		end := min(start+jellyfinIDChunkSize, len(ids))
		chunk := ids[start:end]

		query := url.Values{}
		query.Set("UserId", auth.UserID)
		query.Set("Recursive", "true")
		query.Set("EnableUserData", "true")
		query.Set("Fields", jellyfinItemFields)
		query.Set("Ids", strings.Join(chunk, ","))
		if strings.TrimSpace(includeItemTypes) != "" {
			query.Set("IncludeItemTypes", includeItemTypes)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, auth.endpoint("/Items")+"?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeaderWithToken(auth.AccessToken))

		var payload jellyfinItemsResponse
		if err := c.doJSON(req, &payload); err != nil {
			return nil, fmt.Errorf("fetching Jellyfin items by ids: %w", err)
		}
		allItems = append(allItems, keepRequested(payload.Items, chunk, func(item jellyfinItem) string { return item.ID })...)
	}
	return allItems, nil
}

// FetchResumableItems pages through the user's in-progress movies and
// episodes. It filters by MediaTypes=Video, as Jellyfin's own Continue
// Watching row does, and keeps movies and episodes here: on a large Jellyfin
// 12.1 library the same request with IncludeItemTypes took over a minute,
// longer than the client timeout, while MediaTypes answered in under a second.
func (c *JellyfinClient) FetchResumableItems(ctx context.Context, auth jellyfinLocalAuth) ([]jellyfinItem, error) {
	query := url.Values{}
	query.Set("UserId", auth.UserID)
	query.Set("EnableUserData", "true")
	query.Set("MediaTypes", jellyfinVideoMediaType)
	query.Set("Fields", jellyfinItemFields)

	var items []jellyfinItem
	err := c.forEachPage(ctx, auth, auth.endpoint("/UserItems/Resume"), query, 0, func(page []jellyfinItem, _ int) error {
		items = append(items, page...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetching Jellyfin resumable items: %w", err)
	}
	// Filter after paging: paging stops on a short page, so it must see the
	// unfiltered page sizes.
	return slices.DeleteFunc(items, func(item jellyfinItem) bool {
		return !strings.EqualFold(item.Type, "Movie") && !strings.EqualFold(item.Type, "Episode")
	}), nil
}

// streamPlayedSince visits the user's played movies and episodes whose
// LastPlayedDate is at or after since. It asks Jellyfin for the newest plays
// first and stops after a page that holds older plays and none in the window,
// so a short lookback costs a page or two however long the user's history is.
// Every in-window item on a page is visited even when the page is not strictly
// newest first, which happens where the sort key and the reported play date
// disagree. Items with no LastPlayedDate are skipped: a full import covers them.
//
// The early stop is only as good as the server's sort, so sorted is false when
// the plays visibly are not newest first: a page starts with a play newer than
// the last one on the page before it, or the read would stop early after any
// page that was itself out of order. The caller then falls back to the full
// played read. Items already visited are applied again by it, which is safe.
//
// A play during the read moves its item to the top of the list. When that
// item was still unread, every unread item shifts down one place and the next
// page repeats the last item read, and the moved item is never returned. A
// repeated item therefore starts the read over once; if the list moves again,
// sorted is false and the caller reads the full history.
func (c *JellyfinClient) streamPlayedSince(ctx context.Context, auth jellyfinLocalAuth, since time.Time, visit func([]jellyfinItem) error) (sorted bool, err error) {
	for range 2 {
		sorted, moved, err := c.readPlayedSince(ctx, auth, since, visit)
		if err != nil || !moved {
			return sorted, err
		}
	}
	return false, nil
}

// readPlayedSince makes one streamPlayedSince pass. moved reports a page that
// repeated an item from the previous page; a shift of the list repeats items
// across a page boundary, so the previous page is all it has to remember.
func (c *JellyfinClient) readPlayedSince(ctx context.Context, auth jellyfinLocalAuth, since time.Time, visit func([]jellyfinItem) error) (sorted, moved bool, err error) {
	query := itemsQuery(auth, "IsPlayed", jellyfinPlayableItemTypes)
	query.Set("SortBy", "DatePlayed")
	query.Set("SortOrder", "Descending")

	endpoint := auth.endpoint("/Items")
	var budget listingBudget
	var previous *time.Time // the last dated play on the pages read so far
	disordered := false     // some page read so far was not newest first
	var lastPage map[string]struct{}
	startIndex := 0
	window := since.UTC().Format(time.RFC3339)
	for {
		payload, err := c.fetchPage(ctx, auth, endpoint, query, startIndex, &budget)
		if err != nil {
			return false, false, fmt.Errorf("fetching Jellyfin items played since %s: %w", window, err)
		}
		page := make(map[string]struct{}, len(payload.Items))
		for _, item := range payload.Items {
			if _, ok := lastPage[item.ID]; ok {
				return false, true, nil
			}
			page[item.ID] = struct{}{}
		}
		lastPage = page
		var kept []jellyfinItem
		older := 0
		var first, last *time.Time
		for _, item := range payload.Items {
			played := item.UserData.LastPlayedDate
			if played == nil {
				continue
			}
			if first == nil {
				first = played
			}
			if last != nil && played.After(*last) {
				disordered = true
			}
			last = played
			if played.Before(since) {
				older++
			} else {
				kept = append(kept, item)
			}
		}
		if previous != nil && first != nil && first.After(*previous) {
			return false, false, nil
		}
		if len(kept) > 0 {
			if err := visit(kept); err != nil {
				return false, false, err
			}
		}
		startIndex += len(payload.Items)
		if older > 0 && len(kept) == 0 {
			return !disordered, false, nil
		}
		if lastJellyfinPage(payload, startIndex) {
			return true, false, nil
		}
		if last != nil {
			previous = last
		}
	}
}

// forEachPage visits a paged listing from startIndex.
func (c *JellyfinClient) forEachPage(ctx context.Context, auth jellyfinLocalAuth, endpoint string, query url.Values, startIndex int, visit func(items []jellyfinItem, pageStart int) error) error {
	var budget listingBudget
	for {
		payload, err := c.fetchPage(ctx, auth, endpoint, query, startIndex, &budget)
		if err != nil {
			return err
		}
		if err := visit(payload.Items, startIndex); err != nil {
			return err
		}
		startIndex += len(payload.Items)
		if lastJellyfinPage(payload, startIndex) {
			return nil
		}
	}
}

// fetchPage reads one page of query starting at startIndex, charging it to
// budget.
func (c *JellyfinClient) fetchPage(ctx context.Context, auth jellyfinLocalAuth, endpoint string, query url.Values, startIndex int, budget *listingBudget) (jellyfinItemsResponse, error) {
	if err := budget.startPage(); err != nil {
		return jellyfinItemsResponse{}, err
	}
	pageQuery := url.Values{}
	for key, values := range query {
		copied := make([]string, len(values))
		copy(copied, values)
		pageQuery[key] = copied
	}
	pageQuery.Set("Limit", strconv.Itoa(jellyfinPageSize))
	pageQuery.Set("StartIndex", strconv.Itoa(startIndex))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+pageQuery.Encode(), nil)
	if err != nil {
		return jellyfinItemsResponse{}, err
	}
	setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeaderWithToken(auth.AccessToken))

	var payload jellyfinItemsResponse
	if err := c.doJSONWithBudget(req, &payload, budget); err != nil {
		return jellyfinItemsResponse{}, err
	}
	if err := budget.addItems(len(payload.Items)); err != nil {
		return jellyfinItemsResponse{}, err
	}
	return payload, nil
}

// lastJellyfinPage reports whether paging is done after a page that brought
// the running offset to startIndex.
func lastJellyfinPage(payload jellyfinItemsResponse, startIndex int) bool {
	return len(payload.Items) == 0 || (payload.TotalRecordCount > 0 && startIndex >= payload.TotalRecordCount) || len(payload.Items) < jellyfinPageSize
}

func (c *JellyfinClient) doJSON(req *http.Request, out any) error {
	return c.doJSONWithBudget(req, out, nil)
}

// doJSONWithBudget is doJSON for one page of a listing; budget may be nil.
// Listing pages, the only requests with a budget, use the listing client.
func (c *JellyfinClient) doJSONWithBudget(req *http.Request, out any, budget *listingBudget) error {
	if err := c.limiter.Wait(req.Context(), req.URL); err != nil {
		return err
	}
	client := c.httpClient
	if budget != nil {
		client = c.listingClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body := readUpstreamErrorBody(resp.Body)
		return &jellyfinHTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return decodeUpstreamJSON(req.Context(), resp.Body, out, budget)
}

type jellyfinHTTPError struct {
	StatusCode int
	Body       string
}

func (e *jellyfinHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("jellyfin http %d", e.StatusCode)
	}
	return fmt.Sprintf("jellyfin http %d: %s", e.StatusCode, e.Body)
}

func jellyfinAuthorizationHeader() string {
	return `MediaBrowser Client="watch-importer", Device="Silo", DeviceId="silo-history-import", Version="1.0.0"`
}

func jellyfinAuthorizationHeaderWithToken(token string) string {
	return jellyfinAuthorizationHeader() + `, Token="` + token + `"`
}

func setJellyfinAuthorizationHeader(req *http.Request, value string) {
	req.Header.Set("Authorization", value)
}

// ListUsers returns all user accounts on the Jellyfin server using an admin API token.
func (c *JellyfinClient) ListUsers(ctx context.Context, baseURL, adminToken string) ([]ExternalUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jellyfinBaseURL(baseURL)+"/Users", nil)
	if err != nil {
		return nil, err
	}
	setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeaderWithToken(adminToken))
	var users []struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := c.doJSON(req, &users); err != nil {
		return nil, fmt.Errorf("listing Jellyfin users: %w", err)
	}
	result := make([]ExternalUser, 0, len(users))
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		result = append(result, ExternalUser{ID: u.ID, Name: u.Name})
	}
	return result, nil
}
