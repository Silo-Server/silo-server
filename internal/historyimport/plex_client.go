package historyimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	plexClientIdentifier = "silo-history-import"
	plexProduct          = "Silo"
	plexVersion          = "1.0.0"
	plexTVBaseURL        = "https://plex.tv"
	// plexDiscoverBaseURL hosts account-level metadata (the user's
	// watchlist), which lives on plex.tv infrastructure rather than the PMS.
	plexDiscoverBaseURL = "https://discover.provider.plex.tv"
	plexPageSize        = 500
	// plexWatchlistPageSize is deliberately smaller: the discover API
	// rejects the PMS page size with 400 "Invalid value provided for
	// x-plex-container-size!".
	plexWatchlistPageSize = 100
)

var (
	errPrivatePlexDestination  = errors.New("plex destination resolves to a private or special-use network")
	errInsecurePlexDestination = errors.New("profile Plex imports require HTTPS destinations")
	errPlexDialTimeout         = errors.New("plex dial budget exhausted before any address was reachable")
	plexDeniedNetworks         = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/128"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2001:2::/48"),
		netip.MustParsePrefix("2001:10::/28"),
		netip.MustParsePrefix("2001:20::/28"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("fe80::/10"),
	}
)

type PlexClient struct {
	httpClient *http.Client
	limiter    *upstreamRateLimiter
	// discoverBaseURL is overridable for tests; empty means the real host.
	discoverBaseURL string
}

type PlexAccount struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

func NewPlexClient() *PlexClient {
	return &PlexClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		limiter:    sharedHistoryImportUpstreamLimiter,
	}
}

func (c *PlexClient) publicDestinationsOnly() *PlexClient {
	clone := *c
	timeout := 30 * time.Second
	if c.httpClient != nil && c.httpClient.Timeout > 0 {
		timeout = c.httpClient.Timeout
	}
	clone.httpClient = newPublicPlexHTTPClient(timeout)
	return &clone
}

func newPublicPlexHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         publicPlexDialContext(dialer),
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     &publicPlexTransport{base: transport},
		CheckRedirect: checkPublicPlexRedirect,
	}
}

// checkPublicPlexRedirect bounds the redirect chain, keeps it on HTTPS, and
// drops the Plex credential when the chain leaves the host it was minted for.
//
// net/http strips Authorization, Cookie, and WWW-Authenticate across a
// cross-host redirect but knows nothing about X-Plex-Token, so a redirect from
// a user-supplied Plex address to an attacker's host would otherwise hand that
// host the user's token. Same-host redirects (including a port change) keep it.
func checkPublicPlexRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many Plex redirects")
	}
	if req.URL.Scheme != "https" {
		return errInsecurePlexDestination
	}
	if len(via) > 0 {
		previous := via[len(via)-1].URL
		if !strings.EqualFold(previous.Hostname(), req.URL.Hostname()) {
			req.Header.Del("X-Plex-Token")
		}
	}
	return nil
}

// plexDialTimeoutFloor keeps a long address list from partitioning the dial
// budget into attempts too short to ever succeed. It matches the "sane minimum"
// net/http applies in partialDeadline.
const plexDialTimeoutFloor = 2 * time.Second

// plexDialBudget is the deadline the whole address list shares: the caller's
// context deadline or the dialer's own timeout, whichever lands first. A zero
// return means neither imposes one.
func plexDialBudget(ctx context.Context, dialer *net.Dialer, now time.Time) time.Time {
	var deadline time.Time
	if dialer != nil && dialer.Timeout > 0 {
		deadline = now.Add(dialer.Timeout)
	}
	if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
		deadline = ctxDeadline
	}
	return deadline
}

// plexPartialDeadline splits the remaining budget across the addresses still
// untried, mirroring net/http's partialDeadline: without it a couple of
// black-holed A/AAAA records each consume the full dial timeout and exhaust the
// enclosing HTTP budget before a reachable address is ever tried.
func plexPartialDeadline(now, deadline time.Time, addressesRemaining int) (time.Time, error) {
	if deadline.IsZero() {
		return time.Time{}, nil
	}
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return time.Time{}, errPlexDialTimeout
	}
	if addressesRemaining < 1 {
		addressesRemaining = 1
	}
	timeout := remaining / time.Duration(addressesRemaining)
	if timeout < plexDialTimeoutFloor {
		timeout = min(remaining, plexDialTimeoutFloor)
	}
	return now.Add(timeout), nil
}

