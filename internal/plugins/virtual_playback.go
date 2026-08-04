package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/remotestream"
)

const (
	virtualStreamProviderCapabilityType = "virtual_stream_provider.v1"
	maxVirtualPlaybackStreams           = 50
	maxVirtualPlaybackResponseLen       = 1 << 20
	maxVirtualPlaybackCacheEntries      = 256
	maxVirtualPlaybackCacheBytes        = 16 << 20
	maxVirtualProfileCacheEntries       = 128
	virtualStreamsCacheTTL              = 1 * time.Minute
	virtualProfilesCacheTTL             = time.Minute
	minVirtualStreamsCacheTTL           = 10 * time.Second
	maxVirtualStreamsCacheTTL           = 7 * 24 * time.Hour
	maxVirtualIDLen                     = 256
	maxVirtualLabelLen                  = 256
	maxVirtualMetadataStringLen         = 1024
	maxVirtualMetadataLen               = 64 << 10
	maxVirtualLanguages                 = 32
	maxVirtualLanguageLen               = 35
)

type virtualStreamsCacheEntry struct {
	result    *pluginv1.VirtualStreamResult
	expiresAt time.Time
	createdAt time.Time
	size      int
}

type virtualProfilesCacheEntry struct {
	response  *pluginv1.ListVirtualStreamProfilesResponse
	expiresAt time.Time
	createdAt time.Time
}

type virtualVariantsCacheEntry struct {
	variants  []VirtualPlaybackVariant
	err       error
	expiresAt time.Time
}

// VirtualPlaybackRouting makes plugin ownership and fallback an explicit
// decision. OwnerInstallationID should come from the virtual MediaFile.
// Fallback is disabled unless the caller opts in.
type VirtualPlaybackRouting struct {
	OwnerInstallationID int
	AllowFallback       bool
	AllowInsecure       bool // skip SSRF validation when plugin allows local URLs
}

// VirtualPlaybackVariant is a provider-neutral profile placeholder returned
// by a virtual playback plugin. It is safe to request during collection sync:
// no upstream streaming provider is contacted until playback.
type VirtualPlaybackVariant struct {
	VirtualURI          string `json:"virtual_uri"`
	Label               string `json:"label"`
	Resolution          string `json:"resolution,omitempty"`
	CodecVideo          string `json:"codec_video,omitempty"`
	CodecAudio          string `json:"codec_audio,omitempty"`
	HDR                 string `json:"hdr,omitempty"`
	OwnerInstallationID int    `json:"-"`
}

// VirtualPlaybackStream is a provider result exposed as a temporary, stable
// virtual file. The provider URL is deliberately not returned or persisted;
// it is resolved again when the selected file is played.
type VirtualPlaybackStream struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	URI                 string   `json:"uri"`
	Resolution          string   `json:"resolution,omitempty"`
	CodecVideo          string   `json:"codec_video,omitempty"`
	CodecAudio          string   `json:"codec_audio,omitempty"`
	HDR                 string   `json:"hdr,omitempty"`
	SourceType          string   `json:"source_type,omitempty"`
	FileSize            int64    `json:"file_size,omitempty"`
	Container           string   `json:"container,omitempty"`
	Bitrate             int      `json:"bitrate,omitempty"`
	AudioLanguages      []string `json:"audio_languages,omitempty"`
	SubtitleLanguages   []string `json:"subtitle_languages,omitempty"`
	OwnerInstallationID int      `json:"-"`
}

var ErrVirtualPlaybackResolverNotInstalled = errors.New("virtual playback resolver is not installed")

// ListVirtualPlaybackStreams is the compatibility entry point for virtual
// files that predate persisted owner IDs. New callers should use
// ListVirtualPlaybackStreamsWithRouting and pass the file owner.
func (s *Service) ListVirtualPlaybackStreams(ctx context.Context, virtualPath string) ([]VirtualPlaybackStream, error) {
	return s.ListVirtualPlaybackStreamsWithRouting(ctx, virtualPath, 0, "", VirtualPlaybackRouting{AllowFallback: true})
}

// ListVirtualPlaybackStreamsWithRouting asks the owning typed virtual stream
// provider for current candidates. Collection sync must not call this method.
func (s *Service) ListVirtualPlaybackStreamsWithRouting(
	ctx context.Context,
	virtualPath string,
	userID int,
	profileID string,
	routing VirtualPlaybackRouting,
) ([]VirtualPlaybackStream, error) {
	request, selection, err := virtualStreamRequest(virtualPath, userID, profileID)
	if err != nil {
		return nil, err
	}
	result, installationID, err := s.resolveVirtualStreamResult(ctx, request, selection, routing)
	if err != nil {
		return nil, err
	}
	return streamsFromVirtualResult(virtualPath, result, installationID), nil
}

func (s *Service) ListVirtualPlaybackStreamsForInstallation(
	ctx context.Context,
	virtualPath string,
	userID int,
	profileID string,
	ownerInstallationID int,
	allowFallback bool,
) ([]VirtualPlaybackStream, error) {
	return s.ListVirtualPlaybackStreamsWithRouting(ctx, virtualPath, userID, profileID, VirtualPlaybackRouting{
		OwnerInstallationID: ownerInstallationID,
		AllowFallback:       allowFallback,
	})
}

// ResolveVirtualPlayback is the compatibility entry point for callers that do
// not yet pass the MediaFile owner. It explicitly opts into provider fallback.
func (s *Service) ResolveVirtualPlayback(ctx context.Context, virtualPath string, userID int, profileID string) (string, error) {
	return s.ResolveVirtualPlaybackWithRouting(ctx, virtualPath, userID, profileID, VirtualPlaybackRouting{AllowFallback: true})
}

