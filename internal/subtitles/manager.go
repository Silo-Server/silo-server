// internal/subtitles/manager.go
package subtitles

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrSubtitleNotFound indicates the requested subtitle record does not exist.
	ErrSubtitleNotFound  = errors.New("subtitle not found")
	ErrSubtitleDuplicate = errors.New("subtitle content already stored")
	// ErrSubtitleLanguageConflict indicates the target language already has identical content.
	ErrSubtitleLanguageConflict = errors.New("subtitle with this language already exists for this file")
	// ErrUnknownProvider reports a provider key no registered subtitle provider
	// answers to. It is a client input problem, not an upstream failure, so the
	// API layer answers 404 instead of 500.
	ErrUnknownProvider = errors.New("unknown subtitle provider")
)

// Manager orchestrates subtitle search and download across providers.
type Manager struct {
	mu        sync.RWMutex
	providers map[string]Provider
	repo      Repository
	blobs     BlobStore
}

// NewManager creates a new subtitle manager.
func NewManager(repo Repository, blobs BlobStore) *Manager {
	return &Manager{
		providers: make(map[string]Provider),
		repo:      repo,
		blobs:     blobs,
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

// ProviderNames returns the names of every currently registered provider,
// sorted for a stable response. Empty when none are configured.
//
// The slice is always non-nil so callers can hand it straight to a JSON
// response without it marshalling as null — clients feature-detecting subtitle
// search read an empty list as "no providers here", not as a missing field.
func (m *Manager) ProviderNames() []string {
	m.mu.RLock()
	names := make([]string, 0, len(m.providers))
	for name := range m.providers {
		names = append(names, name)
	}
	m.mu.RUnlock()

	sort.Strings(names)
	return names
}

// Search fans out to all registered providers concurrently.
func (m *Manager) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	req.Languages = NormalizeBridgeSearchLanguages(req.Languages)
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
			// Provider adapters own protocol-to-language conversion. Keep the
			// manager payload untouched so the frozen v1 bridge response remains
			// compatible with legacy providers; v2 canonicalizes its projection.
			scored := pr.results[i]
			scored.Language = NormalizeProviderLanguage(scored.Provider, scored.Language)
			pr.results[i].Score = ScoreResult(scored, req)
		}
		resp.Results = append(resp.Results, pr.results...)
	}

	sort.Slice(resp.Results, func(i, j int) bool {
		return resp.Results[i].Score > resp.Results[j].Score
	})

	return resp, nil
}

