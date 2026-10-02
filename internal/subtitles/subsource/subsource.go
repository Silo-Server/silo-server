// internal/subtitles/subsource/subsource.go
package subsource

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
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const defaultBaseURL = "https://api.subsource.net/api/v1"

// languageMap maps ISO 639-1 codes to full English names used by SubSource.
var languageMap = map[string]string{
	"en": "english", "es": "spanish", "fr": "french", "de": "german",
	"it": "italian", "pt": "portuguese", "pt-BR": "brazillian portuguese", "nl": "dutch", "pl": "polish",
	"sv": "swedish", "no": "norwegian", "da": "danish", "fi": "finnish",
	"ru": "russian", "uk": "ukrainian", "cs": "czech", "sk": "slovak",
	"hu": "hungarian", "ro": "romanian", "bg": "bulgarian", "hr": "croatian",
	"sl": "slovenian", "sr": "serbian", "tr": "turkish", "el": "greek",
	"he": "hebrew", "ar": "arabic", "zh": "chinese", "ja": "japanese",
	"ko": "korean", "vi": "vietnamese", "th": "thai", "id": "indonesian",
	"ms": "malay",
}

// Config holds the configuration for the SubSource provider.
type Config struct {
	APIKey  string
	BaseURL string // override for testing
}

// Provider implements subtitles.Provider for SubSource.net.
type Provider struct {
	client  *http.Client
	baseURL string
	apiKey  string
	limiter *subtitles.RateLimiter
}

// New creates a new SubSource provider.
func New(cfg Config) *Provider {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	return &Provider{
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: base,
		apiKey:  cfg.APIKey,
		limiter: subtitles.NewRateLimiter(subtitles.RateLimiterConfig{
			MaxRequests: 40,
			Window:      10 * time.Second,
		}),
	}
}

func (p *Provider) Name() string { return "subsource" }

func (p *Provider) Search(ctx context.Context, req subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	// Step 1: Search for the movie/show
	params := url.Values{}
	if req.IMDbID != "" {
		params.Set("searchType", "imdb")
		params.Set("imdb", req.IMDbID)
	} else {
		params.Set("searchType", "text")
		params.Set("q", req.Title)
	}
	if req.Season > 0 {
		params.Set("type", "series")
		params.Set("season", strconv.Itoa(req.Season))
	} else {
		params.Set("type", "movie")
	}
	if req.Year > 0 {
		params.Set("year", strconv.Itoa(req.Year))
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/movies/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("subsource: build search request: %w", err)
	}
	httpReq.Header.Set("X-API-Key", p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("subsource: search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("subsource: search returned %d: %s", resp.StatusCode, string(body))
	}

	var movieResp movieSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&movieResp); err != nil {
		return nil, fmt.Errorf("subsource: decode search response: %w", err)
	}

	if !movieResp.Success || len(movieResp.Data) == 0 {
		return nil, nil
	}

	// Pick the first matching result
	movieID := movieResp.Data[0].MovieID

	// Step 2: Get subtitles for this movie
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	subParams := url.Values{}
	subParams.Set("movieId", strconv.Itoa(movieID))
	subParams.Set("limit", "100")
	subParams.Set("sort", "popular")

	// Map ISO languages to full names
	if len(req.Languages) > 0 {
		lang := req.Languages[0]
		if fullName, ok := languageMap[lang]; ok {
			subParams.Set("language", fullName)
		} else {
			subParams.Set("language", lang)
		}
	}

	subReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/subtitles?"+subParams.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("subsource: build subtitle request: %w", err)
	}
	subReq.Header.Set("X-API-Key", p.apiKey)

	subResp, err := p.client.Do(subReq)
	if err != nil {
		return nil, fmt.Errorf("subsource: subtitle request: %w", err)
	}
	defer subResp.Body.Close()

	if subResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(subResp.Body)
		return nil, fmt.Errorf("subsource: subtitle list returned %d: %s", subResp.StatusCode, string(body))
	}

	var subListResp subtitleListResponse
	if err := json.NewDecoder(subResp.Body).Decode(&subListResp); err != nil {
		return nil, fmt.Errorf("subsource: decode subtitle list: %w", err)
	}

	// If multiple languages requested, fetch remaining languages
	var extraSubs []subtitleEntry
	if len(req.Languages) > 1 {
		for _, lang := range req.Languages[1:] {
			extra, err := p.fetchSubtitles(ctx, movieID, lang)
			if err != nil {
				continue // best-effort for additional languages
			}
			extraSubs = append(extraSubs, extra...)
		}
	}

	allSubs := append(subListResp.Data, extraSubs...)
	results := make([]subtitles.SubtitleResult, 0, len(allSubs))
	for _, s := range allSubs {
		lang := reverseLanguageMap(s.Language)
		releaseName := strings.Join(s.ReleaseInfo, " ")
		format := detectFormat(releaseName)
		result := subtitles.SubtitleResult{
			ID:              strconv.Itoa(s.SubtitleID),
			Provider:        "subsource",
			Language:        subtitles.NormalizeProviderLanguage("subsource", lang),
			ReleaseName:     releaseName,
			Format:          format,
			Downloads:       s.Downloads,
			HearingImpaired: s.HearingImpaired,
		}
		result.SetRawLanguageForBridge(lang)
		results = append(results, result)
	}
	return results, nil
}