// ResolveVirtualPlaybackWithRouting resolves the selected candidate through
// the typed SDK capability. It routes to the file owner first and only tries
// another provider when AllowFallback is true.
func (s *Service) ResolveVirtualPlaybackWithRouting(
	ctx context.Context,
	virtualPath string,
	userID int,
	profileID string,
	routing VirtualPlaybackRouting,
) (string, error) {
	return s.resolveVirtualPlaybackWithRouting(ctx, virtualPath, userID, profileID, routing, false)
}

func (s *Service) resolveVirtualPlaybackWithRouting(
	ctx context.Context,
	virtualPath string,
	userID int,
	profileID string,
	routing VirtualPlaybackRouting,
	forceRefresh bool,
) (string, error) {
	request, selection, err := virtualStreamRequestWithRefresh(virtualPath, userID, profileID, forceRefresh)
	if err != nil {
		return "", err
	}
	result, _, err := s.resolveVirtualStreamResult(ctx, request, selection, routing)
	if err != nil {
		return "", err
	}
	candidate := selectVirtualCandidate(result.GetCandidates(), selection)
	if candidate == nil {
		return "", errors.New("virtual playback provider returned no matching stream")
	}
	raw := candidate.GetTemporaryUri()
	var validated string
	var validateErr error
	if routing.AllowInsecure {
		validated, validateErr = validateProviderStreamURLSyntax(raw)
	} else {
		validated, validateErr = validateProviderStreamURL(ctx, raw)
	}
	if validateErr != nil {
		return "", fmt.Errorf("virtual playback provider returned an unsafe stream URL: %w", validateErr)
	}
	return validated, nil
}

func (s *Service) ResolveVirtualPlaybackForInstallation(
	ctx context.Context,
	virtualPath string,
	userID int,
	profileID string,
	ownerInstallationID int,
	allowFallback bool,
) (string, error) {
	return s.ResolveVirtualPlaybackWithRouting(ctx, virtualPath, userID, profileID, VirtualPlaybackRouting{
		OwnerInstallationID: ownerInstallationID,
		AllowFallback:       allowFallback,
		AllowInsecure:       s.installationAllowsInsecure(ctx, ownerInstallationID),
	})
}

func (s *Service) RefreshVirtualPlaybackForInstallation(
	ctx context.Context,
	virtualPath string,
	userID int,
	profileID string,
	ownerInstallationID int,
	allowFallback bool,
) (string, error) {
	return s.resolveVirtualPlaybackWithRouting(ctx, virtualPath, userID, profileID, VirtualPlaybackRouting{
		OwnerInstallationID: ownerInstallationID,
		AllowFallback:       allowFallback,
		AllowInsecure:       s.installationAllowsInsecure(ctx, ownerInstallationID),
	}, true)
}

type virtualStreamSelection struct {
	resultID string
	profile  string
}

func virtualStreamRequest(virtualPath string, userID int, profileID string) (*pluginv1.ResolveVirtualStreamRequest, virtualStreamSelection, error) {
	return virtualStreamRequestWithRefresh(virtualPath, userID, profileID, false)
}

func virtualStreamRequestWithRefresh(virtualPath string, userID int, profileID string, forceRefresh bool) (*pluginv1.ResolveVirtualStreamRequest, virtualStreamSelection, error) {
	raw := strings.TrimSpace(virtualPath)
	if raw == "" || len(raw) > maxVirtualMetadataStringLen {
		return nil, virtualStreamSelection{}, errors.New("virtual media URI is empty or too long")
	}
	if !strings.Contains(raw, "://") {
		raw = "virtual://" + strings.TrimPrefix(raw, "/")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "virtual" || parsed.Host == "" {
		return nil, virtualStreamSelection{}, errors.New("invalid virtual media URI")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, virtualStreamSelection{}, errors.New("invalid virtual media URI")
	}
	mediaType := strings.ToLower(parsed.Host)
	segments := splitVirtualPath(parsed.EscapedPath())
	if len(segments) == 0 {
		return nil, virtualStreamSelection{}, errors.New("virtual media URI has no external ID")
	}

	externalIDs := make(map[string]string, 1)
	season, episode := int32(0), int32(0)
	switch mediaType {
	case "movie":
		idIndex := 0
		if len(segments) == 2 && (segments[0] == "tmdb" || segments[0] == "imdb") {
			putVirtualExternalID(externalIDs, segments[0]+":"+segments[1], "tmdb")
			idIndex = 1
		}
		if len(segments)-idIndex != 1 {
			return nil, virtualStreamSelection{}, errors.New("movie virtual URI has an invalid path")
		}
		if idIndex == 0 {
			putVirtualExternalID(externalIDs, segments[0], "tmdb")
		}
	case "series", "episode":
		mediaType = "episode"
		idIndex := 0
		if len(segments) == 4 && (segments[0] == "tvdb" || segments[0] == "tmdb" || segments[0] == "imdb") {
			putVirtualExternalID(externalIDs, segments[0]+":"+segments[1], "tvdb")
			idIndex = 1
		}
		if len(segments)-idIndex != 3 {
			return nil, virtualStreamSelection{}, errors.New("episode virtual URI must include an external ID, season, and episode")
		}
		if idIndex == 0 {
			putVirtualExternalID(externalIDs, segments[0], "tvdb")
		}
		season64, seasonErr := strconv.ParseInt(segments[idIndex+1], 10, 32)
		episode64, episodeErr := strconv.ParseInt(segments[idIndex+2], 10, 32)
		if seasonErr != nil || episodeErr != nil || season64 <= 0 || episode64 <= 0 {
			return nil, virtualStreamSelection{}, errors.New("episode virtual URI has invalid season or episode")
		}
		season, episode = int32(season64), int32(episode64)
	default:
		return nil, virtualStreamSelection{}, fmt.Errorf("unsupported virtual media type %q", mediaType)
	}
	if len(externalIDs) == 0 {
		return nil, virtualStreamSelection{}, errors.New("virtual media URI has an invalid external ID")
	}

	query := parsed.Query()
	selection := virtualStreamSelection{
		resultID: boundedQueryValue(query.Get("result")),
		profile:  boundedQueryValue(query.Get("profile")),
	}
	query.Del("result")
	parsed.RawQuery = query.Encode()
	requestMetadata := map[string]any{
		"virtual_uri": parsed.String(),
		"user_id":     userID,
		"profile_id":  boundedQueryValue(profileID),
	}
	if forceRefresh {
		requestMetadata["force_refresh"] = true
	}
	metadata, err := structpb.NewStruct(requestMetadata)
	if err != nil {
		return nil, virtualStreamSelection{}, fmt.Errorf("build virtual stream request metadata: %w", err)
	}
	return &pluginv1.ResolveVirtualStreamRequest{
		MediaType:     mediaType,
		ExternalIds:   externalIDs,
		SeasonNumber:  season,
		EpisodeNumber: episode,
		Metadata:      metadata,
	}, selection, nil
}

func splitVirtualPath(escapedPath string) []string {
	raw := strings.Trim(escapedPath, "/")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." ||
			strings.ContainsAny(decoded, "/\\\x00\r\n\t") {
			return nil
		}
		result = append(result, strings.ToLower(decoded))
	}
	return result
}

