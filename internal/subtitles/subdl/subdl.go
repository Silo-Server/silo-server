// internal/subtitles/subdl/subdl.go
package subdl

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

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
		client:    &http.Client{Timeout: 15 * time.Second},
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
		return nil, fmt.Errorf("subdl: search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("subdl: search returned %d: %s", resp.StatusCode, string(body))
	}

	var searchResp searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
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
			ID:              s.URL, // relative download path
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
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, "", err
	}

	// id is the relative download URL from search results
	downloadURL := p.dlBaseURL + id

	httpReq, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("subdl: build download request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("subdl: download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("subdl: download returned %d: %s", resp.StatusCode, string(body))
	}

	zipData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("subdl: read zip: %w", err)
	}

	return extractSubtitleFromZip(zipData)
}

// extractSubtitleFromZip finds the first subtitle file in a ZIP archive.
func extractSubtitleFromZip(data []byte) ([]byte, subtitles.SubtitleFormat, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("subdl: open zip: %w", err)
	}

	subtitleExts := map[string]subtitles.SubtitleFormat{
		".srt": subtitles.FormatSRT,
		".ass": subtitles.FormatASS,
		".ssa": subtitles.FormatSSA,
		".vtt": subtitles.FormatVTT,
		".sub": subtitles.FormatSUB,
	}

	for _, f := range reader.File {
		ext := strings.ToLower(filepath.Ext(f.Name))
		if format, ok := subtitleExts[ext]; ok {
			rc, err := f.Open()
			if err != nil {
				return nil, "", fmt.Errorf("subdl: open zip entry: %w", err)
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, "", fmt.Errorf("subdl: read zip entry: %w", err)
			}
			return content, format, nil
		}
	}

	return nil, "", fmt.Errorf("subdl: no subtitle file found in zip archive")
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
