package historyimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	jellyfinPageSize    = 200
	jellyfinIDChunkSize = 100
)

type JellyfinClient struct {
	httpClient *http.Client
	limiter    *upstreamRateLimiter
}

func NewJellyfinClient() *JellyfinClient {
	return &JellyfinClient{httpClient: &http.Client{Timeout: 30 * time.Second}, limiter: sharedHistoryImportUpstreamLimiter}
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
	UserData          struct {
		PlaybackPositionTicks int64      `json:"PlaybackPositionTicks"`
		PlayCount             int        `json:"PlayCount"`
		LastPlayedDate        *time.Time `json:"LastPlayedDate"`
		Played                bool       `json:"Played"`
	} `json:"UserData"`
}

type jellyfinLocalAuth struct{ BaseURL, UserID, AccessToken string }

func (c *JellyfinClient) AuthenticateServerUser(ctx context.Context, baseURL, username, password string) (*jellyfinLocalAuth, error) {
	body, _ := json.Marshal(map[string]string{"Username": username, "Pw": password})
	var lastErr error
	for _, candidate := range baseCandidates(baseURL) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, candidate+"/Users/AuthenticateByName", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeader())
		var resp jellyfinServerAuthResponse
		err = c.doJSON(req, &resp)
		if err == nil {
			if strings.TrimSpace(resp.User.ID) == "" || strings.TrimSpace(resp.AccessToken) == "" {
				return nil, fmt.Errorf("authenticating against Jellyfin server: incomplete auth response")
			}
			return &jellyfinLocalAuth{BaseURL: candidate, UserID: resp.User.ID, AccessToken: resp.AccessToken}, nil
		}
		if !shouldTryAnotherBase(err) {
			return nil, fmt.Errorf("authenticating against Jellyfin server: %w", err)
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("authenticating against Jellyfin server: %w", lastErr)
	}
	return nil, fmt.Errorf("authenticating against Jellyfin server: no reachable server URL")
}

func (c *JellyfinClient) FetchItems(ctx context.Context, auth jellyfinLocalAuth, filter string) ([]jellyfinItem, error) {
	query := url.Values{}
	query.Set("Filters", filter)
	query.Set("IncludeItemTypes", "Movie,Episode")
	query.Set("Recursive", "true")
	query.Set("EnableUserData", "true")
	query.Set("Fields", "ProviderIds,ParentId,DateCreated,Path,MediaSources,SeriesId,SeasonId,IndexNumber,ParentIndexNumber")

	items, err := c.fetchPagedItems(ctx, auth, fmt.Sprintf("%s/Users/%s/Items", auth.BaseURL, url.PathEscape(auth.UserID)), query)
	if err != nil {
		return nil, fmt.Errorf("fetching Jellyfin items with filter %s: %w", filter, err)
	}
	return items, nil
}

func (c *JellyfinClient) FetchItemsByIDs(ctx context.Context, auth jellyfinLocalAuth, ids []string, includeItemTypes string) ([]jellyfinItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var allItems []jellyfinItem
	for start := 0; start < len(ids); start += jellyfinIDChunkSize {
		end := start + jellyfinIDChunkSize
		if end > len(ids) {
			end = len(ids)
		}

		query := url.Values{}
		query.Set("Recursive", "true")
		query.Set("EnableUserData", "true")
		query.Set("Fields", "ProviderIds,ParentId,DateCreated,Path,MediaSources,SeriesId,SeasonId,IndexNumber,ParentIndexNumber")
		query.Set("Ids", strings.Join(ids[start:end], ","))
		if strings.TrimSpace(includeItemTypes) != "" {
			query.Set("IncludeItemTypes", includeItemTypes)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/Users/%s/Items?%s", auth.BaseURL, url.PathEscape(auth.UserID), query.Encode()), nil)
		if err != nil {
			return nil, err
		}
		setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeaderWithToken(auth.AccessToken))

		var payload jellyfinItemsResponse
		if err := c.doJSON(req, &payload); err != nil {
			return nil, fmt.Errorf("fetching Jellyfin items by ids: %w", err)
		}
		allItems = append(allItems, payload.Items...)
	}
	return allItems, nil
}

func (c *JellyfinClient) FetchResumableItems(ctx context.Context, auth jellyfinLocalAuth) ([]jellyfinItem, error) {
	var allItems []jellyfinItem
	startIndex := 0

	for {
		query := url.Values{}
		query.Set("UserId", auth.UserID)
		query.Set("EnableUserData", "true")
		query.Set("IncludeItemTypes", "Movie,Episode")
		query.Set("Fields", "ProviderIds,ParentId,DateCreated,Path,MediaSources,SeriesId,SeasonId,IndexNumber,ParentIndexNumber")
		query.Set("Limit", strconv.Itoa(jellyfinPageSize))
		query.Set("StartIndex", strconv.Itoa(startIndex))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/UserItems/Resume?%s", auth.BaseURL, query.Encode()), nil)
		if err != nil {
			return nil, err
		}
		setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeaderWithToken(auth.AccessToken))

		var payload jellyfinItemsResponse
		if err := c.doJSON(req, &payload); err != nil {
			return nil, fmt.Errorf("fetching Jellyfin resumable items: %w", err)
		}

		allItems = append(allItems, payload.Items...)
		startIndex += len(payload.Items)
		if len(payload.Items) == 0 || (payload.TotalRecordCount > 0 && startIndex >= payload.TotalRecordCount) || len(payload.Items) < jellyfinPageSize {
			break
		}
	}

	return allItems, nil
}

func (c *JellyfinClient) fetchPagedItems(ctx context.Context, auth jellyfinLocalAuth, endpoint string, query url.Values) ([]jellyfinItem, error) {
	var allItems []jellyfinItem
	startIndex := 0

	for {
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
			return nil, err
		}
		setJellyfinAuthorizationHeader(req, jellyfinAuthorizationHeaderWithToken(auth.AccessToken))

		var payload jellyfinItemsResponse
		if err := c.doJSON(req, &payload); err != nil {
			return nil, err
		}

		allItems = append(allItems, payload.Items...)
		startIndex += len(payload.Items)
		if len(payload.Items) == 0 || (payload.TotalRecordCount > 0 && startIndex >= payload.TotalRecordCount) || len(payload.Items) < jellyfinPageSize {
			break
		}
	}

	return allItems, nil
}

func (c *JellyfinClient) doJSON(req *http.Request, out any) error {
	if err := c.limiter.Wait(req.Context(), req.URL); err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &jellyfinHTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/Users", nil)
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