func putVirtualExternalID(ids map[string]string, raw, defaultProvider string) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	provider, value, found := strings.Cut(raw, ":")
	if !found {
		provider, value = defaultProvider, raw
		if strings.HasPrefix(raw, "tt") {
			provider = "imdb"
		}
	}
	if (provider != "imdb" && provider != "tvdb" && provider != "tmdb") ||
		value == "" || len(value) > maxVirtualIDLen {
		return
	}
	if provider == "imdb" && !strings.HasPrefix(value, "tt") {
		return
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (provider != "imdb" || r != 't') {
			return
		}
	}
	ids[provider] = value
}

func boundedQueryValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxVirtualLabelLen {
		return ""
	}
	return value
}

func (s *Service) resolveVirtualStreamResult(
	ctx context.Context,
	request *pluginv1.ResolveVirtualStreamRequest,
	selection virtualStreamSelection,
	routing VirtualPlaybackRouting,
) (*pluginv1.VirtualStreamResult, int, error) {
	if s == nil || s.installations == nil {
		return nil, 0, ErrVirtualPlaybackResolverNotInstalled
	}
	providers, err := s.virtualStreamProviders(ctx, routing)
	if err != nil {
		return nil, 0, err
	}
	var providerErr error
	var firstCandidateResult *pluginv1.VirtualStreamResult
	var firstCandidateInstallationID int

	for _, provider := range providers {
		result, err := s.resolveVirtualStreamProvider(ctx, provider.installationID, provider.capabilityID, request)
		if err != nil {
			providerErr = errors.Join(providerErr, err)
			continue
		}
		if len(result.GetCandidates()) == 0 {
			providerErr = errors.Join(providerErr, errors.New("virtual stream provider returned no candidates"))
			continue
		}
		if firstCandidateResult == nil {
			firstCandidateResult = result
			firstCandidateInstallationID = provider.installationID
		}
		if selectVirtualCandidate(result.GetCandidates(), selection) != nil {
			return result, provider.installationID, nil
		}
		providerErr = errors.Join(providerErr, errors.New("virtual stream provider returned no matching candidate"))
		if selection.resultID != "" {
			break
		}
	}

	if firstCandidateResult != nil && selection.resultID == "" {
		return firstCandidateResult, firstCandidateInstallationID, nil
	}

	if providerErr != nil {
		return nil, 0, fmt.Errorf("resolve virtual playback: %w", providerErr)
	}
	return nil, 0, ErrVirtualPlaybackResolverNotInstalled
}

type virtualStreamProviderRoute struct {
	installationID int
	capabilityID   string
}

func (s *Service) virtualStreamProviders(ctx context.Context, routing VirtualPlaybackRouting) ([]virtualStreamProviderRoute, error) {
	var routes []virtualStreamProviderRoute
	seen := make(map[int]struct{})
	if routing.OwnerInstallationID > 0 {
		capabilityID, err := s.virtualStreamCapability(ctx, routing.OwnerInstallationID)
		if err == nil {
			routes = append(routes, virtualStreamProviderRoute{installationID: routing.OwnerInstallationID, capabilityID: capabilityID})
			seen[routing.OwnerInstallationID] = struct{}{}
		} else if !routing.AllowFallback {
			return nil, fmt.Errorf("load owning virtual stream provider: %w", err)
		}
	} else if !routing.AllowFallback {
		return nil, errors.New("virtual playback owner is required when provider fallback is disabled")
	}
	if !routing.AllowFallback {
		return routes, nil
	}
	installations, err := s.installations.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled virtual stream providers: %w", err)
	}
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		if _, ok := seen[installation.ID]; ok {
			continue
		}
		capabilityID, err := s.virtualStreamCapability(ctx, installation.ID)
		if err != nil {
			continue
		}
		routes = append(routes, virtualStreamProviderRoute{installationID: installation.ID, capabilityID: capabilityID})
	}
	return routes, nil
}

func (s *Service) virtualStreamCapability(ctx context.Context, installationID int) (string, error) {
	if _, err := s.loadInstallation(ctx, installationID, true); err != nil {
		return "", err
	}
	capabilities, err := s.installations.ListCapabilities(ctx, installationID)
	if err != nil {
		return "", err
	}
	for _, capability := range capabilities {
		if capability != nil && capability.Type == virtualStreamProviderCapabilityType && strings.TrimSpace(capability.ID) != "" {
			return capability.ID, nil
		}
	}
	return "", ErrVirtualPlaybackResolverNotInstalled
}

