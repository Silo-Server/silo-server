// internal/subtitles/subdl/subdl.go
package subdl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const (
	defaultBaseURL   = "https://api.subdl.com/api/v1"
	defaultDLBaseURL = "https://dl.subdl.com"
)

// Config holds the configuration for the SubDL provider.
type Config struct {
	APIKey    string
	BaseURL   string // override for testing
	DLBaseURL string // override for testing
}

// Provider implements subtitles.Provider for SubDL.com.
type Provider struct {
	client    *http.Client
	baseURL   string
	dlBaseURL string
	apiKey    string
	limiter   *subtitles.RateLimiter
}

// New creates a new SubDL provider.
func New(cfg Config) *Provider {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	dlBase := cfg.DLBaseURL
	if dlBase == "" {
		dlBase = defaultDLBaseURL
	}
	return &Provider{
		client:    &http.Client{Timeout: 15 * time.Second, CheckRedirect: sameOriginRedirects},
		baseURL:   base,
		dlBaseURL: dlBase,
		apiKey:    cfg.APIKey,
		limiter: subtitles.NewRateLimiter(subtitles.RateLimiterConfig{
			MaxRequests: 40,
			Window:      10 * time.Second,
		}),
	}
}

func (p *Provider) Name() string { return "subdl" }

func (p *Provider) Search(ctx context.Context, req subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("api_key", p.apiKey)
	if req.IMDbID != "" {
		params.Set("imdb_id", req.IMDbID)
	}
	if len(req.Languages) > 0 {
		codes := make([]string, 0, len(req.Languages))
		for _, language := range req.Languages {
			codes = append(codes, subtitles.SubDLLanguageCode(language))
		}
		params.Set("languages", strings.Join(codes, ","))
	}
	if req.Season > 0 {
		params.Set("type", "tv")
		params.Set("season_number", fmt.Sprintf("%d", req.Season))
		params.Set("episode_number", fmt.Sprintf("%d", req.Episode))
	} else {
		params.Set("type", "movie")
	}
	if req.Filename != "" {
		params.Set("file_name", req.Filename)
	}
	if req.Title != "" && req.IMDbID == "" {
		params.Set("film_name", req.Title)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/subtitles?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("subdl: build request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		// The request URL carries the API key; keep it out of the error text.
		return nil, fmt.Errorf("subdl: search request: %w", logredact.SanitizeURLError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subdl: search returned %d: %s", resp.StatusCode, subtitles.ProviderErrorBody(resp.Body))
	}

	var searchResp searchResponse
	if err := json.NewDecoder(subtitles.LimitProviderResponse(resp.Body)).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("subdl: decode response: %w", err)
	}

	results := make([]subtitles.SubtitleResult, 0, len(searchResp.Subtitles))
	for _, s := range searchResp.Subtitles {
		// Skip results that don't match the requested episode
		if req.Episode > 0 && s.Episode > 0 && s.Episode != req.Episode {
			continue
		}
		rawLanguage := s.Language
		if strings.TrimSpace(rawLanguage) == "" {
			rawLanguage = s.Lang
		}
		language := subtitles.NormalizeProviderLanguage("subdl", rawLanguage)
		format := detectFormat(s.ReleaseName)
		result := subtitles.SubtitleResult{
			ID:              resultID(s.URL),
			Provider:        "subdl",
			Language:        language,
			ReleaseName:     s.ReleaseName,
			Format:          format,
			Downloads:       s.DownloadCount,
			HearingImpaired: s.HearingImpaired,
		}
		results = append(results, result)
	}
	return results, nil
}

func (p *Provider) Download(ctx context.Context, id string) ([]byte, subtitles.SubtitleFormat, error) {
	downloadURL, err := p.downloadURL(id)
	if err != nil {
		return nil, "", err
	}
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("subdl: build download request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("subdl: download request: %w", logredact.SanitizeURLError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("subdl: download returned %d: %s", resp.StatusCode, subtitles.ProviderErrorBody(resp.Body))
	}

	zipData, err := subtitles.ReadProviderResponse(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("subdl: read zip: %w", err)
	}

	content, format, err := subtitles.ExtractSubtitleFromZip(zipData)
	if err != nil {
		return nil, "", fmt.Errorf("subdl: %w", err)
	}
	return content, format, nil
}

func detectFormat(filename string) subtitles.SubtitleFormat {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".srt":
		return subtitles.FormatSRT
	case ".ass":
		return subtitles.FormatASS
	case ".ssa":
		return subtitles.FormatSSA
	case ".vtt":
		return subtitles.FormatVTT
	case ".sub":
		return subtitles.FormatSUB
	default:
		return subtitles.FormatSRT
	}
}
