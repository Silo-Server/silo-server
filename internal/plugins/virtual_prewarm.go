package plugins

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// prewarmProbeBudget bounds ffprobe inside a pre-warm job. It mirrors the
// handler's known-codec probe budget; a slow provider probe must not tie up a
// pre-warm slot for long.
const prewarmProbeBudget = 15 * time.Second

// DeviceCapabilities describes what a client device can play directly
// without transcoding. Extracted from the playback/start request.
type DeviceCapabilities struct {
	CodecsVideo   []string
	CodecsAudio   []string
	Containers    []string
	MaxResolution string // "1080p", "4K", etc.
	HDR           bool
}

// PrewarmRequest describes a single pre-warm job.
type PrewarmRequest struct {
	ContentID string
	FileID    int
	FilePath  string
	ProfileID string
	UserID    int
	OwnerID   int
	Device    DeviceCapabilities
}

// PrewarmResult holds the pre-warmed state for a content+profile pair.
type PrewarmResult struct {
	ResolvedURL string
	ProbedFile  *models.MediaFile
	StreamURI   string
	SelectedIdx int
	ResolvedAt  time.Time
	ExpiresAt   time.Time
}

// PrewarmResolver resolves virtual playback sources for pre-warming.
type PrewarmResolver interface {
	ResolveForPrewarm(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) (string, *models.MediaFile, error)
	ListCandidates(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error)
}

// PrewarmCache is an in-memory cache for pre-warmed results.
type PrewarmCache struct {
	mu         sync.RWMutex
	entries    map[string]*PrewarmResult
	maxEntries int
	ttl        time.Duration
}

func NewPrewarmCache(maxEntries int, ttl time.Duration) *PrewarmCache {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &PrewarmCache{
		entries:    make(map[string]*PrewarmResult),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

func prewarmCacheKey(contentID, profileID string) string {
	return contentID + "\x00" + profileID
}

func (c *PrewarmCache) Get(contentID, profileID string) *PrewarmResult {
	key := prewarmCacheKey(contentID, profileID)
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		return nil
	}
	return entry
}

func (c *PrewarmCache) Set(contentID, profileID string, result *PrewarmResult) {
	key := prewarmCacheKey(contentID, profileID)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Evict expired entries
	now := time.Now()
	for k, v := range c.entries {
		if now.After(v.ExpiresAt) {
			delete(c.entries, k)
		}
	}
	// Evict oldest if full
	for len(c.entries) >= c.maxEntries {
		var oldestKey string
		var oldest time.Time
		for k, v := range c.entries {
			if oldestKey == "" || v.ResolvedAt.Before(oldest) {
				oldestKey = k
				oldest = v.ResolvedAt
			}
		}
		delete(c.entries, oldestKey)
	}
	result.ExpiresAt = now.Add(c.ttl)
	c.entries[key] = result
}

func (c *PrewarmCache) Clear() {
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

// PrewarmService runs background pre-warming of virtual playback sources.
type PrewarmService struct {
	resolver  PrewarmResolver
	cache     *PrewarmCache
	probeFunc func(ctx context.Context, sourceURL string, file *models.MediaFile) (*models.MediaFile, error)
	enabled   func() bool // reads server setting

	// pending deduplicates in-flight pre-warms for the same content
	mu      sync.Mutex
	pending map[string]context.CancelFunc
}

func NewPrewarmService(
	resolver PrewarmResolver,
	cache *PrewarmCache,
	probeFunc func(ctx context.Context, sourceURL string, file *models.MediaFile) (*models.MediaFile, error),
	enabled func() bool,
) *PrewarmService {
	return &PrewarmService{
		resolver:  resolver,
		cache:     cache,
		probeFunc: probeFunc,
		enabled:   enabled,
		pending:   make(map[string]context.CancelFunc),
	}
}

// TriggerPrewarm starts a background pre-warm for the given content.
// It is a no-op when pre-warming is disabled, already cached, or already in-flight.
func (s *PrewarmService) TriggerPrewarm(req PrewarmRequest) {
	if s == nil || !s.enabled() {
		return
	}
	// Already cached?
	if existing := s.cache.Get(req.ContentID, req.ProfileID); existing != nil {
		return
	}
	// Deduplicate
	dedupKey := req.ContentID + "\x00" + req.ProfileID
	s.mu.Lock()
	if _, ok := s.pending[dedupKey]; ok {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	s.pending[dedupKey] = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.pending, dedupKey)
			s.mu.Unlock()
			cancel()
		}()
		s.runPrewarm(ctx, req)
	}()
}