func (s *Service) resolveVirtualStreamProvider(
	ctx context.Context,
	installationID int,
	capabilityID string,
	request *pluginv1.ResolveVirtualStreamRequest,
) (*pluginv1.VirtualStreamResult, error) {
	callRequest := proto.CloneOf(request)
	if callRequest == nil {
		return nil, errors.New("virtual stream request is empty")
	}
	callRequest.CapabilityId = capabilityID
	forceRefresh := virtualStreamForceRefresh(callRequest)
	cacheKey, err := virtualStreamCacheKey(installationID, callRequest)
	if err != nil {
		return nil, err
	}
	if !forceRefresh {
		if cached := s.cachedVirtualStreamResult(cacheKey, time.Now()); cached != nil {
			return cached, nil
		}
	}
	flightKey := "virtual-stream:" + cacheKey
	if forceRefresh {
		flightKey += ":refresh"
	}
	value, err, _ := s.launchGroup.Do(flightKey, func() (any, error) {
		if !forceRefresh {
			if cached := s.cachedVirtualStreamResult(cacheKey, time.Now()); cached != nil {
				return cached, nil
			}
		}
		client, err := s.VirtualStreamProviderClient(ctx, installationID, capabilityID)
		if err != nil {
			return nil, err
		}
		response, err := client.ResolveVirtualStream(ctx, callRequest)
		if err != nil {
			// Plugin errors are untrusted and may embed the provider request or
			// tokenized response URL. Keep the public error provider-neutral.
			return nil, fmt.Errorf("virtual stream provider %d request failed", installationID)
		}
		result, err := validateVirtualStreamResponse(response)
		if err != nil {
			return nil, err
		}
		s.storeVirtualStreamResult(cacheKey, result, time.Now())
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	result, ok := value.(*pluginv1.VirtualStreamResult)
	if !ok || result == nil {
		return nil, errors.New("virtual stream provider returned an invalid result")
	}
	return proto.CloneOf(result), nil
}

func virtualStreamForceRefresh(request *pluginv1.ResolveVirtualStreamRequest) bool {
	if request == nil || request.GetMetadata() == nil {
		return false
	}
	value, ok := request.GetMetadata().GetFields()["force_refresh"]
	return ok && value.GetBoolValue()
}

func requestForVirtualStreamCache(request *pluginv1.ResolveVirtualStreamRequest) *pluginv1.ResolveVirtualStreamRequest {
	cacheRequest := proto.CloneOf(request)
	if cacheRequest.Metadata == nil {
		return cacheRequest
	}
	delete(cacheRequest.Metadata.Fields, "force_refresh")
	return cacheRequest
}

func virtualStreamCacheKey(installationID int, request *pluginv1.ResolveVirtualStreamRequest) (string, error) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(requestForVirtualStreamCache(request))
	if err != nil {
		return "", fmt.Errorf("marshal virtual stream cache key: %w", err)
	}
	sum := sha256.Sum256(data)
	return strconv.Itoa(installationID) + ":" + hex.EncodeToString(sum[:]), nil
}

func (s *Service) cachedVirtualStreamResult(key string, now time.Time) *pluginv1.VirtualStreamResult {
	s.virtualStreamsMu.Lock()
	defer s.virtualStreamsMu.Unlock()
	s.evictExpiredVirtualStreamsLocked(now)
	entry, ok := s.virtualStreamsCache[key]
	if !ok || !now.Before(entry.expiresAt) {
		return nil
	}
	return proto.CloneOf(entry.result)
}

func virtualStreamCacheTTL(result *pluginv1.VirtualStreamResult) time.Duration {
	if result == nil || result.GetMetadata() == nil {
		return virtualStreamsCacheTTL
	}
	value, ok := result.GetMetadata().GetFields()["cache_ttl_seconds"]
	if !ok {
		return virtualStreamsCacheTTL
	}
	seconds := value.GetNumberValue()
	switch {
	case seconds < minVirtualStreamsCacheTTL.Seconds():
		return minVirtualStreamsCacheTTL
	case seconds > maxVirtualStreamsCacheTTL.Seconds():
		return maxVirtualStreamsCacheTTL
	default:
		return time.Duration(seconds) * time.Second
	}
}

func (s *Service) storeVirtualStreamResult(key string, result *pluginv1.VirtualStreamResult, now time.Time) {
	resultSize := proto.Size(result)
	if resultSize <= 0 || resultSize > maxVirtualPlaybackCacheBytes {
		return
	}
	expiresAt := now.Add(virtualStreamCacheTTL(result))
	for _, candidate := range result.GetCandidates() {
		if candidate.GetExpiresAt() != nil && candidate.GetExpiresAt().IsValid() {
			candidateExpiry := candidate.GetExpiresAt().AsTime()
			if candidateExpiry.Before(expiresAt) {
				expiresAt = candidateExpiry
			}
		}
	}
	if !expiresAt.After(now) {
		return
	}
	s.virtualStreamsMu.Lock()
	defer s.virtualStreamsMu.Unlock()
	s.evictExpiredVirtualStreamsLocked(now)
	if s.virtualStreamsCache == nil {
		s.virtualStreamsCache = make(map[string]virtualStreamsCacheEntry)
	}
	for len(s.virtualStreamsCache) >= maxVirtualPlaybackCacheEntries ||
		virtualStreamsCacheSize(s.virtualStreamsCache)+resultSize > maxVirtualPlaybackCacheBytes {
		var oldestKey string
		var oldestAt time.Time
		for candidateKey, entry := range s.virtualStreamsCache {
			if oldestKey == "" || entry.createdAt.Before(oldestAt) {
				oldestKey, oldestAt = candidateKey, entry.createdAt
			}
		}
		delete(s.virtualStreamsCache, oldestKey)
	}
	s.virtualStreamsCache[key] = virtualStreamsCacheEntry{
		result:    proto.CloneOf(result),
		expiresAt: expiresAt,
		createdAt: now,
		size:      resultSize,
	}
}

func virtualStreamsCacheSize(entries map[string]virtualStreamsCacheEntry) int {
	total := 0
	for _, entry := range entries {
		total += entry.size
	}
	return total
}

func (s *Service) evictExpiredVirtualStreamsLocked(now time.Time) {
	for key, entry := range s.virtualStreamsCache {
		if !now.Before(entry.expiresAt) {
			delete(s.virtualStreamsCache, key)
		}
	}
}

func validateVirtualStreamResponse(response *pluginv1.ResolveVirtualStreamResponse) (*pluginv1.VirtualStreamResult, error) {
	if response == nil || response.GetResult() == nil {
		return nil, errors.New("virtual stream provider returned an empty response")
	}
	if proto.Size(response) > maxVirtualPlaybackResponseLen {
		return nil, errors.New("virtual stream provider response exceeded size limit")
	}
	result := proto.CloneOf(response.GetResult())
	if len(result.GetProviderId()) > maxVirtualIDLen || len(result.GetResultId()) > maxVirtualIDLen {
		return nil, errors.New("virtual stream provider returned an oversized identifier")
	}
	if err := validateVirtualStatus(result.GetAvailability(), result.GetError(), result.GetMetadata()); err != nil {
		return nil, err
	}
	if err := validateVirtualResultCacheMetadata(result.GetMetadata()); err != nil {
		return nil, err
	}
	if len(result.GetCandidates()) > maxVirtualPlaybackStreams {
		return nil, fmt.Errorf("virtual stream provider returned %d candidates; maximum is %d", len(result.GetCandidates()), maxVirtualPlaybackStreams)
	}
	now := time.Now()
	seen := make(map[string]struct{}, len(result.GetCandidates()))
	active := result.Candidates[:0]
	for _, candidate := range result.GetCandidates() {
		if candidate != nil && candidate.GetExpiresAt() != nil {
			if !candidate.GetExpiresAt().IsValid() {
				return nil, errors.New("virtual stream provider returned an invalid candidate expiry")
			}
			if !candidate.GetExpiresAt().AsTime().After(now) {
				continue
			}
		}
		if err := validateVirtualStreamCandidate(candidate, seen); err != nil {
			return nil, err
		}
		active = append(active, candidate)
	}
	result.Candidates = active
	sort.SliceStable(result.Candidates, func(i, j int) bool {
		left, right := result.Candidates[i].GetRank(), result.Candidates[j].GetRank()
		if left <= 0 {
			left = math.MaxInt32
		}
		if right <= 0 {
			right = math.MaxInt32
		}
		return left < right
	})
	return result, nil
}

func validateVirtualStreamCandidate(candidate *pluginv1.VirtualStreamCandidate, seen map[string]struct{}) error {
	if candidate == nil {
		return errors.New("virtual stream provider returned a nil candidate")
	}
	id := strings.TrimSpace(candidate.GetCandidateId())
	if id == "" || len(id) > maxVirtualIDLen || strings.ContainsAny(id, "\x00\r\n\t") {
		return errors.New("virtual stream provider returned an invalid candidate ID")
	}
	if _, duplicate := seen[id]; duplicate {
		return errors.New("virtual stream provider returned duplicate candidate IDs")
	}
	seen[id] = struct{}{}
	for _, value := range []string{
		candidate.GetProviderId(), candidate.GetVideoCodec(), candidate.GetAudioCodec(),
		candidate.GetContainer(), candidate.GetResolution().GetLabel(),
		candidate.GetHdr().GetFormat(), candidate.GetHdr().GetDolbyVisionProfile(),
	} {
		if len(value) > maxVirtualLabelLen || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("virtual stream provider returned an oversized or invalid field")
		}
	}
	if candidate.GetBitrate() < 0 || candidate.GetFileSizeBytes() < 0 ||
		candidate.GetResolution().GetWidth() < 0 || candidate.GetResolution().GetHeight() < 0 ||
		candidate.GetResolution().GetWidth() > 32768 || candidate.GetResolution().GetHeight() > 32768 ||
		candidate.GetResolution().GetFrameRate() < 0 || candidate.GetResolution().GetFrameRate() > 1000 {
		return errors.New("virtual stream provider returned invalid media metadata")
	}
	if err := validateVirtualStatus(candidate.GetAvailability(), candidate.GetError(), candidate.GetMetadata()); err != nil {
		return err
	}
	if err := validateVirtualCandidateDisplayMetadata(candidate.GetMetadata()); err != nil {
		return err
	}
	if err := validateLanguages(candidate.GetAudioLanguages()); err != nil {
		return err
	}
	if err := validateLanguages(candidate.GetSubtitleLanguages()); err != nil {
		return err
	}
	if _, err := remotestream.ValidateURLSyntax(candidate.GetTemporaryUri()); err != nil {
		return fmt.Errorf("virtual stream provider returned an unsafe candidate URL: %w", err)
	}
	return nil
}

