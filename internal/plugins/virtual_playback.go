package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

const virtualPlaybackCapabilityID = "virtual-playback"

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
			return "", fmt.Errorf("list plugin capabilities: %w", err)
		}
		for _, capability := range capabilities {
			if capability == nil || capability.Type != "http_routes.v1" || capability.ID != virtualPlaybackCapabilityID {
				continue
			}
			client, err := s.HTTPRoutesClient(ctx, installation.ID, capability.ID)
			if err != nil {
				return "", fmt.Errorf("connect to virtual playback plugin: %w", err)
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
				return "", fmt.Errorf("resolve virtual playback: %w", err)
			}
			if response.GetStatusCode() < 200 || response.GetStatusCode() >= 300 {
				return "", fmt.Errorf("virtual playback plugin returned status %d", response.GetStatusCode())
			}
			var payload struct {
				StreamURL string `json:"stream_url"`
			}
			if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
				return "", fmt.Errorf("decode virtual playback response: %w", err)
			}
			streamURL, err := url.Parse(payload.StreamURL)
			if err != nil || !streamURL.IsAbs() || (streamURL.Scheme != "https" && streamURL.Scheme != "http") {
				return "", errors.New("virtual playback plugin returned an invalid stream URL")
			}
			return streamURL.String(), nil
		}
	}
	return "", ErrVirtualPlaybackResolverNotInstalled
}