type publicPlexTransport struct {
	base *http.Transport
}

func (t *publicPlexTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, errInsecurePlexDestination
	}
	return t.base.RoundTrip(req)
}

func (t *publicPlexTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
}

func publicPlexDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}

		var addresses []netip.Addr
		if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
			addresses = []netip.Addr{literal}
		} else {
			addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
		}

		candidates := make([]netip.Addr, 0, len(addresses))
		for _, candidate := range addresses {
			if publicPlexAddress(candidate) {
				candidates = append(candidates, candidate)
			}
		}
		if len(candidates) == 0 {
			return nil, errPrivatePlexDestination
		}

		budget := plexDialBudget(ctx, dialer, time.Now())
		var dialErrors []error
		for i, candidate := range candidates {
			attemptDeadline, err := plexPartialDeadline(time.Now(), budget, len(candidates)-i)
			if err != nil {
				dialErrors = append(dialErrors, err)
				break
			}
			attemptCtx, cancel := ctx, context.CancelFunc(func() {})
			if !attemptDeadline.IsZero() {
				attemptCtx, cancel = context.WithDeadline(ctx, attemptDeadline)
			}
			conn, dialErr := dialer.DialContext(attemptCtx, network, net.JoinHostPort(candidate.String(), port))
			cancel()
			if dialErr == nil {
				return conn, nil
			}
			dialErrors = append(dialErrors, dialErr)
			if ctx.Err() != nil {
				break
			}
		}
		return nil, errors.Join(dialErrors...)
	}
}

func publicPlexAddress(address netip.Addr) bool {
	if address.Is4In6() {
		address = address.Unmap()
	}
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, denied := range plexDeniedNetworks {
		if denied.Contains(address) {
			return false
		}
	}
	return true
}

func (c *PlexClient) CreatePin(ctx context.Context) (pinID int, pinCode string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, plexTVBaseURL+"/api/v2/pins", strings.NewReader("strong=true"))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setPlexHeaders(req, "")
	var resp struct {
		ID   int    `json:"id"`
		Code string `json:"code"`
	}
	if err := c.doJSON(req, &resp); err != nil {
		return 0, "", fmt.Errorf("creating Plex pin: %w", err)
	}
	if resp.ID == 0 || resp.Code == "" {
		return 0, "", fmt.Errorf("creating Plex pin: empty pin response")
	}
	return resp.ID, resp.Code, nil
}

func (c *PlexClient) CheckPin(ctx context.Context, pinID int) (authToken string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v2/pins/%d", plexTVBaseURL, pinID), nil)
	if err != nil {
		return "", err
	}
	c.setPlexHeaders(req, "")
	var resp struct {
		AuthToken string `json:"authToken"`
	}
	if err := c.doJSON(req, &resp); err != nil {
		return "", fmt.Errorf("checking Plex pin: %w", err)
	}
	return resp.AuthToken, nil
}

type plexResourceEntry struct {
	Name             string `json:"name"`
	Product          string `json:"product"`
	ClientIdentifier string `json:"clientIdentifier"`
	Provides         string `json:"provides"`
	OwnerID          *int   `json:"ownerId"`
	Owned            bool   `json:"owned"`
	AccessToken      string `json:"accessToken"`
	Connections      []struct {
		Protocol string `json:"protocol"`
		Address  string `json:"address"`
		Port     int    `json:"port"`
		URI      string `json:"uri"`
		Local    bool   `json:"local"`
	} `json:"connections"`
}