func validateVirtualCandidateDisplayMetadata(metadata *structpb.Struct) error {
	if metadata == nil {
		return nil
	}
	for _, key := range []string{"display_name", "source_type"} {
		value, ok := metadata.GetFields()[key]
		if !ok {
			continue
		}
		if _, ok := value.GetKind().(*structpb.Value_StringValue); !ok {
			return fmt.Errorf("virtual stream provider metadata %q must be a string", key)
		}
		text := strings.TrimSpace(value.GetStringValue())
		if len(text) > maxVirtualLabelLen || strings.IndexFunc(text, func(char rune) bool {
			return unicode.IsControl(char) || unicode.Is(unicode.Cf, char)
		}) >= 0 {
			return fmt.Errorf("virtual stream provider metadata %q is invalid", key)
		}
	}
	return nil
}

func validateVirtualResultCacheMetadata(metadata *structpb.Struct) error {
	if metadata == nil {
		return nil
	}
	value, ok := metadata.GetFields()["cache_ttl_seconds"]
	if !ok {
		return nil
	}
	if _, ok := value.GetKind().(*structpb.Value_NumberValue); !ok {
		return errors.New("virtual stream provider metadata \"cache_ttl_seconds\" must be a number")
	}
	seconds := value.GetNumberValue()
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Trunc(seconds) != seconds {
		return errors.New("virtual stream provider metadata \"cache_ttl_seconds\" must be a finite integer")
	}
	return nil
}