// SearchBridge returns the legacy provider language payload for the frozen v1
// bridge while sharing the canonical search and scoring path.
func (m *Manager) SearchBridge(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	resp, err := m.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	for i := range resp.Results {
		if resp.Results[i].rawLanguage != "" {
			resp.Results[i].Language = resp.Results[i].rawLanguage
		}
	}
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
	// Publication requires atomic AI job fencing; nil is ordinary subtitle storage.
	Publication     *AIJobPublication
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

// Download fetches a subtitle from a provider and stores its bytes.
func (m *Manager) Download(ctx context.Context, req DownloadRequest) (*DownloadedSubtitle, error) {
	m.mu.RLock()
	prov, ok := m.providers[req.ProviderName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownProvider, req.ProviderName)
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

// Upload stores a user-provided subtitle file.
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

// SubtitleRevisionConflict reports the current row after a guarded write loses a race.
type SubtitleRevisionConflict struct{ Current *DownloadedSubtitle }

func (*SubtitleRevisionConflict) Error() string { return "subtitle revision changed" }

func subtitleContentHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// StoreSubtitle deduplicates logical content while every attempted publication
// owns a fresh physical object key. A failed or concurrent writer must never
// delete another publication's content.
func (m *Manager) StoreSubtitle(ctx context.Context, req StoreSubtitleRequest) (*DownloadedSubtitle, error) {
	req.Language = NormalizeProviderLanguage(req.Provider, req.Language)
	if req.Publication != nil {
		return m.storeAISubtitle(ctx, req)
	}
	sub := &DownloadedSubtitle{MediaFileID: req.MediaFileID, Provider: req.Provider, Language: req.Language, Format: req.Format,
		ReleaseName: req.ReleaseName, Score: req.Score, HearingImpaired: req.HearingImpaired, DownloadedBy: req.UserID, ContentSHA256: subtitleContentHash(req.Data)}
	existing, err := m.repo.GetDownloadedSubtitleByContent(ctx, sub)
	if err != nil {
		return nil, fmt.Errorf("check duplicate: %w", err)
	}
	if existing != nil {
		return existing, nil
	}
	// Legacy keys retain their old short digest. Verify actual bytes before
	// treating one as an identical subtitle; a 32-bit prefix is not identity.
	legacy, err := m.repo.GetDownloadedSubtitleByS3Key(ctx, buildSubtitleS3Key(req.MediaFileID, req.Language, req.Provider, req.Format, req.Data))
	if err != nil {
		return nil, fmt.Errorf("check legacy duplicate: %w", err)
	}
	if legacy != nil && legacy.Language == req.Language {
		data, err := m.blobs.Get(ctx, legacy.S3Key)
		if err != nil {
			return nil, fmt.Errorf("verify legacy subtitle: %w", err)
		}
		if subtitleContentHash(data) == sub.ContentSHA256 {
			return legacy, nil
		}
	}
	sub.S3Key = fmt.Sprintf("subtitles/%d/%s.%s", req.MediaFileID, uuid.NewString(), req.Format)
	if err := m.blobs.Put(ctx, sub.S3Key, req.Data); err != nil {
		return nil, fmt.Errorf("store subtitle object: %w", err)
	}
	if err := m.repo.InsertDownloadedSubtitle(ctx, sub); err != nil {
		if errors.Is(err, ErrSubtitleDuplicate) {
			// DO NOTHING confirms this candidate did not become a stored row.
			m.cleanupSubtitleObject(ctx, sub.S3Key)
			existing, lookupErr := m.repo.GetDownloadedSubtitleByContent(ctx, sub)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if existing == nil {
				return nil, ErrSubtitleNotFound
			}
			return existing, nil
		}
		// A lost database reply may conceal a committed row. Keep its unique
		// object rather than deleting content that may already be published.
		slog.ErrorContext(ctx, "subtitle publication outcome uncertain; retain object for reconciliation", "component", "subtitles", "object_key", sub.S3Key, "error", err)
		return nil, fmt.Errorf("insert subtitle record: %w", err)
	}
	return sub, nil
}

// UpdateDownloadedSubtitle changes metadata without moving immutable content.
func (m *Manager) UpdateDownloadedSubtitle(ctx context.Context, id int, patch SubtitleMetadataPatch) (*DownloadedSubtitle, error) {
	return m.UpdateDownloadedSubtitleWithRevision(ctx, id, patch, nil)
}

// UpdateDownloadedSubtitleWithRevision atomically merges only the supplied
// fields. A nonnil revision requires the same durable row version at the write.
func (m *Manager) UpdateDownloadedSubtitleWithRevision(ctx context.Context, id int, patch SubtitleMetadataPatch, revision *int64) (*DownloadedSubtitle, error) {
	sub, err := m.repo.GetDownloadedSubtitle(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("lookup subtitle: %w", err)
	}
	if sub == nil {
		return nil, ErrSubtitleNotFound
	}
	update := SubtitleMetadataUpdate{HearingImpaired: patch.HearingImpaired, ExpectedRevision: revision}
	if patch.Language != nil {
		language, err := NormalizeLanguageCode(*patch.Language)
		if err != nil {
			return nil, err
		}
		update.Language = new(language)
		// Backfill legacy content identity from bytes, never from the short key.
		if sub.ContentSHA256 == "" {
			data, err := m.blobs.Get(ctx, sub.S3Key)
			if err != nil {
				return nil, fmt.Errorf("fetch subtitle content: %w", err)
			}
			update.ContentSHA256 = subtitleContentHash(data)
		}
	}
	if patch.ReleaseName != nil {
		update.ReleaseName = new(strings.TrimSpace(*patch.ReleaseName))
	}
	updated, err := m.repo.UpdateDownloadedSubtitle(ctx, id, update)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrSubtitleNotFound
	}
	return updated, nil
}

func (m *Manager) cleanupSubtitleObject(ctx context.Context, key string) {
	if err := m.blobs.Delete(ctx, key); err != nil {
		slog.ErrorContext(ctx, "subtitle metadata removed; object cleanup needs reconciliation", "component", "subtitles", "object_key", key, "error", err)
	}
}

// GetSubtitleContent loads a downloaded subtitle record and its bytes.
func (m *Manager) GetSubtitleContent(ctx context.Context, id int) (*DownloadedSubtitle, []byte, error) {
	sub, err := m.repo.GetDownloadedSubtitle(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup subtitle: %w", err)
	}
	if sub == nil {
		return nil, nil, ErrSubtitleNotFound
	}

	data, err := m.blobs.Get(ctx, sub.S3Key)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch subtitle content: %w", err)
	}
	return sub, data, nil
}

// ListDownloadedSubtitles returns the downloaded subtitles stored for a media
// file (used to enumerate offline subtitle assets).
func (m *Manager) ListDownloadedSubtitles(ctx context.Context, mediaFileID int) ([]DownloadedSubtitle, error) {
	return m.repo.ListDownloadedSubtitles(ctx, mediaFileID)
}

// DeleteSubtitle removes the database row, then attempts object cleanup.
// Success means metadata is absent; object cleanup is best effort.
func (m *Manager) DeleteSubtitle(ctx context.Context, id int) error {
	sub, err := m.repo.DeleteDownloadedSubtitle(ctx, id)
	if err != nil {
		return fmt.Errorf("delete subtitle record: %w", err)
	}
	if sub == nil {
		return nil
	}
	m.cleanupSubtitleObject(ctx, sub.S3Key)
	return nil
}