func (c *PlexClient) GetResources(ctx context.Context, token string) ([]PlexServer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexTVBaseURL+"/api/v2/resources?includeHttps=1&includeRelay=1", nil)
	if err != nil {
		return nil, err
	}
	c.setPlexHeaders(req, token)
	var entries []plexResourceEntry
	if err := c.doJSON(req, &entries); err != nil {
		return nil, fmt.Errorf("listing Plex resources: %w", err)
	}
	var servers []PlexServer
	for _, entry := range entries {
		if !strings.Contains(entry.Provides, "server") {
			continue
		}
		server := PlexServer{
			Name:             entry.Name,
			ClientIdentifier: entry.ClientIdentifier,
			AccessToken:      entry.AccessToken,
			ConnectionURLs:   make([]string, 0, len(entry.Connections)),
			Owned:            entry.Owned,
		}
		for _, conn := range entry.Connections {
			server.ConnectionURLs = append(server.ConnectionURLs, conn.URI)
			if conn.Local {
				server.LocalURL = conn.URI
				server.HasLocalURL = true
			} else {
				server.RemoteURL = conn.URI
				server.HasRemoteURL = true
			}
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func (c *PlexClient) GetCurrentUser(ctx context.Context, token string) (*PlexAccount, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexTVBaseURL+"/api/v2/user", nil)
	if err != nil {
		return nil, err
	}
	c.setPlexHeaders(req, token)
	var account PlexAccount
	if err := c.doJSON(req, &account); err != nil {
		return nil, fmt.Errorf("getting current Plex user: %w", err)
	}
	if account.ID == 0 {
		return nil, fmt.Errorf("getting current Plex user: empty user response")
	}
	return &account, nil
}

type plexMediaContainerBody struct {
	Size      int        `json:"size"`
	TotalSize int        `json:"totalSize"`
	Offset    int        `json:"offset"`
	Metadata  []PlexItem `json:"Metadata"`
	// Video mirrors Metadata: the discover API inconsistently keys some
	// responses on "Video" instead of "Metadata" (movie items in
	// particular), so both must be decoded.
	Video     []PlexItem `json:"Video"`
	Directory []struct {
		Key   string `json:"key"`
		Type  string `json:"type"`
		Title string `json:"title"`
	} `json:"Directory"`
}

type plexMediaContainer struct {
	MediaContainer plexMediaContainerBody `json:"MediaContainer"`
}

type plexLibrarySection struct {
	Key   string
	Type  string
	Title string
}

// items returns the container's media entries regardless of whether the
// upstream keyed them on "Metadata" or "Video".
func (c *plexMediaContainer) items() []PlexItem {
	if len(c.MediaContainer.Metadata) > 0 {
		return c.MediaContainer.Metadata
	}
	return c.MediaContainer.Video
}

type PlexItem struct {
	RatingKey            string    `json:"ratingKey"`
	Key                  string    `json:"key"`
	Type                 string    `json:"type"`
	Title                string    `json:"title"`
	GrandparentTitle     string    `json:"grandparentTitle"`
	GrandparentRatingKey string    `json:"grandparentRatingKey"`
	ParentIndex          int       `json:"parentIndex"`
	Index                int       `json:"index"`
	Year                 int       `json:"year"`
	Duration             int64     `json:"duration"`
	ViewOffset           int64     `json:"viewOffset"`
	ViewCount            int       `json:"viewCount"`
	LastViewedAt         int64     `json:"lastViewedAt"`
	Guid                 PlexGuids `json:"Guid"`
}

type PlexGuid struct {
	ID string `json:"id"`
}

type PlexGuids []PlexGuid

func (g *PlexGuids) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*g = nil
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		single = strings.TrimSpace(single)
		if single == "" {
			*g = nil
			return nil
		}
		*g = PlexGuids{{ID: single}}
		return nil
	}

	var many []PlexGuid
	if err := json.Unmarshal(data, &many); err == nil {
		*g = PlexGuids(many)
		return nil
	}

	return fmt.Errorf("unsupported Plex Guid payload: %s", string(data))
}

func (c *PlexClient) FetchLibrarySections(ctx context.Context, baseURL, token string) ([]plexLibrarySection, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/library/sections", nil)
	if err != nil {
		return nil, err
	}
	c.setPlexHeaders(req, token)
	var container struct {
		MediaContainer *plexMediaContainerBody `json:"MediaContainer"`
	}
	if err := c.doJSON(req, &container); err != nil {
		return nil, fmt.Errorf("fetching Plex library sections: %w", err)
	}
	if container.MediaContainer == nil {
		return nil, fmt.Errorf("fetching Plex library sections: response is missing MediaContainer")
	}
	var sections []plexLibrarySection
	for _, dir := range container.MediaContainer.Directory {
		sections = append(sections, plexLibrarySection{Key: dir.Key, Type: dir.Type, Title: dir.Title})
	}
	return sections, nil
}

