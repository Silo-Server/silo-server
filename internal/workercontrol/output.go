package workercontrol

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func outputPathWork(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 2 && (parts[0] == transcodeMode || parts[0] == "remux") {
		return parts[1]
	}
	if len(parts) >= 3 && parts[0] == "downloads" && parts[1] == "artifacts" {
		return parts[2]
	}
	return ""
}

func outputRouteAllowed(request *http.Request, work string) bool {
	if outputPathWork(request.URL.Path) != work || strings.ContainsAny(request.URL.Path, "\\\x00") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
	for _, p := range parts {
		if p == "." || p == ".." || p == "" {
			return false
		}
	}
	read := request.Method == http.MethodGet || request.Method == http.MethodHead
	switch parts[0] {
	case "remux":
		return read && len(parts) == 2
	case "downloads":
		return read && len(parts) == 3
	case transcodeMode:
		if len(parts) == 3 {
			return read && (parts[2] == "master.m3u8" || parts[2] == "stream.m3u8")
		}
		if len(parts) >= 4 && parts[2] == "segment" {
			return read && len(parts) == 4 || request.Method == http.MethodPost && len(parts) == 5 && parts[4] == "downloaded"
		}
	}
	return false
}

func (r *Runtime) decodeOutput(encoded string) (workeridentity.Grant, workeridentity.OutputAuthority, *streamtoken.Claims, error) {
	var body workeridentity.OutputAuthority
	grant, err := workeridentity.DecodeGrant(encoded, workeridentity.DomainProxyOutputGrant, r.options.Binding.NodeURL, r.options.Binding.CorePublicKey, time.Now())
	if err != nil || grant.NodeID != r.options.Binding.NodeID || grant.WorkID == "" || grant.Generation == 0 {
		return grant, body, nil, errors.New("invalid delegated output grant")
	}
	if err = json.Unmarshal(grant.Body, &body); err != nil || len(body.ProxyPublicKey) != ed25519.PublicKeySize {
		return grant, body, nil, errors.New("invalid proxy public authority")
	}
	if epoch := r.config.OutputAuthorizationEpoch(); epoch == "" || body.Epoch != epoch {
		return grant, body, nil, errors.New("output authorization epoch changed")
	}
	var claims streamtoken.Claims
	if err = json.Unmarshal(body.Claims, &claims); err != nil {
		return grant, body, nil, err
	}
	work := claims.TranscodeTransportID
	if claims.DownloadArtifactID != "" {
		work = claims.DownloadArtifactID
	} else if work == "" {
		work = claims.SessionID
	}
	if work != grant.WorkID || claims.TranscodeNode != r.options.Binding.NodeURL || claims.RoutingExecutionNodeID != 0 && claims.RoutingExecutionNodeID != grant.NodeID || claims.ExpiresAt == nil || !time.Now().Before(claims.ExpiresAt.Time) {
		return grant, body, nil, errors.New("output identity mismatch")
	}
	if body.AuthorityID == "" {
		return grant, body, nil, errors.New("output has no viewer authority")
	}
	return grant, body, &claims, nil
}
