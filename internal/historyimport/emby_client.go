package historyimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

const (
	embyPageSize    = 500
	embyIDChunkSize = 100
	// Emby list queries omit the year, play count, and last-played date unless
	// Fields names them; without the date no history row or freshness
	// timestamp can be imported.
	embyItemFields = "ProviderIds,ProductionYear,UserDataPlayCount,UserDataLastPlayedDate"
)

type EmbyClient struct {
	httpClient *http.Client
	limiter    *upstreamRateLimiter
}

func NewEmbyClient() *EmbyClient {
	return &EmbyClient{
		httpClient: netguard.NewClient(30 * time.Second),
		limiter:    sharedHistoryImportUpstreamLimiter,
	}
}

type embyConnectAuthResponse struct {
	ConnectAccessToken string
	ConnectUserID      string
	ResponseKeys       []string
}

type embyServerInfo struct {
	Name         string `json:"Name"`
	SystemID     string `json:"SystemId"`
	URL          string `json:"Url"`
	LocalAddress string `json:"LocalAddress"`
	AccessKey    string `json:"AccessKey"`
}

type embyServerAuthResponse struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID string `json:"Id"`
	} `json:"User"`
}

type embyExchangeResponse struct {
	LocalUserID string `json:"LocalUserId"`
	AccessToken string `json:"AccessToken"`
}

type embyItemsResponse struct {
	Items            []embyItem `json:"Items"`
	TotalRecordCount int        `json:"TotalRecordCount"`
}

type embyItem struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	ProductionYear    int               `json:"ProductionYear"`
	RunTimeTicks      int64             `json:"RunTimeTicks"`
	SeriesName        string            `json:"SeriesName"`
	SeriesID          string            `json:"SeriesId"`
	ProviderIDs       map[string]string `json:"ProviderIds"`
	IndexNumber       int               `json:"IndexNumber"`
	IndexNumberEnd    int               `json:"IndexNumberEnd"`
	ParentIndexNumber int               `json:"ParentIndexNumber"`
	UserData          struct {
		PlaybackPositionTicks int64      `json:"PlaybackPositionTicks"`
		PlayCount             int        `json:"PlayCount"`
		LastPlayedDate        *time.Time `json:"LastPlayedDate"`
		Played                bool       `json:"Played"`
	} `json:"UserData"`
}

type embyUser struct {
	ID string `json:"Id"`
}

type embyLocalAuth struct {
	BaseURL     string
	UserID      string
	AccessToken string
}

func (c *EmbyClient) ConnectAuthenticate(ctx context.Context, username, password string) (*embyConnectAuthResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"nameOrEmail": username,
		"rawpw":       password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://connect.emby.media/service/user/authenticate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Application", connectApplicationName)
	var resp embyConnectAuthResponse
	if err := c.doJSON(req, &resp); err != nil {
		return nil, fmt.Errorf("authenticating with Emby Connect: %w", err)
	}
	if strings.TrimSpace(resp.ConnectUserID) == "" || strings.TrimSpace(resp.ConnectAccessToken) == "" {
		return nil, fmt.Errorf(
			"authenticating with Emby Connect: login succeeded but connect token payload was incomplete (keys=%s)",
			strings.Join(resp.ResponseKeys, ","),
		)
	}
	return &resp, nil
}

func (c *EmbyClient) ConnectServers(ctx context.Context, connectUserID, connectToken string) ([]ConnectServer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://connect.emby.media/service/servers?userId="+url.QueryEscape(connectUserID),
		nil,
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Application", connectApplicationName)
	req.Header.Set("X-Connect-UserToken", connectToken)
	var payload []embyServerInfo
	if err := c.doJSON(req, &payload); err != nil {
		return nil, fmt.Errorf("listing Emby Connect servers: %w", err)
	}
	servers := make([]ConnectServer, 0, len(payload))
	for _, item := range payload {
		serverURL := firstNonEmpty(item.URL, item.LocalAddress)
		servers = append(servers, ConnectServer{
			ID:           firstNonEmpty(item.SystemID, item.Name),
			Name:         item.Name,
			SystemID:     item.SystemID,
			URL:          strings.TrimSpace(item.URL),
			LocalAddress: strings.TrimSpace(item.LocalAddress),
			AccessKey:    item.AccessKey,
			HasRemoteURL: strings.TrimSpace(item.URL) != "",
			HasLocalURL:  strings.TrimSpace(item.LocalAddress) != "",
		})
		if servers[len(servers)-1].ID == "" {
			servers[len(servers)-1].ID = serverURL
		}
	}
	return servers, nil
}

