package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

const virtualPlaybackCapabilityID = "virtual-playback"

const (
	maxVirtualPlaybackStreams     = 50
	maxVirtualPlaybackResponseLen = 4 << 20
	virtualStreamsCacheTTL        = 10 * time.Minute
)

type virtualStreamsCacheEntry struct {
	streams   []VirtualPlaybackStream
	expiresAt time.Time
}

// VirtualPlaybackVariant is a provider-neutral profile placeholder returned
// by a virtual playback plugin. It is safe to request during collection sync:
// no upstream streaming provider is contacted until playback.
type VirtualPlaybackVariant struct {
	VirtualURI string `json:"virtual_uri"`
	Label      string `json:"label"`
	Resolution string `json:"resolution,omitempty"`
	CodecVideo string `json:"codec_video,omitempty"`
	CodecAudio string `json:"codec_audio,omitempty"`
	HDR        string `json:"hdr,omitempty"`
}

// VirtualPlaybackStream is a provider result exposed as a temporary, stable
// virtual file. The provider URL is deliberately not returned or persisted;
// it is resolved again when the selected file is played.
type VirtualPlaybackStream struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	URI        string `json:"uri"`
	Resolution string `json:"resolution,omitempty"`
	CodecVideo string `json:"codec_video,omitempty"`
	CodecAudio string `json:"codec_audio,omitempty"`
	HDR        string `json:"hdr,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	FileSize   int64  `json:"file_size,omitempty"`
	Container  string `json:"container,omitempty"`
}

// ListVirtualPlaybackStreams asks enabled virtual playback plugins for
// current provider candidates. It is intended for just-in-time picker
// population at play time; collection sync must not call it.
func (s *Service) ListVirtualPlaybackStreams(ctx context.Context, virtualPath string) ([]VirtualPlaybackStream, error) {
	if s == nil {
		return nil, ErrVirtualPlaybackResolverNotInstalled
	}
	now := time.Now()
	s.virtualStreamsMu.Lock()
	if cached, ok := s.virtualStreamsCache[virtualPath]; ok && now.Before(cached.expiresAt) {
		streams := append([]VirtualPlaybackStream(nil), cached.streams...)
		s.virtualStreamsMu.Unlock()
		return streams, nil
	}
	s.virtualStreamsMu.Unlock()
	v, err, _ := s.launchGroup.Do("virtual-streams:"+virtualPath, func() (any, error) {
		return s.listVirtualPlaybackStreamsUncached(ctx, virtualPath)
	})
	if err != nil {
		return nil, err
	}
	streams := append([]VirtualPlaybackStream(nil), v.([]VirtualPlaybackStream)...)
	s.virtualStreamsMu.Lock()
	if s.virtualStreamsCache == nil {
		s.virtualStreamsCache = make(map[string]virtualStreamsCacheEntry)
	}
	s.virtualStreamsCache[virtualPath] = virtualStreamsCacheEntry{streams: append([]VirtualPlaybackStream(nil), streams...), expiresAt: time.Now().Add(virtualStreamsCacheTTL)}
	s.virtualStreamsMu.Unlock()
	return streams, nil
}

func (s *Service) listVirtualPlaybackStreamsUncached(ctx context.Context, virtualPath string) ([]VirtualPlaybackStream, error) {
	if s == nil || s.installations == nil {
		return nil, ErrVirtualPlaybackResolverNotInstalled
	}
	installations, err := s.installations.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled plugins: %w", err)
	}
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		caps, err := s.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			continue
		}
		for _, cap := range caps {
			if cap == nil || cap.Type != "http_routes.v1" || cap.ID != virtualPlaybackCapabilityID {
				continue
			}
			client, err := s.HTTPRoutesClient(ctx, installation.ID, cap.ID)
			if err != nil {
				continue
			}
			response, err := client.Handle(ctx, &pluginv1.HandleHTTPRequest{
				Method: http.MethodGet, Path: virtualPath,
				Headers: map[string]string{"X-Silo-List-Streams": "true"},
			})
			if err != nil {
				continue
			}
			if response.GetStatusCode() < 200 || response.GetStatusCode() >= 300 {
				continue
			}
			if len(response.GetBody()) > maxVirtualPlaybackResponseLen {
				continue
			}
			var payload struct {
				Streams []VirtualPlaybackStream `json:"streams"`
			}
			if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
				continue
			}
			if len(payload.Streams) == 0 {
				continue
			}
			if len(payload.Streams) > maxVirtualPlaybackStreams {
				payload.Streams = payload.Streams[:maxVirtualPlaybackStreams]
			}
			return payload.Streams, nil
		}
	}
	return nil, ErrVirtualPlaybackResolverNotInstalled
}

func (s *Service) ConfiguredVirtualVariants(ctx context.Context, virtualPath, mediaType string) ([]VirtualPlaybackVariant, error) {
	if s == nil || s.installations == nil {
		return nil, nil
	}
	installations, err := s.installations.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		caps, err := s.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			continue
		}
		for _, cap := range caps {
			if cap == nil || cap.Type != "http_routes.v1" || cap.ID != virtualPlaybackCapabilityID {
				continue
			}
			client, err := s.HTTPRoutesClient(ctx, installation.ID, cap.ID)
			if err != nil {
				continue
			}
			response, err := client.Handle(ctx, &pluginv1.HandleHTTPRequest{Method: http.MethodGet, Path: "/profiles/" + virtualPath})
			if err != nil || response.GetStatusCode() < 200 || response.GetStatusCode() >= 300 {
				continue
			}
			if len(response.GetBody()) > maxVirtualPlaybackResponseLen {
				continue
			}
			var payload struct {
				Variants []VirtualPlaybackVariant `json:"variants"`
			}
			if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
				continue
			}
			return payload.Variants, nil
		}
	}
	return nil, nil
}

var ErrVirtualPlaybackResolverNotInstalled = errors.New("virtual playback resolver is not installed")

// ResolveVirtualPlayback delegates a provider-neutral virtual URI to the first enabled
// plugin advertising the reserved virtual-playback HTTP routes capability.
func (s *Service) ResolveVirtualPlayback(ctx context.Context, virtualPath string, userID int, profileID string) (string, error) {
	if s == nil || s.installations == nil {
		return "", ErrVirtualPlaybackResolverNotInstalled
	}
	installations, err := s.installations.ListEnabled(ctx)
	if err != nil {
		return "", fmt.Errorf("list enabled plugins: %w", err)
	}
	for _, installation := range installations {
		if installation == nil {
			continue
		}
		capabilities, err := s.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			continue
		}
		for _, capability := range capabilities {
			if capability == nil || capability.Type != "http_routes.v1" || capability.ID != virtualPlaybackCapabilityID {
				continue
			}
			client, err := s.HTTPRoutesClient(ctx, installation.ID, capability.ID)
			if err != nil {
				continue
			}
			response, err := client.Handle(ctx, &pluginv1.HandleHTTPRequest{
				Method: http.MethodPost,
				Path:   virtualPath,
				Headers: map[string]string{
					"X-Silo-User-Id":    strconv.Itoa(userID),
					"X-Silo-Profile-Id": profileID,
				},
			})
			if err != nil {
				continue
			}
			if response.GetStatusCode() < 200 || response.GetStatusCode() >= 300 {
				continue
			}
			if len(response.GetBody()) > maxVirtualPlaybackResponseLen {
				continue
			}
			var payload struct {
				StreamURL string `json:"stream_url"`
			}
			if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
				continue
			}
			streamURL, err := validateProviderStreamURL(ctx, payload.StreamURL)
			if err != nil {
				continue
			}
			return streamURL, nil
		}
	}
	return "", ErrVirtualPlaybackResolverNotInstalled
}