func (c *PlexClient) FetchWatchedItems(ctx context.Context, baseURL, token, sectionKey string, mediaType int) ([]PlexItem, error) {
	return c.fetchSectionItems(ctx, baseURL, token, sectionKey, mediaType, true)
}

func (c *PlexClient) FetchSectionItems(ctx context.Context, baseURL, token, sectionKey string, mediaType int) ([]PlexItem, error) {
	return c.fetchSectionItems(ctx, baseURL, token, sectionKey, mediaType, false)
}

func (c *PlexClient) fetchSectionItems(ctx context.Context, baseURL, token, sectionKey string, mediaType int, watchedOnly bool) ([]PlexItem, error) {
	var allItems []PlexItem
	offset := 0
	for {
		query := url.Values{}
		query.Set("type", strconv.Itoa(mediaType))
		if watchedOnly {
			query.Set("unwatched", "0")
		}
		query.Set("includeGuids", "1")
		query.Set("X-Plex-Container-Start", strconv.Itoa(offset))
		query.Set("X-Plex-Container-Size", strconv.Itoa(plexPageSize))
		reqURL := fmt.Sprintf("%s/library/sections/%s/all?%s", baseURL, url.PathEscape(sectionKey), query.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		c.setPlexHeaders(req, token)
		var container plexMediaContainer
		if err := c.doJSON(req, &container); err != nil {
			return nil, fmt.Errorf("fetching Plex section items (section %s, type %d, offset %d): %w", sectionKey, mediaType, offset, err)
		}
		pageItems := container.items()
		allItems = append(allItems, pageItems...)
		offset += len(pageItems)
		if offset >= container.MediaContainer.TotalSize || len(pageItems) == 0 {
			break
		}
	}
	return allItems, nil
}

// FetchWatchlist pages through the user's account-level watchlist on the
// Plex discover API. It authenticates with the plex.tv ACCOUNT token (from
// the PIN/OAuth session), not a server access token: the watchlist belongs
// to the account, not to any PMS.
//
// The discover listing does not honor includeGuids, so items usually arrive
// without external ids. Each id-less item gets a follow-up per-item metadata
// fetch to resolve its Guid array; failures there degrade to warnings so the
// rest of the watchlist still imports (matching falls back to title/year).
func (c *PlexClient) FetchWatchlist(ctx context.Context, accountToken string) ([]PlexItem, []string, error) {
	base := c.discoverBaseURL
	if base == "" {
		base = plexDiscoverBaseURL
	}
	var allItems []PlexItem
	offset := 0
	for {
		query := url.Values{}
		query.Set("includeGuids", "1")
		query.Set("X-Plex-Container-Start", strconv.Itoa(offset))
		query.Set("X-Plex-Container-Size", strconv.Itoa(plexWatchlistPageSize))
		reqURL := fmt.Sprintf("%s/library/sections/watchlist/all?%s", base, query.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, nil, err
		}
		c.setPlexHeaders(req, accountToken)
		var container plexMediaContainer
		if err := c.doJSON(req, &container); err != nil {
			return nil, nil, fmt.Errorf("fetching Plex watchlist (offset %d): %w", offset, err)
		}
		items := container.items()
		allItems = append(allItems, items...)
		offset += len(items)
		if offset >= container.MediaContainer.TotalSize || len(items) == 0 {
			break
		}
	}

	var warnings []string
	unresolved := 0
	for i := range allItems {
		if len(allItems[i].Guid) > 0 {
			continue
		}
		detail, err := c.fetchWatchlistItemMetadata(ctx, base, accountToken, allItems[i].RatingKey)
		if err != nil || detail == nil {
			unresolved++
			continue
		}
		allItems[i].Guid = detail.Guid
		if allItems[i].Year == 0 {
			allItems[i].Year = detail.Year
		}
	}
	if unresolved > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"watchlist: could not resolve external ids for %d of %d items; those fall back to exact title/year matching",
			unresolved, len(allItems)))
	}
	return allItems, warnings, nil
}