// Get returns the cached pre-warm result for a content+profile pair, or nil
// when the feature is unwired, the entry is missing, or it has expired.
func (s *PrewarmService) Get(contentID, profileID string) *PrewarmResult {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.Get(contentID, profileID)
}

func (s *PrewarmService) runPrewarm(ctx context.Context, req PrewarmRequest) {
	t0 := time.Now()
	slog.Info("virtual prewarm: starting",
		"content_id", req.ContentID, "profile_id", req.ProfileID)

	// 1. List candidates from provider
	candidates, err := s.resolver.ListCandidates(ctx, req.FilePath, req.UserID, req.ProfileID, req.OwnerID)
	if err != nil {
		slog.Warn("virtual prewarm: list candidates failed",
			"content_id", req.ContentID, "error", err)
		return
	}
	if len(candidates) == 0 {
		slog.Info("virtual prewarm: no candidates from provider",
			"content_id", req.ContentID)
		return
	}

	// 2. Select the candidate most likely to direct-play on the device.
	bestIdx := selectBestCandidateForDevice(candidates, req.Device)
	best := candidates[bestIdx]

	slog.Info("virtual prewarm: selected candidate",
		"content_id", req.ContentID, "candidate_idx", bestIdx,
		"resolution", best.Resolution, "video", best.CodecVideo,
		"audio", best.CodecAudio, "uri", truncateStr(best.URI, 80))

	// 3. Resolve the provider URL, then probe it so the pre-warmed entry
	// carries real track metadata. Playback can reuse the resolved winner and
	// skip its own probe on the next view of the same content.
	resolvedURL, resolvedFile, err := s.resolver.ResolveForPrewarm(ctx, best.URI, req.UserID, req.ProfileID, req.OwnerID)
	if err != nil {
		slog.Warn("virtual prewarm: resolve failed",
			"content_id", req.ContentID, "candidate_uri", best.URI, "error", err)
		return
	}
	probedFile := resolvedFile
	probeSucceeded := false
	if s.probeFunc != nil && resolvedURL != "" {
		template := &models.MediaFile{
			FilePath:                   req.FilePath,
			ContentID:                  req.ContentID,
			VirtualOwnerInstallationID: req.OwnerID,
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, prewarmProbeBudget)
		probed, probeErr := s.probeFunc(probeCtx, resolvedURL, template)
		probeCancel()
		if probeErr != nil {
			slog.Warn("virtual prewarm: probe failed",
				"content_id", req.ContentID, "candidate_uri", best.URI, "error", probeErr)
		} else if probed != nil {
			probedFile = probed
			probeSucceeded = true
		}
	}

	elapsed := time.Since(t0)
	slog.Info("virtual prewarm: completed",
		"content_id", req.ContentID, "elapsed_ms", elapsed.Milliseconds(),
		"url_obtained", resolvedURL != "", "probed", probeSucceeded)

	// 4. Cache the result
	s.cache.Set(req.ContentID, req.ProfileID, &PrewarmResult{
		ResolvedURL: resolvedURL,
		ProbedFile:  probedFile,
		StreamURI:   best.URI,
		SelectedIdx: bestIdx,
		ResolvedAt:  time.Now(),
	})
}

