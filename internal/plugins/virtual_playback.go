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

var ErrVirtualPlaybackResolverNotInstalled = errors.New("virtual playback resolver is not installed")

// ResolveVirtualPlayback delegates an aiostreams URI to the first enabled
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
