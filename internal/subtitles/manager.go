// internal/subtitles/manager.go
package subtitles

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrSubtitleNotFound indicates the requested subtitle record does not exist.
	ErrSubtitleNotFound = errors.New("subtitle not found")
	// ErrSubtitleLanguageConflict indicates another subtitle already uses the target S3 key.
	ErrSubtitleLanguageConflict = errors.New("subtitle with this language already exists for this file")
)

// Manager orchestrates subtitle search and download across providers.
type Manager struct {
	mu        sync.RWMutex
	providers map[string]Provider
	repo      Repository
	s3        S3Client
	s3Bucket  string
}

// NewManager creates a new subtitle manager.
func NewManager(repo Repository, s3 S3Client, s3Bucket string) *Manager {
	return &Manager{
		providers: make(map[string]Provider),
		repo:      repo,
		s3:        s3,
		s3Bucket:  s3Bucket,
	}
}

// RegisterProvider adds or replaces a provider.
func (m *Manager) RegisterProvider(p Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[p.Name()] = p
}

// RemoveProvider removes a provider by name.
func (m *Manager) RemoveProvider(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.providers, name)
}

// Search fans out to all registered providers concurrently.
func (m *Manager) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	m.mu.RLock()
	providers := make([]Provider, 0, len(m.providers))
	for _, p := range m.providers {
		providers = append(providers, p)
	}
	m.mu.RUnlock()

	if len(providers) == 0 {
		return &SearchResponse{}, nil
	}

	type providerResult struct {
		results []SubtitleResult
		warning string
	}

	ch := make(chan providerResult, len(providers))
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Add(1)
		go func(prov Provider) {
			defer wg.Done()

			timeout := 20 * time.Second
			if prov.Name() == "subsource" {
				timeout = 30 * time.Second
			}
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			results, err := prov.Search(pctx, req)
			if err != nil {
				ch <- providerResult{warning: fmt.Sprintf("%s: %v", prov.Name(), err)}
				return
			}
			ch <- providerResult{results: results}
		}(p)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	resp := &SearchResponse{}
	for pr := range ch {
		if pr.warning != "" {
			resp.Warnings = append(resp.Warnings, pr.warning)
			continue
		}
		for i := range pr.results {
			pr.results[i].Score = ScoreResult(pr.results[i], req)
		}
		resp.Results = append(resp.Results, pr.results...)
	}

	sort.Slice(resp.Results, func(i, j int) bool {
		return resp.Results[i].Score > resp.Results[j].Score
	})

	return resp, nil
}

// DownloadRequest contains metadata from the search result the user selected.
type DownloadRequest struct {
	ProviderName    string
	SubtitleID      string
	MediaFileID     int
	UserID          *int
	Language        string
	ReleaseName     string
	Score           float64
	HearingImpaired bool
}

// StoreSubtitleRequest contains metadata and content for persisting a subtitle.
type StoreSubtitleRequest struct {
	MediaFileID     int
	UserID          *int
	Provider        string
	Language        string
	Format          SubtitleFormat
	ReleaseName     string
	Score           float64
	HearingImpaired bool
	Data            []byte
}

// UploadRequest contains metadata for a user-uploaded subtitle file.
type UploadRequest struct {
	MediaFileID        int
	UserID             *int
	Language           string
	PreferUserLanguage bool
	Filename           string
	ReleaseName        string
	HearingImpaired    bool
	Data               []byte
}

// Download fetches a subtitle from a provider and stores it in S3.
func (m *Manager) Download(ctx context.Context, req DownloadRequest) (*DownloadedSubtitle, error) {
	m.mu.RLock()
	prov, ok := m.providers[req.ProviderName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", req.ProviderName)
	}

	data, format, err := prov.Download(ctx, req.SubtitleID)
	if err != nil {
		return nil, fmt.Errorf("download from %s: %w", req.ProviderName, err)
	}

	return m.StoreSubtitle(ctx, StoreSubtitleRequest{
		MediaFileID:     req.MediaFileID,
		UserID:          req.UserID,
		Provider:        req.ProviderName,
		Language:        req.Language,
		Format:          format,
		ReleaseName:     req.ReleaseName,
		Score:           req.Score,
		HearingImpaired: req.HearingImpaired,
		Data:            data,
	})
}

// Upload stores a user-provided subtitle file in S3.
func (m *Manager) Upload(ctx context.Context, req UploadRequest) (*DownloadedSubtitle, error) {
	if len(req.Data) == 0 {
		return nil, fmt.Errorf("empty subtitle file")
	}
	if len(req.Data) > MaxUploadSize {
		return nil, fmt.Errorf("subtitle file exceeds maximum size of %d bytes", MaxUploadSize)
	}

	format, err := FormatFromFilename(req.Filename)
	if err != nil {
		return nil, err
	}

	detected, err := ResolveUploadLanguage(req.Filename, format, req.Data, req.Language, req.PreferUserLanguage)
	if err != nil {
		return nil, err
	}

	releaseName := req.ReleaseName
	if releaseName == "" {
		releaseName = req.Filename
	}

	return m.StoreSubtitle(ctx, StoreSubtitleRequest{
		MediaFileID:     req.MediaFileID,
		UserID:          req.UserID,
		Provider:        ProviderUpload,
		Language:        detected.Language,
		Format:          format,
		ReleaseName:     releaseName,
		Score:           0,
		HearingImpaired: req.HearingImpaired,
		Data:            req.Data,
	})
}