func (c *EmbyClient) ConnectExchange(ctx context.Context, baseURL, connectUserID, accessKey string) (*embyLocalAuth, error) {
	path := "/Connect/Exchange?format=json&ConnectUserId=" + url.QueryEscape(connectUserID)
	var lastErr error
	for _, candidate := range baseCandidates(baseURL) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Emby-Token", accessKey)
		req.Header.Set("X-Emby-Authorization", embyAuthorizationHeader("", accessKey))
		var resp embyExchangeResponse
		err = c.doJSON(req, &resp)
		if err == nil {
			return &embyLocalAuth{BaseURL: candidate, UserID: resp.LocalUserID, AccessToken: resp.AccessToken}, nil
		}
		if !shouldTryAnotherBase(err) {
			return nil, fmt.Errorf("exchanging Emby Connect token: %w", err)
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("exchanging Emby Connect token: %w", lastErr)
	}
	return nil, fmt.Errorf("exchanging Emby Connect token: no reachable server URL")
}

func (c *EmbyClient) AuthenticateServerUser(ctx context.Context, baseURL, username, password string) (*embyLocalAuth, error) {
	body, _ := json.Marshal(map[string]string{
		"Username": username,
		"Pw":       password,
	})
	var lastErr error
	for _, candidate := range baseCandidates(baseURL) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, candidate+"/Users/AuthenticateByName", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Emby-Authorization", embyAuthorizationHeader("", ""))
		var resp embyServerAuthResponse
		err = c.doJSON(req, &resp)
		if err == nil {
			return &embyLocalAuth{BaseURL: candidate, UserID: resp.User.ID, AccessToken: resp.AccessToken}, nil
		}
		if !shouldTryAnotherBase(err) {
			return nil, fmt.Errorf("authenticating against Emby server: %w", err)
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("authenticating against Emby server: %w", lastErr)
	}
	return nil, fmt.Errorf("authenticating against Emby server: no reachable server URL")
}

func (c *EmbyClient) FetchItems(ctx context.Context, auth embyLocalAuth, filter string) ([]embyItem, error) {
	return c.fetchItems(ctx, auth, filter, "Movie,Episode")
}

// FetchFavoriteItems includes seasons so the provider can report the season
// favorites Silo has no target for, rather than dropping them silently.
func (c *EmbyClient) FetchFavoriteItems(ctx context.Context, auth embyLocalAuth) ([]embyItem, error) {
	return c.fetchItems(ctx, auth, "IsFavorite", "Movie,Series,Season,Episode")
}

// fetchItems pages through the user's items so a large library is read in
// bounded responses instead of one body racing the client timeout.
func (c *EmbyClient) fetchItems(ctx context.Context, auth embyLocalAuth, filter, includeItemTypes string) ([]embyItem, error) {
	var items []embyItem
	for startIndex := 0; ; {
		query := url.Values{}
		query.Set("Recursive", "true")
		query.Set("EnableUserData", "true")
		query.Set("Fields", embyItemFields)
		query.Set("IncludeItemTypes", includeItemTypes)
		query.Set("Filters", filter)
		query.Set("StartIndex", strconv.Itoa(startIndex))
		query.Set("Limit", strconv.Itoa(embyPageSize))
		payload, err := c.getItems(ctx, auth, query)
		if err != nil {
			return nil, fmt.Errorf("fetching Emby items with filter %s: %w", filter, err)
		}
		items = append(items, payload.Items...)
		startIndex += len(payload.Items)
		if len(payload.Items) == 0 {
			return items, nil
		}
		// Trust the reported total so a server capping page size below the
		// request is still read to the end; without one, a short page is last.
		if payload.TotalRecordCount > 0 {
			if startIndex >= payload.TotalRecordCount {
				return items, nil
			}
		} else if len(payload.Items) < embyPageSize {
			return items, nil
		}
	}
}

func (c *EmbyClient) FetchItemsByIDs(ctx context.Context, auth embyLocalAuth, ids []string, includeItemTypes string) ([]embyItem, error) {
	var items []embyItem
	for chunk := range slices.Chunk(ids, embyIDChunkSize) {
		query := url.Values{}
		query.Set("Recursive", "true")
		query.Set("Fields", embyItemFields)
		query.Set("Ids", strings.Join(chunk, ","))
		if strings.TrimSpace(includeItemTypes) != "" {
			query.Set("IncludeItemTypes", includeItemTypes)
		}
		payload, err := c.getItems(ctx, auth, query)
		if err != nil {
			return nil, fmt.Errorf("fetching Emby items by ids: %w", err)
		}
		items = append(items, payload.Items...)
	}
	return items, nil
}

