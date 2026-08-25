package transcodenode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const maxHWCapabilitiesResponseBytes = 1 << 20

// FetchHWCapabilities performs the authenticated node capability request with
// redirect refusal and a bounded response. The supplied client is shallow-
// cloned so its transport, timeout, and cookie jar remain available without
// mutating its redirect policy.
func FetchHWCapabilities(ctx context.Context, baseClient *http.Client, nodeURL, jwtSecret string) (playback.HWAccelInfo, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(nodeURL, "/")+"/hw-capabilities", nil)
	if err != nil {
		return playback.HWAccelInfo{}, 0, logredact.SanitizeURLError(err)
	}
	request.Header.Set("Authorization", "Bearer "+jwtSecret)

	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	client := *baseClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return playback.HWAccelInfo{}, 0, logredact.SanitizeURLError(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		// Drain the (small) error body so the transport can reuse the
		// connection instead of tearing it down on every failed probe.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return playback.HWAccelInfo{}, response.StatusCode, nil
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxHWCapabilitiesResponseBytes+1))
	if err != nil {
		return playback.HWAccelInfo{}, response.StatusCode, err
	}
	if len(data) > maxHWCapabilitiesResponseBytes {
		return playback.HWAccelInfo{}, response.StatusCode, fmt.Errorf("node capability response exceeds %d bytes", maxHWCapabilitiesResponseBytes)
	}
	var info playback.HWAccelInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return playback.HWAccelInfo{}, response.StatusCode, err
	}
	return info, response.StatusCode, nil
}
