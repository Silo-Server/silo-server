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
	info, _, status, err := FetchHWCapabilitiesPayload(ctx, baseClient, nodeURL, jwtSecret)
	return info, status, err
}

// FetchHWCapabilitiesPayload is FetchHWCapabilities with the node's own
// response bytes returned alongside the decoded report.
//
// A caller that *persists* the report must use this form. Re-marshaling the
// decoded struct instead drops every field this build does not know about, and
// during a rolling upgrade a node is routinely newer than the API server that
// reads it. The truncated payload would then be stored under the node's own
// hash, so after the API is upgraded the sweep still sees the hashes agree and
// never refetches: the durable inventory stays missing fields the new code
// reads, until some unrelated capability change or a manual re-probe moves the
// hash. The bytes are already bounded and parsed by the time they are returned,
// which is what makes storing them verbatim safe.
func FetchHWCapabilitiesPayload(ctx context.Context, baseClient *http.Client, nodeURL, jwtSecret string) (playback.HWAccelInfo, []byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(nodeURL, "/")+"/hw-capabilities", nil)
	if err != nil {
		return playback.HWAccelInfo{}, nil, 0, logredact.SanitizeURLError(err)
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
		return playback.HWAccelInfo{}, nil, 0, logredact.SanitizeURLError(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		// Drain the (small) error body so the transport can reuse the
		// connection instead of tearing it down on every failed probe.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return playback.HWAccelInfo{}, nil, response.StatusCode, nil
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxHWCapabilitiesResponseBytes+1))
	if err != nil {
		return playback.HWAccelInfo{}, nil, response.StatusCode, err
	}
	if len(data) > maxHWCapabilitiesResponseBytes {
		return playback.HWAccelInfo{}, nil, response.StatusCode, fmt.Errorf("node capability response exceeds %d bytes", maxHWCapabilitiesResponseBytes)
	}
	var info playback.HWAccelInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return playback.HWAccelInfo{}, nil, response.StatusCode, err
	}
	return info, data, response.StatusCode, nil
}