func validateLanguages(values []string) error {
	if len(values) > maxVirtualLanguages {
		return errors.New("virtual stream provider returned too many language tags")
	}
	for _, value := range values {
		if len(value) > maxVirtualLanguageLen || strings.ContainsAny(value, "\x00\r\n\t") {
			return errors.New("virtual stream provider returned an invalid language tag")
		}
	}
	return nil
}

func validateVirtualStatus(
	availability *pluginv1.VirtualStreamAvailability,
	streamError *pluginv1.VirtualStreamError,
	metadata *structpb.Struct,
) error {
	if availability != nil {
		if len(availability.GetMessage()) > maxVirtualMetadataStringLen ||
			availability.GetProgressPercent() < 0 || availability.GetProgressPercent() > 100 {
			return errors.New("virtual stream provider returned invalid availability metadata")
		}
		if availability.GetEstimatedReadyAt() != nil && !availability.GetEstimatedReadyAt().IsValid() {
			return errors.New("virtual stream provider returned an invalid availability timestamp")
		}
	}
	if streamError != nil {
		if len(streamError.GetMessage()) > maxVirtualMetadataStringLen {
			return errors.New("virtual stream provider returned an oversized error message")
		}
		if streamError.GetRetryAfter() != nil && streamError.GetRetryAfter().CheckValid() != nil {
			return errors.New("virtual stream provider returned an invalid retry delay")
		}
	}
	if metadata != nil && proto.Size(metadata) > maxVirtualMetadataLen {
		return errors.New("virtual stream provider returned oversized metadata")
	}
	return nil
}

