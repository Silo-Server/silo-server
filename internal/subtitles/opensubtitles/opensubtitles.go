// internal/subtitles/opensubtitles/opensubtitles.go
package opensubtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const (
	defaultBaseURL   = "https://api.opensubtitles.com/api/v1"
	defaultAPIKey    = "Hsn0IpAAGNFVIbAvK0gtJqCi8lAYuugT"
	defaultUserAgent = "Silo v1.0"
)

// Config holds the configuration for the OpenSubtitles provider.
type Config struct {
	Username string
	Password string
	BaseURL  string // override for testing
}

// Provider implements subtitles.Provider for OpenSubtitles.com.
type Provider struct {
	client   *http.Client
	baseURL  string
	username string
	password string
	limiter  *subtitles.RateLimiter

	mu    sync.Mutex
	token string
}

// New creates a new OpenSubtitles provider.
func New(cfg Config) *Provider {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	return &Provider{
		client:   &http.Client{Timeout: 15 * time.Second},
		baseURL:  base,
		username: cfg.Username,
		password: cfg.Password,
		limiter: subtitles.NewRateLimiter(subtitles.RateLimiterConfig{
			MaxRequests: 40,
			Window:      10 * time.Second,
		}),
	}
}

func (p *Provider) Name() string { return "opensubtitles" }

func (p *Provider) Search(ctx context.Context, req subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	// OpenSubtitles permits anonymous searches, but configured credentials must
	// be checked here so the admin connection test cannot report bogus login
	// details as healthy.
	if p.username != "" || p.password != "" {
		if _, err := p.ensureToken(ctx); err != nil {
			return nil, fmt.Errorf("opensubtitles: credential validation failed: %w", err)
		}
	}

	params := url.Values{}
	if req.IMDbID != "" {
		params.Set("imdb_id", req.IMDbID)
	}
	if len(req.Languages) > 0 {
		params.Set("languages", strings.Join(req.Languages, ","))
	}
	if req.Season > 0 {
		params.Set("season_number", strconv.Itoa(req.Season))
		params.Set("episode_number", strconv.Itoa(req.Episode))
		params.Set("type", "episode")
	} else {
		params.Set("type", "movie")
	}
	if req.FileHash != "" {
		params.Set("moviehash", req.FileHash)
	}
	if req.Title != "" && req.IMDbID == "" {
		params.Set("query", req.Title)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/subtitles?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: build request: %w", err)
	}
	httpReq.Header.Set("Api-Key", defaultAPIKey)
	httpReq.Header.Set("User-Agent", defaultUserAgent)
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("opensubtitles: search returned %d: %s", resp.StatusCode, string(body))
	}

	var searchResp searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("opensubtitles: decode response: %w", err)
	}

	results := make([]subtitles.SubtitleResult, 0, len(searchResp.Data))
	for _, d := range searchResp.Data {
		if len(d.Attributes.Files) == 0 {
			continue
		}
		format := detectFormat(d.Attributes.Files[0].FileName)
		result := subtitles.SubtitleResult{
			ID:              strconv.Itoa(d.Attributes.Files[0].FileID),
			Provider:        "opensubtitles",
			Language:        subtitles.NormalizeProviderLanguage("opensubtitles", d.Attributes.Language),
			ReleaseName:     d.Attributes.Release,
			Format:          format,
			Downloads:       d.Attributes.DownloadCount,
			HearingImpaired: d.Attributes.HearingImpaired,
		}
		resultRaw := d.Attributes.Language
		result.SetRawLanguageForBridge(resultRaw)
		results = append(results, result)
	}
	return results, nil
}

func (p *Provider) Download(ctx context.Context, id string) ([]byte, subtitles.SubtitleFormat, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, "", err
	}

	token, err := p.ensureToken(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: login required for downloads: %w", err)
	}

	fileID, err := strconv.Atoi(id)
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: invalid file id: %w", err)
	}

	body, _ := json.Marshal(downloadRequest{FileID: fileID})
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/download", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: build download request: %w", err)
	}
	httpReq.Header.Set("Api-Key", defaultAPIKey)
	httpReq.Header.Set("User-Agent", defaultUserAgent)
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: download request: %w", err)
	}
	defer resp.Body.Close()

	// If token expired, re-login and retry once
	if resp.StatusCode == http.StatusUnauthorized {
		p.mu.Lock()
		p.token = ""
		p.mu.Unlock()

		token, err = p.ensureToken(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("opensubtitles: re-login failed: %w", err)
		}
		return p.downloadWithToken(ctx, fileID, token)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("opensubtitles: download returned %d: %s", resp.StatusCode, string(respBody))
	}

	return p.fetchDownloadLink(ctx, resp.Body)
}

func (p *Provider) downloadWithToken(ctx context.Context, fileID int, token string) ([]byte, subtitles.SubtitleFormat, error) {
	body, _ := json.Marshal(downloadRequest{FileID: fileID})
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/download", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: build download request: %w", err)
	}
	httpReq.Header.Set("Api-Key", defaultAPIKey)
	httpReq.Header.Set("User-Agent", defaultUserAgent)
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("opensubtitles: download returned %d: %s", resp.StatusCode, string(respBody))
	}

	return p.fetchDownloadLink(ctx, resp.Body)
}

func (p *Provider) fetchDownloadLink(ctx context.Context, body io.Reader) ([]byte, subtitles.SubtitleFormat, error) {
	var dlResp downloadResponse
	if err := json.NewDecoder(body).Decode(&dlResp); err != nil {
		return nil, "", fmt.Errorf("opensubtitles: decode download response: %w", err)
	}

	format := detectFormat(dlResp.FileName)

	fileReq, err := http.NewRequestWithContext(ctx, "GET", dlResp.Link, nil)
	if err != nil {
		return nil, "", err
	}
	fileResp, err := p.client.Do(fileReq)
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: fetch file: %w", err)
	}
	defer fileResp.Body.Close()

	data, err := io.ReadAll(fileResp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("opensubtitles: read file: %w", err)
	}
	return data, format, nil
}

// ensureToken returns a cached JWT token or logs in to obtain a new one.
func (p *Provider) ensureToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" {
		return p.token, nil
	}

	if p.username == "" || p.password == "" {
		return "", fmt.Errorf("username and password required")
	}

	body, _ := json.Marshal(loginRequest{Username: p.username, Password: p.password})
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/login", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build login request: %w", err)
	}
	httpReq.Header.Set("Api-Key", defaultAPIKey)
	httpReq.Header.Set("User-Agent", defaultUserAgent)
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login returned %d: %s", resp.StatusCode, string(respBody))
	}

	var loginResp loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}

	p.token = loginResp.Token
	return p.token, nil
}

func detectFormat(filename string) subtitles.SubtitleFormat {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	switch ext {
	case "srt":
		return subtitles.FormatSRT
	case "ass":
		return subtitles.FormatASS
	case "ssa":
		return subtitles.FormatSSA
	case "vtt":
		return subtitles.FormatVTT
	case "sub":
		return subtitles.FormatSUB
	default:
		return subtitles.FormatSRT
	}
}