func buildSubtitleS3Key(mediaFileID int, language, provider string, format SubtitleFormat, data []byte) string {
	hash := fmt.Sprintf("%x", sha256.Sum256(data))[:8]
	return fmt.Sprintf("subtitles/%d/%s_%s_%s.%s", mediaFileID, language, provider, hash, format)
}

// SubtitleMetadataPatch contains optional metadata updates for a downloaded subtitle.
type SubtitleMetadataPatch struct {
	Language        *string
	ReleaseName     *string
	HearingImpaired *bool
}

// StoreSubtitle uploads subtitle content to S3 and records it in the database.
func (m *Manager) StoreSubtitle(ctx context.Context, req StoreSubtitleRequest) (*DownloadedSubtitle, error) {
	s3Key := buildSubtitleS3Key(req.MediaFileID, req.Language, req.Provider, req.Format, req.Data)

	existing, err := m.repo.GetDownloadedSubtitleByS3Key(ctx, s3Key)
	if err != nil {
		return nil, fmt.Errorf("check duplicate: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	if err := m.s3.PutObject(ctx, m.s3Bucket, s3Key, req.Data); err != nil {
		return nil, fmt.Errorf("upload to s3: %w", err)
	}

	sub := &DownloadedSubtitle{
		MediaFileID:     req.MediaFileID,
		Provider:        req.Provider,
		Language:        req.Language,
		Format:          req.Format,
		ReleaseName:     req.ReleaseName,
		S3Key:           s3Key,
		Score:           req.Score,
		HearingImpaired: req.HearingImpaired,
		DownloadedBy:    req.UserID,
	}
	if err := m.repo.InsertDownloadedSubtitle(ctx, sub); err != nil {
		_ = m.s3.DeleteObject(ctx, m.s3Bucket, s3Key)
		return nil, fmt.Errorf("insert subtitle record: %w", err)
	}

	return sub, nil
}

// UpdateDownloadedSubtitle updates subtitle metadata and migrates S3 keys when language changes.
func (m *Manager) UpdateDownloadedSubtitle(ctx context.Context, id int, patch SubtitleMetadataPatch) (*DownloadedSubtitle, error) {
	sub, err := m.repo.GetDownloadedSubtitle(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("lookup subtitle: %w", err)
	}
	if sub == nil {
		return nil, ErrSubtitleNotFound
	}

	language := sub.Language
	if patch.Language != nil {
		normalized, err := NormalizeLanguageCode(*patch.Language)
		if err != nil {
			return nil, err
		}
		language = normalized
	}

	releaseName := sub.ReleaseName
	if patch.ReleaseName != nil {
		releaseName = strings.TrimSpace(*patch.ReleaseName)
	}

	hearingImpaired := sub.HearingImpaired
	if patch.HearingImpaired != nil {
		hearingImpaired = *patch.HearingImpaired
	}

	newS3Key := sub.S3Key
	if language != sub.Language {
		data, err := m.s3.GetObject(ctx, m.s3Bucket, sub.S3Key)
		if err != nil {
			return nil, fmt.Errorf("fetch subtitle content: %w", err)
		}
		newS3Key = buildSubtitleS3Key(sub.MediaFileID, language, sub.Provider, sub.Format, data)

		existing, err := m.repo.GetDownloadedSubtitleByS3Key(ctx, newS3Key)
		if err != nil {
			return nil, fmt.Errorf("check duplicate: %w", err)
		}
		if existing != nil && existing.ID != id {
			return nil, ErrSubtitleLanguageConflict
		}

		if newS3Key != sub.S3Key {
			if err := m.s3.PutObject(ctx, m.s3Bucket, newS3Key, data); err != nil {
				return nil, fmt.Errorf("upload migrated subtitle: %w", err)
			}
		}
	}

	updated, err := m.repo.UpdateDownloadedSubtitle(ctx, id, SubtitleMetadataUpdate{
		Language:        language,
		ReleaseName:     releaseName,
		HearingImpaired: hearingImpaired,
		S3Key:           newS3Key,
	})
	if err != nil {
		if newS3Key != sub.S3Key {
			_ = m.s3.DeleteObject(ctx, m.s3Bucket, newS3Key)
		}
		return nil, err
	}
	if updated == nil {
		return nil, ErrSubtitleNotFound
	}

	if newS3Key != sub.S3Key {
		_ = m.s3.DeleteObject(ctx, m.s3Bucket, sub.S3Key)
	}

	return updated, nil
}

// GetSubtitleContent loads a downloaded subtitle record and its S3 bytes.
func (m *Manager) GetSubtitleContent(ctx context.Context, id int) (*DownloadedSubtitle, []byte, error) {
	sub, err := m.repo.GetDownloadedSubtitle(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup subtitle: %w", err)
	}
	if sub == nil {
		return nil, nil, ErrSubtitleNotFound
	}

	data, err := m.s3.GetObject(ctx, m.s3Bucket, sub.S3Key)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch subtitle content: %w", err)
	}
	return sub, data, nil
}

// DeleteSubtitle removes a downloaded subtitle from both DB and S3.
func (m *Manager) DeleteSubtitle(ctx context.Context, id int) error {
	sub, err := m.repo.DeleteDownloadedSubtitle(ctx, id)
	if err != nil {
		return fmt.Errorf("delete subtitle record: %w", err)
	}
	if sub == nil {
		return nil
	}
	_ = m.s3.DeleteObject(ctx, m.s3Bucket, sub.S3Key)
	return nil
}