func selectVirtualCandidate(candidates []*pluginv1.VirtualStreamCandidate, selection virtualStreamSelection) *pluginv1.VirtualStreamCandidate {
	if selection.resultID != "" {
		for _, candidate := range candidates {
			if candidate.GetCandidateId() == selection.resultID {
				return candidate
			}
		}
		// Candidate IDs are deliberately temporary. A provider may rotate or
		// remove one between picker display and playback, or the owning provider
		// may have been replaced. A provider-neutral URI without a profile can
		// safely choose the newly ranked first result because planning/probing
		// happens after this selection. Profile-bound selections stay strict so
		// the server never silently crosses a user-selected quality boundary.
		if selection.profile == "" && len(candidates) > 0 {
			return candidates[0]
		}
		return nil
	}
	if selection.profile != "" {
		for _, candidate := range candidates {
			if strings.EqualFold(candidate.GetResolution().GetLabel(), selection.profile) {
				return candidate
			}
		}
		for _, candidate := range candidates {
			lbl := strings.ToLower(candidate.GetResolution().GetLabel())
			prof := strings.ToLower(selection.profile)
			if lbl != "" && (strings.Contains(lbl, prof) || strings.Contains(prof, lbl)) {
				return candidate
			}
		}
		return nil
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[0]
}

func streamsFromVirtualResult(virtualPath string, result *pluginv1.VirtualStreamResult, installationID int) []VirtualPlaybackStream {
	streams := make([]VirtualPlaybackStream, 0, len(result.GetCandidates()))
	for _, candidate := range result.GetCandidates() {
		bitrate := int(candidate.GetBitrate())
		if candidate.GetBitrate() > int64(math.MaxInt) {
			bitrate = math.MaxInt
		}
		label := virtualCandidateMetadataString(candidate, "display_name")
		if label == "" {
			label = virtualCandidateLabel(candidate)
		}
		sourceType := virtualCandidateMetadataString(candidate, "source_type")
		if sourceType == "" {
			sourceType = candidate.GetProviderId()
		}
		streams = append(streams, VirtualPlaybackStream{
			ID:                  candidate.GetCandidateId(),
			Label:               label,
			URI:                 virtualCandidateURI(virtualPath, candidate.GetCandidateId()),
			Resolution:          candidate.GetResolution().GetLabel(),
			CodecVideo:          candidate.GetVideoCodec(),
			CodecAudio:          candidate.GetAudioCodec(),
			HDR:                 candidate.GetHdr().GetFormat(),
			SourceType:          sourceType,
			FileSize:            candidate.GetFileSizeBytes(),
			Container:           candidate.GetContainer(),
			Bitrate:             bitrate,
			AudioLanguages:      append([]string(nil), candidate.GetAudioLanguages()...),
			SubtitleLanguages:   append([]string(nil), candidate.GetSubtitleLanguages()...),
			OwnerInstallationID: installationID,
		})
	}
	return streams
}

func virtualCandidateMetadataString(candidate *pluginv1.VirtualStreamCandidate, key string) string {
	if candidate == nil || candidate.GetMetadata() == nil {
		return ""
	}
	value, ok := candidate.GetMetadata().GetFields()[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(value.GetStringValue())
}

func virtualCandidateLabel(candidate *pluginv1.VirtualStreamCandidate) string {
	parts := make([]string, 0, 4)
	for _, value := range []string{
		candidate.GetResolution().GetLabel(),
		candidate.GetVideoCodec(),
		candidate.GetHdr().GetFormat(),
		candidate.GetProviderId(),
	} {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return candidate.GetCandidateId()
	}
	return strings.Join(parts, " · ")
}

func virtualCandidateURI(virtualPath, candidateID string) string {
	raw := virtualPath
	if !strings.Contains(raw, "://") {
		raw = "virtual://" + strings.TrimPrefix(raw, "/")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	query.Set("result", candidateID)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// ConfiguredVirtualVariants uses the typed SDK configuration-only method. A
// compliant provider must not contact its upstream streaming source here.
func (s *Service) ConfiguredVirtualVariants(ctx context.Context, virtualPath, mediaType string) ([]VirtualPlaybackVariant, error) {
	if s == nil || s.installations == nil {
		return nil, nil
	}
	key := strings.ToLower(strings.TrimSpace(mediaType))
	if variants, err, ok := s.cachedConfiguredVirtualVariants(key, time.Now()); ok {
		return applyVirtualVariantPath(virtualPath, variants), err
	}
	value, err, _ := s.launchGroup.Do("configured-virtual-variants:"+key, func() (any, error) {
		if variants, cachedErr, ok := s.cachedConfiguredVirtualVariants(key, time.Now()); ok {
			return virtualVariantsCacheEntry{variants: variants, err: cachedErr}, nil
		}
		variants, loadErr := s.configuredVirtualVariantsUncached(ctx, "virtual://movie/placeholder", key)
		s.storeConfiguredVirtualVariants(key, variants, loadErr, time.Now())
		return virtualVariantsCacheEntry{variants: variants, err: loadErr}, nil
	})
	if err != nil {
		return nil, err
	}
	entry, ok := value.(virtualVariantsCacheEntry)
	if !ok {
		return nil, errors.New("virtual profile provider returned an invalid cache entry")
	}
	return applyVirtualVariantPath(virtualPath, entry.variants), entry.err
}

func (s *Service) configuredVirtualVariantsUncached(ctx context.Context, virtualPath, mediaType string) ([]VirtualPlaybackVariant, error) {
	installations, err := s.installations.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		capabilityID, err := s.virtualStreamCapability(ctx, installation.ID)
		if err != nil {
			continue
		}
		client, err := s.VirtualStreamProviderClient(ctx, installation.ID, capabilityID)
		if err != nil {
			continue
		}
		response, err := s.configuredVirtualProfiles(ctx, installation.ID, capabilityID, mediaType, client)
		if err != nil {
			continue
		}
		if proto.Size(response) > maxVirtualMetadataLen || len(response.GetProfiles()) > maxVirtualPlaybackStreams {
			return nil, errors.New("virtual profile provider response exceeded its limit")
		}
		if len(response.GetProfiles()) == 0 {
			// The base placeholder still needs an installation owner even when
			// the administrator has not configured quality profiles. Carry the
			// owner in a sentinel variant; catalog materialization skips the
			// duplicate URI while retaining its ownership.
			return []VirtualPlaybackVariant{{
				VirtualURI:          virtualPath,
				OwnerInstallationID: installation.ID,
			}}, nil
		}
		variants := make([]VirtualPlaybackVariant, 0, len(response.GetProfiles()))
		for _, profile := range response.GetProfiles() {
			if profile == nil {
				return nil, errors.New("virtual profile provider returned an empty profile")
			}
			for _, value := range []string{
				profile.GetLabel(), profile.GetResolution(), profile.GetVideoCodec(),
				profile.GetAudioCodec(), profile.GetHdrFormat(),
			} {
				if len(value) > maxVirtualLabelLen || strings.ContainsAny(value, "\x00\r\n") {
					return nil, errors.New("virtual profile provider returned an invalid field")
				}
			}
			label := strings.TrimSpace(profile.GetLabel())
			if label == "" {
				return nil, errors.New("virtual profile provider returned an unlabeled profile")
			}
			parsed, err := url.Parse(virtualPath)
			if err != nil || parsed.Scheme != "virtual" {
				return nil, errors.New("invalid virtual profile base URI")
			}
			query := parsed.Query()
			if profile.GetAllResults() {
				query.Set("results", "all")
				query.Del("profile")
			} else {
				query.Set("profile", label)
				query.Del("results")
			}
			query.Del("result")
			parsed.RawQuery = query.Encode()
			variants = append(variants, VirtualPlaybackVariant{
				VirtualURI: parsed.String(), Label: label,
				Resolution: profile.GetResolution(), CodecVideo: profile.GetVideoCodec(),
				CodecAudio: profile.GetAudioCodec(), HDR: profile.GetHdrFormat(),
				OwnerInstallationID: installation.ID,
			})
		}
		return variants, nil
	}
	return nil, nil
}

func (s *Service) cachedConfiguredVirtualVariants(key string, now time.Time) ([]VirtualPlaybackVariant, error, bool) {
	s.virtualVariantsMu.Lock()
	defer s.virtualVariantsMu.Unlock()
	entry, ok := s.virtualVariantsCache[key]
	if !ok || !now.Before(entry.expiresAt) {
		if ok {
			delete(s.virtualVariantsCache, key)
		}
		return nil, nil, false
	}
	return append([]VirtualPlaybackVariant(nil), entry.variants...), entry.err, true
}

func (s *Service) storeConfiguredVirtualVariants(key string, variants []VirtualPlaybackVariant, err error, now time.Time) {
	ttl := virtualProfilesCacheTTL
	if err != nil {
		ttl = 10 * time.Second
	}
	s.virtualVariantsMu.Lock()
	defer s.virtualVariantsMu.Unlock()
	if s.virtualVariantsCache == nil {
		s.virtualVariantsCache = make(map[string]virtualVariantsCacheEntry)
	}
	s.virtualVariantsCache[key] = virtualVariantsCacheEntry{
		variants: append([]VirtualPlaybackVariant(nil), variants...), err: err, expiresAt: now.Add(ttl),
	}
}

func applyVirtualVariantPath(virtualPath string, variants []VirtualPlaybackVariant) []VirtualPlaybackVariant {
	result := append([]VirtualPlaybackVariant(nil), variants...)
	for index := range result {
		parsed, err := url.Parse(virtualPath)
		if err != nil || parsed.Scheme != "virtual" {
			result[index].VirtualURI = virtualPath
			continue
		}
		query := parsed.Query()
		template, _ := url.Parse(result[index].VirtualURI)
		if template != nil {
			if template.Query().Get("results") == "all" {
				query.Set("results", "all")
				query.Del("profile")
			} else if profile := template.Query().Get("profile"); profile != "" {
				query.Set("profile", profile)
				query.Del("results")
			}
		}
		query.Del("result")
		parsed.RawQuery = query.Encode()
		result[index].VirtualURI = parsed.String()
	}
	return result
}

func (s *Service) configuredVirtualProfiles(
	ctx context.Context,
	installationID int,
	capabilityID string,
	mediaType string,
	client *pluginhost.VirtualStreamProviderClient,
) (*pluginv1.ListVirtualStreamProfilesResponse, error) {
	if client == nil {
		return nil, errors.New("virtual stream provider client is unavailable")
	}
	key := fmt.Sprintf("%d\x00%s\x00%s", installationID, capabilityID, strings.ToLower(strings.TrimSpace(mediaType)))
	if cached := s.cachedVirtualProfiles(key, time.Now()); cached != nil {
		return cached, nil
	}
	value, err, _ := s.launchGroup.Do("virtual-profiles:"+key, func() (any, error) {
		if cached := s.cachedVirtualProfiles(key, time.Now()); cached != nil {
			return cached, nil
		}
		response, err := client.ListVirtualStreamProfiles(ctx, &pluginv1.ListVirtualStreamProfilesRequest{
			CapabilityId: capabilityID,
			MediaType:    mediaType,
		})
		if err != nil {
			return nil, err
		}
		if response == nil {
			return nil, errors.New("virtual profile provider returned an empty response")
		}
		if proto.Size(response) > maxVirtualMetadataLen || len(response.GetProfiles()) > maxVirtualPlaybackStreams {
			return nil, errors.New("virtual profile provider response exceeded its limit")
		}
		s.storeVirtualProfiles(key, response, time.Now())
		return proto.CloneOf(response), nil
	})
	if err != nil {
		return nil, err
	}
	response, ok := value.(*pluginv1.ListVirtualStreamProfilesResponse)
	if !ok || response == nil {
		return nil, errors.New("virtual profile provider returned an invalid response")
	}
	return response, nil
}

func (s *Service) cachedVirtualProfiles(key string, now time.Time) *pluginv1.ListVirtualStreamProfilesResponse {
	s.virtualProfilesMu.Lock()
	defer s.virtualProfilesMu.Unlock()
	entry, ok := s.virtualProfilesCache[key]
	if !ok || !now.Before(entry.expiresAt) {
		if ok {
			delete(s.virtualProfilesCache, key)
		}
		return nil
	}
	return proto.CloneOf(entry.response)
}

func (s *Service) storeVirtualProfiles(key string, response *pluginv1.ListVirtualStreamProfilesResponse, now time.Time) {
	s.virtualProfilesMu.Lock()
	defer s.virtualProfilesMu.Unlock()
	if s.virtualProfilesCache == nil {
		s.virtualProfilesCache = make(map[string]virtualProfilesCacheEntry)
	}
	for candidateKey, entry := range s.virtualProfilesCache {
		if !now.Before(entry.expiresAt) {
			delete(s.virtualProfilesCache, candidateKey)
		}
	}
	for len(s.virtualProfilesCache) >= maxVirtualProfileCacheEntries {
		oldestKey := ""
		var oldest time.Time
		for candidateKey, entry := range s.virtualProfilesCache {
			if oldestKey == "" || entry.createdAt.Before(oldest) {
				oldestKey, oldest = candidateKey, entry.createdAt
			}
		}
		delete(s.virtualProfilesCache, oldestKey)
	}
	s.virtualProfilesCache[key] = virtualProfilesCacheEntry{
		response:  proto.CloneOf(response),
		expiresAt: now.Add(virtualProfilesCacheTTL),
		createdAt: now,
	}
}

// installationAllowsInsecure checks whether a plugin installation has the
// allow_insecure_http config key enabled, permitting private/local IP stream
// URLs that would otherwise be blocked by the SSRF guard.
func (s *Service) installationAllowsInsecure(ctx context.Context, installationID int) bool {
	if s == nil || s.configs == nil || installationID <= 0 {
		return false
	}
	configs, err := s.configs.ListGlobalConfigs(ctx, installationID)
	if err != nil {
		return false
	}
	for _, cfg := range configs {
		if cfg == nil || cfg.Key != "allow_insecure_http" {
			continue
		}
		// Value map may contain the boolean under the schema key itself,
		// or under common generic keys like "enabled" or "value".
		if v, ok := cfg.Value["allow_insecure_http"]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
		for _, k := range []string{"enabled", "value"} {
			if val, ok := cfg.Value[k]; ok {
				switch v := val.(type) {
				case bool:
					return v
				case string:
					return strings.EqualFold(strings.TrimSpace(v), "true")
				}
			}
		}
	}
	return false
}