func (c *EmbyClient) getItems(ctx context.Context, auth embyLocalAuth, query url.Values) (embyItemsResponse, error) {
	path := fmt.Sprintf("%s/Users/%s/Items?%s", auth.BaseURL, url.PathEscape(auth.UserID), query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return embyItemsResponse{}, err
	}
	req.Header.Set("X-Emby-Token", auth.AccessToken)
	req.Header.Set("X-Emby-Authorization", embyAuthorizationHeader(auth.UserID, auth.AccessToken))
	var payload embyItemsResponse
	if err := c.doJSON(req, &payload); err != nil {
		return embyItemsResponse{}, err
	}
	return payload, nil
}

func (c *EmbyClient) doJSON(req *http.Request, out any) error {
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
		return &embyHTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type embyHTTPError struct {
	StatusCode int
	Body       string
}

func (e *embyHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("emby http %d", e.StatusCode)
	}
	return fmt.Sprintf("emby http %d: %s", e.StatusCode, e.Body)
}

func UpstreamHTTPStatus(err error) int {
	var embyErr *embyHTTPError
	if errors.As(err, &embyErr) {
		return embyErr.StatusCode
	}
	var plexErr *plexHTTPError
	if errors.As(err, &plexErr) {
		return plexErr.StatusCode
	}
	var jellyfinErr *jellyfinHTTPError
	if errors.As(err, &jellyfinErr) {
		return jellyfinErr.StatusCode
	}
	return 0
}

func shouldTryAnotherBase(err error) bool {
	httpErr, ok := err.(*embyHTTPError)
	if !ok {
		return true
	}
	return httpErr.StatusCode == http.StatusNotFound
}

func embyAuthorizationHeader(userID, token string) string {
	parts := []string{
		`Client="Silo"`,
		`Device="Silo"`,
		`DeviceId="silo-history-import"`,
		`Version="1.0.0"`,
	}
	if userID != "" {
		parts = append(parts, fmt.Sprintf(`UserId="%s"`, userID))
	}
	if token != "" {
		parts = append(parts, fmt.Sprintf(`Token="%s"`, token))
	}
	return "Emby " + strings.Join(parts, ", ")
}

func baseCandidates(raw string) []string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return nil
	}
	candidates := []string{trimmed}
	if !strings.HasSuffix(strings.ToLower(trimmed), "/emby") {
		candidates = append(candidates, trimmed+"/emby")
	}
	return candidates
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (r *embyConnectAuthResponse) UnmarshalJSON(data []byte) error {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	r.ResponseKeys = slices.Sorted(maps.Keys(payload))

	var userID string
	if nestedUser, ok := payload["User"].(map[string]any); ok {
		userID = firstNonEmpty(
			stringField(nestedUser, "Id"),
			stringField(nestedUser, "ID"),
			stringField(nestedUser, "id"),
			stringField(nestedUser, "UserId"),
			stringField(nestedUser, "userId"),
		)
	}

	r.ConnectAccessToken = firstNonEmpty(
		stringField(payload, "ConnectAccessToken"),
		stringField(payload, "connectAccessToken"),
		stringField(payload, "ConnectUserToken"),
		stringField(payload, "connectUserToken"),
		stringField(payload, "AccessToken"),
		stringField(payload, "accessToken"),
	)
	r.ConnectUserID = firstNonEmpty(
		stringField(payload, "ConnectUserId"),
		stringField(payload, "ConnectUserID"),
		stringField(payload, "connectUserId"),
		stringField(payload, "UserId"),
		stringField(payload, "userId"),
		userID,
	)
	return nil
}

func stringField(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

// ListUsers returns all user accounts on the Emby server using an admin API token.
func (c *EmbyClient) ListUsers(ctx context.Context, baseURL, adminToken string) ([]ExternalUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/Users", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Emby-Token", adminToken)
	req.Header.Set("X-Emby-Authorization", embyAuthorizationHeader("", adminToken))
	var users []struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := c.doJSON(req, &users); err != nil {
		return nil, fmt.Errorf("listing Emby users: %w", err)
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