// fetchWatchlistItemMetadata resolves one watchlist entry's full metadata
// (including its external-id Guid array) from the discover API.
func (c *PlexClient) fetchWatchlistItemMetadata(ctx context.Context, base, accountToken, ratingKey string) (*PlexItem, error) {
	reqURL := fmt.Sprintf("%s/library/metadata/%s", base, url.PathEscape(ratingKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	c.setPlexHeaders(req, accountToken)
	var container plexMediaContainer
	if err := c.doJSON(req, &container); err != nil {
		return nil, fmt.Errorf("fetching Plex watchlist metadata for %s: %w", ratingKey, err)
	}
	items := container.items()
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

func (c *PlexClient) FetchOnDeck(ctx context.Context, baseURL, token string) ([]PlexItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/library/onDeck?includeGuids=1", nil)
	if err != nil {
		return nil, err
	}
	c.setPlexHeaders(req, token)
	var container plexMediaContainer
	if err := c.doJSON(req, &container); err != nil {
		return nil, fmt.Errorf("fetching Plex on-deck items: %w", err)
	}
	return container.MediaContainer.Metadata, nil
}

func (c *PlexClient) FetchMetadata(ctx context.Context, baseURL, token, ratingKey string) (*PlexItem, error) {
	reqURL := fmt.Sprintf("%s/library/metadata/%s?includeGuids=1", baseURL, url.PathEscape(ratingKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	c.setPlexHeaders(req, token)
	var container plexMediaContainer
	if err := c.doJSON(req, &container); err != nil {
		return nil, fmt.Errorf("fetching Plex metadata for %s: %w", ratingKey, err)
	}
	if len(container.MediaContainer.Metadata) == 0 {
		return nil, nil
	}
	return &container.MediaContainer.Metadata[0], nil
}

// Authenticate exchanges Plex account credentials for an auth token via plex.tv.
func (c *PlexClient) Authenticate(ctx context.Context, username, password string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, plexTVBaseURL+"/users/sign_in.json", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(username, password)
	c.setPlexHeaders(req, "")
	var resp struct {
		User struct {
			AuthToken string `json:"authToken"`
		} `json:"user"`
	}
	if err := c.doJSON(req, &resp); err != nil {
		return "", fmt.Errorf("authenticating with Plex: %w", err)
	}
	if resp.User.AuthToken == "" {
		return "", fmt.Errorf("authenticating with Plex: no auth token in response")
	}
	return resp.User.AuthToken, nil
}

func (c *PlexClient) setPlexHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Client-Identifier", plexClientIdentifier)
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Version", plexVersion)
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
}

func (c *PlexClient) doJSON(req *http.Request, out any) error {
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
		return &plexHTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type plexHTTPError struct {
	StatusCode int
	Body       string
}

func (e *plexHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("plex http %d", e.StatusCode)
	}
	return fmt.Sprintf("plex http %d: %s", e.StatusCode, e.Body)
}

// ListAccounts returns all user accounts that have access to the Plex Media Server.
// Requires an admin token for the server.
func (c *PlexClient) ListAccounts(ctx context.Context, baseURL, token string) ([]ExternalUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/accounts", nil)
	if err != nil {
		return nil, err
	}
	c.setPlexHeaders(req, token)
	var container struct {
		MediaContainer struct {
			Account []struct {
				ID         int    `json:"id"`
				Name       string `json:"name"`
				Home       bool   `json:"home"`
				Guest      bool   `json:"guest"`
				Restricted bool   `json:"restricted"`
			} `json:"Account"`
		} `json:"MediaContainer"`
	}
	if err := c.doJSON(req, &container); err != nil {
		return nil, fmt.Errorf("listing Plex accounts: %w", err)
	}
	result := make([]ExternalUser, 0, len(container.MediaContainer.Account))
	for _, a := range container.MediaContainer.Account {
		if a.ID == 0 {
			continue
		}
		result = append(result, ExternalUser{
			ID:         strconv.Itoa(a.ID),
			Name:       a.Name,
			Home:       a.Home,
			Guest:      a.Guest,
			Restricted: a.Restricted,
		})
	}
	return result, nil
}

// PlexHistoryItem is a single entry from the PMS session history endpoint.
// It shares fields with plexItem but comes from the history API rather than the library API.
type PlexHistoryItem struct {
	RatingKey            string    `json:"ratingKey"`
	Key                  string    `json:"key"`
	Type                 string    `json:"type"`
	Title                string    `json:"title"`
	GrandparentTitle     string    `json:"grandparentTitle"`
	GrandparentRatingKey string    `json:"grandparentRatingKey"`
	ParentIndex          int       `json:"parentIndex"`
	Index                int       `json:"index"`
	Year                 int       `json:"year"`
	Duration             int64     `json:"duration"`
	ViewedAt             int64     `json:"viewedAt"`
	AccountID            int       `json:"accountID"`
	Guid                 PlexGuids `json:"Guid"`
}

// FetchUserHistory returns the complete watch history for a specific account on the
// Plex Media Server. Requires an admin token.
func (c *PlexClient) FetchUserHistory(ctx context.Context, baseURL, token, accountID string) ([]PlexHistoryItem, error) {
	var allItems []PlexHistoryItem
	offset := 0
	for {
		query := url.Values{}
		query.Set("accountID", accountID)
		query.Set("sort", "viewedAt:desc")
		query.Set("includeGuids", "1")
		query.Set("X-Plex-Container-Start", strconv.Itoa(offset))
		query.Set("X-Plex-Container-Size", strconv.Itoa(plexPageSize))
		reqURL := fmt.Sprintf("%s/status/sessions/history/all?%s", strings.TrimRight(baseURL, "/"), query.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		c.setPlexHeaders(req, token)
		var container struct {
			MediaContainer struct {
				Size      int               `json:"size"`
				TotalSize int               `json:"totalSize"`
				Metadata  []PlexHistoryItem `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := c.doJSON(req, &container); err != nil {
			return nil, fmt.Errorf("fetching Plex user history (account %s, offset %d): %w", accountID, offset, err)
		}
		allItems = append(allItems, container.MediaContainer.Metadata...)
		offset += len(container.MediaContainer.Metadata)
		if offset >= container.MediaContainer.TotalSize || len(container.MediaContainer.Metadata) == 0 {
			break
		}
	}
	return allItems, nil
}

func (c *PlexClient) Scrobble(ctx context.Context, baseURL, token, ratingKey string) error {
	query := url.Values{}
	query.Set("identifier", "com.plexapp.plugins.library")
	query.Set("key", ratingKey)
	reqURL := fmt.Sprintf("%s/:/scrobble?%s", strings.TrimRight(baseURL, "/"), query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	c.setPlexHeaders(req, token)
	if err := c.doJSON(req, nil); err != nil {
		return fmt.Errorf("scrobbling Plex item %s: %w", ratingKey, err)
	}
	return nil
}

func (c *PlexClient) Unscrobble(ctx context.Context, baseURL, token, ratingKey string) error {
	query := url.Values{}
	query.Set("identifier", "com.plexapp.plugins.library")
	query.Set("key", ratingKey)
	reqURL := fmt.Sprintf("%s/:/unscrobble?%s", strings.TrimRight(baseURL, "/"), query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	c.setPlexHeaders(req, token)
	if err := c.doJSON(req, nil); err != nil {
		return fmt.Errorf("unscrobbling Plex item %s: %w", ratingKey, err)
	}
	return nil
}

type PlexTimelineInput struct {
	RatingKey string
	Key       string
	State     string
	TimeMS    int64
	Duration  int64
	UpdatedMS int64
}

func (c *PlexClient) Timeline(ctx context.Context, baseURL, token string, input PlexTimelineInput) error {
	query := url.Values{}
	query.Set("ratingKey", input.RatingKey)
	if input.Key != "" {
		query.Set("key", input.Key)
	}
	if input.State == "" {
		input.State = "stopped"
	}
	query.Set("state", input.State)
	query.Set("time", strconv.FormatInt(input.TimeMS, 10))
	if input.Duration > 0 {
		query.Set("duration", strconv.FormatInt(input.Duration, 10))
	}
	if input.UpdatedMS > 0 {
		query.Set("updated", strconv.FormatInt(input.UpdatedMS, 10))
	}
	reqURL := fmt.Sprintf("%s/:/timeline?%s", strings.TrimRight(baseURL, "/"), query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return err
	}
	c.setPlexHeaders(req, token)
	if err := c.doJSON(req, nil); err != nil {
		return fmt.Errorf("sending Plex timeline for item %s: %w", input.RatingKey, err)
	}
	return nil
}