// selectBestCandidateForDevice picks the candidate most likely to direct-play
// on the given device, avoiding transcoding. Falls back to first candidate
// if no device profile is provided.
func selectBestCandidateForDevice(candidates []VirtualPlaybackStream, device DeviceCapabilities) int {
	if len(candidates) == 0 {
		return 0
	}
	if len(device.CodecsVideo) == 0 && device.MaxResolution == "" {
		return 0 // no device info, pick first
	}

	bestScore := -1
	bestIdx := 0

	for i, c := range candidates {
		score := scoreCandidate(c, device)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	return bestIdx
}

// scoreCandidate returns a higher score for candidates that match the device
// better. Direct-play compatibility is the primary signal.
func scoreCandidate(c VirtualPlaybackStream, device DeviceCapabilities) int {
	score := 0

	// Video codec match (biggest factor)
	videoCodec := strings.ToLower(strings.TrimSpace(c.CodecVideo))
	if videoCodec != "" && deviceSupportsCodec(device.CodecsVideo, videoCodec) {
		score += 100
	}

	// Resolution: prefer matching or lower (avoid transcode from 4K to 1080p)
	resScore := resolutionScore(c.Resolution, device.MaxResolution)
	score += resScore

	// HDR: if device supports HDR, prefer HDR; if not, penalize HDR content
	if device.HDR {
		if strings.Contains(strings.ToLower(c.HDR), "dv") || strings.Contains(strings.ToLower(c.HDR), "hdr") {
			score += 30
		}
	} else if strings.Contains(strings.ToLower(c.HDR), "dv") || strings.Contains(strings.ToLower(c.HDR), "hdr") {
		score -= 20 // HDR content on non-HDR device = possible tone-map needed
	}

	// Audio codec match (less critical — audio transcode is cheaper)
	audioCodec := strings.ToLower(strings.TrimSpace(c.CodecAudio))
	if audioCodec != "" && deviceSupportsCodec(device.CodecsAudio, audioCodec) {
		score += 50
	}

	// Container match
	if c.Container != "" && deviceSupportsContainer(device.Containers, c.Container) {
		score += 10
	}

	return score
}

func deviceSupportsCodec(deviceCodecs []string, codec string) bool {
	if len(deviceCodecs) == 0 {
		return true // no profile = assume supports all
	}
	codec = strings.ToLower(codec)
	for _, dc := range deviceCodecs {
		if strings.EqualFold(strings.TrimSpace(dc), codec) {
			return true
		}
	}
	return false
}

func deviceSupportsContainer(deviceContainers []string, container string) bool {
	if len(deviceContainers) == 0 {
		return true
	}
	container = strings.ToLower(container)
	for _, dc := range deviceContainers {
		if strings.EqualFold(strings.TrimSpace(dc), container) {
			return true
		}
	}
	return false
}

// resolutionScore returns a score based on how well the candidate resolution
// matches the device max. Exact match is best, lower is OK (direct play),
// higher needs transcode (penalized).
func resolutionScore(candidateRes, deviceMax string) int {
	cRank := resolutionRank(candidateRes)
	dRank := resolutionRank(deviceMax)
	if cRank == 0 || dRank == 0 {
		return 25 // unknown resolution, neutral
	}
	if cRank == dRank {
		return 40 // exact match
	}
	if cRank < dRank {
		return 35 // lower than device max = fine, direct play
	}
	// Higher than device max = needs downscale/transcode
	return 10
}

func resolutionRank(res string) int {
	res = strings.ToLower(strings.TrimSpace(res))
	switch {
	case strings.Contains(res, "4320") || strings.Contains(res, "8k"):
		return 8
	case strings.Contains(res, "2160") || strings.Contains(res, "4k"):
		return 7
	case strings.Contains(res, "1440") || strings.Contains(res, "2k"):
		return 6
	case strings.Contains(res, "1080"):
		return 5
	case strings.Contains(res, "720"):
		return 4
	case strings.Contains(res, "480"):
		return 3
	case strings.Contains(res, "360"):
		return 2
	case strings.Contains(res, "240"):
		return 1
	default:
		return 0
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Adapter so plugins.Service implements PrewarmResolver
func (s *Service) ResolveForPrewarm(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) (string, *models.MediaFile, error) {
	url, err := s.ResolveVirtualPlaybackForInstallation(ctx, virtualPath, userID, profileID, ownerInstallationID, true)
	if err != nil {
		return "", nil, err
	}
	// For pre-warm, we return URL + nil file (probe happens in handler)
	return url, nil, nil
}

func (s *Service) ListCandidates(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error) {
	return s.ListVirtualPlaybackStreamsForInstallation(ctx, virtualPath, userID, profileID, ownerInstallationID, true)
}
