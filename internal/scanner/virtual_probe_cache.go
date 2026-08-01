package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	defaultVirtualProbeCacheTTL     = 10 * time.Minute
	defaultVirtualProbeCacheEntries = 256
)

type virtualProbeCacheEntry struct {
	file      *models.MediaFile
	expiresAt time.Time
	usedAt    time.Time
}

// VirtualProbeCache bounds and coalesces playback-time ffprobe work. Its key
// includes the selected provider and resolved-source fingerprint. An exact
// result= URI is the provider contract's stable byte identity, so rotating
// query credentials on the same provider path can reuse its bounded probe.
// Unselected profile/base URIs retain the complete URL because their top
// result may change between calls.
type VirtualProbeCache struct {
	mu         sync.Mutex
	entries    map[string]virtualProbeCacheEntry
	ttl        time.Duration
	maxEntries int
	group      singleflight.Group
}

type VirtualProbeFunc func(context.Context, string, *models.MediaFile) (*models.MediaFile, error)

func NewVirtualProbeCache(ttl time.Duration, maxEntries int) *VirtualProbeCache {
	if ttl <= 0 {
		ttl = defaultVirtualProbeCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultVirtualProbeCacheEntries
	}
	return &VirtualProbeCache{entries: make(map[string]virtualProbeCacheEntry), ttl: ttl, maxEntries: maxEntries}
}

func (c *VirtualProbeCache) Probe(
	ctx context.Context,
	sourceURL string,
	file *models.MediaFile,
	probe VirtualProbeFunc,
) (*models.MediaFile, error) {
	if file == nil {
		return nil, nil
	}
	if probe == nil {
		return file, errors.New("virtual probe function is not configured")
	}
	if c == nil {
		return probe(ctx, sourceURL, file)
	}
	key := virtualProbeCacheKey(sourceURL, file)
	now := time.Now()
	if cached := c.load(key, now); cached != nil {
		return cached, nil
	}
	value, err, _ := c.group.Do(key, func() (any, error) {
		if cached := c.load(key, time.Now()); cached != nil {
			return cached, nil
		}
		probed, err := probe(ctx, sourceURL, file)
		if err != nil || probed == nil {
			return probed, err
		}
		c.store(key, probed, time.Now())
		return cloneVirtualProbeFile(probed), nil
	})
	if err != nil || value == nil {
		return file, err
	}
	probed, ok := value.(*models.MediaFile)
	if !ok {
		return file, errors.New("virtual probe returned an invalid media file")
	}
	return probed, nil
}

func (c *VirtualProbeCache) load(key string, now time.Time) *models.MediaFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	for candidateKey, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, candidateKey)
		}
	}
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	entry.usedAt = now
	c.entries[key] = entry
	return cloneVirtualProbeFile(entry.file)
}

func (c *VirtualProbeCache) store(key string, file *models.MediaFile, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for candidateKey, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, candidateKey)
		}
	}
	for len(c.entries) >= c.maxEntries {
		oldestKey := ""
		var oldest time.Time
		for candidateKey, entry := range c.entries {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey, oldest = candidateKey, entry.usedAt
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = virtualProbeCacheEntry{
		file: cloneVirtualProbeFile(file), expiresAt: now.Add(c.ttl), usedAt: now,
	}
}

func virtualProbeCacheKey(sourceURL string, file *models.MediaFile) string {
	streamIdentity := strings.TrimSpace(sourceURL)
	canonical := ""
	fileID := 0
	ownerID := 0
	if file != nil {
		canonical = file.FilePath
		fileID = file.ID
		ownerID = file.VirtualOwnerInstallationID
	}
	if selected, err := url.Parse(canonical); err == nil && strings.TrimSpace(selected.Query().Get("result")) != "" {
		if providerURL, parseErr := url.Parse(streamIdentity); parseErr == nil && providerURL.IsAbs() {
			providerURL.RawQuery = ""
			providerURL.Fragment = ""
			streamIdentity = providerURL.String()
		}
	}
	digest := sha256.Sum256([]byte(strconv.Itoa(fileID) + "\x00" + strconv.Itoa(ownerID) + "\x00" + canonical + "\x00" + streamIdentity))
	return hex.EncodeToString(digest[:])
}

func cloneVirtualProbeFile(file *models.MediaFile) *models.MediaFile {
	if file == nil {
		return nil
	}
	clone := *file
	clone.VideoTracks = append([]models.VideoTrack(nil), file.VideoTracks...)
	clone.AudioTracks = append([]models.AudioTrack(nil), file.AudioTracks...)
	clone.SubtitleTracks = append([]models.SubtitleTrack(nil), file.SubtitleTracks...)
	clone.ExternalSubtitles = append([]models.ExternalSubtitle(nil), file.ExternalSubtitles...)
	clone.Chapters = append([]models.MediaChapter(nil), file.Chapters...)
	return &clone
}