// fetchSubtitles fetches subtitles for a single language.
func (p *Provider) fetchSubtitles(ctx context.Context, movieID int, lang string) ([]subtitleEntry, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	subParams := url.Values{}
	subParams.Set("movieId", strconv.Itoa(movieID))
	subParams.Set("limit", "100")
	subParams.Set("sort", "popular")
	if fullName, ok := languageMap[lang]; ok {
		subParams.Set("language", fullName)
	} else {
		subParams.Set("language", lang)
	}

	subReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/subtitles?"+subParams.Encode(), nil)
	if err != nil {
		return nil, err
	}
	subReq.Header.Set("X-API-Key", p.apiKey)

	subResp, err := p.client.Do(subReq)
	if err != nil {
		return nil, err
	}
	defer subResp.Body.Close()

	if subResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subsource: subtitle list returned %d", subResp.StatusCode)
	}

	var resp subtitleListResponse
	if err := json.NewDecoder(subResp.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (p *Provider) Download(ctx context.Context, id string) ([]byte, subtitles.SubtitleFormat, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/subtitles/"+id+"/download", nil)
	if err != nil {
		return nil, "", fmt.Errorf("subsource: build download request: %w", err)
	}
	httpReq.Header.Set("X-API-Key", p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("subsource: download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("subsource: download returned %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("subsource: read download: %w", err)
	}

	// API returns a ZIP; extract the first subtitle file
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "zip") {
		return extractSubtitleFromZip(data)
	}

	// Detect format from content-disposition or default to srt
	contentDisp := resp.Header.Get("Content-Disposition")
	format := detectFormatFromHeader(contentDisp)
	return data, format, nil
}

// extractSubtitleFromZip finds the first subtitle file in a ZIP archive.
func extractSubtitleFromZip(data []byte) ([]byte, subtitles.SubtitleFormat, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("subsource: open zip: %w", err)
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
				return nil, "", fmt.Errorf("subsource: open zip entry: %w", err)
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, "", fmt.Errorf("subsource: read zip entry: %w", err)
			}
			return content, format, nil
		}
	}

	return nil, "", fmt.Errorf("subsource: no subtitle file found in zip archive")
}

func reverseLanguageMap(name string) string {
	for code, fullName := range languageMap {
		if strings.EqualFold(fullName, name) {
			return code
		}
	}
	if strings.EqualFold(name, "brazilian portuguese") {
		return "pt-BR"
	}
	return strings.ToLower(name)
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

func detectFormatFromHeader(contentDisposition string) subtitles.SubtitleFormat {
	if contentDisposition == "" {
		return subtitles.FormatSRT
	}
	parts := strings.Split(contentDisposition, "filename=")
	if len(parts) > 1 {
		filename := strings.Trim(parts[1], "\"' ")
		return detectFormat(filename)
	}
	return subtitles.FormatSRT
}
