package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/telemetry"
)

// nodeNetworkAccessTimeout bounds one proxy's answer to a network access
// status read or command, like the node force-reload.
const nodeNetworkAccessTimeout = 10 * time.Second

// maxNodeNetworkAccessBodyBytes bounds a proxy's answer; an honest status is
// under a kilobyte.
const maxNodeNetworkAccessBodyBytes = 64 << 10

// ListNetworkAccessNodes returns every enabled proxy node. Transcode nodes
// never run network access providers: clients never talk to them.
func (h *NodeHandler) ListNetworkAccessNodes(ctx context.Context) ([]plugins.NetworkAccessNode, error) {
	if h == nil || h.repo == nil {
		return nil, ErrAdminNodesUnavailable
	}
	all, err := h.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	nodes := make([]plugins.NetworkAccessNode, 0, len(all))
	for _, node := range all {
		if node == nil || !node.Enabled || node.Type != nodepool.NodeTypeProxy {
			continue
		}
		nodes = append(nodes, plugins.NetworkAccessNode{ID: node.ID, Name: node.Name, URL: node.URL})
	}
	return nodes, nil
}

// NodeNetworkAccessStatus reads the provider's status from one proxy.
func (h *NodeHandler) NodeNetworkAccessStatus(ctx context.Context, node plugins.NetworkAccessNode, provider string) (netaccess.Status, error) {
	var report netaccess.HostStatusReport
	if err := h.nodeNetworkAccessCall(ctx, node, http.MethodGet, "/network-access/status", "status", &report); err != nil {
		return netaccess.Status{}, err
	}
	for _, status := range report.Providers {
		if status.Provider == provider {
			return status, nil
		}
	}
	return netaccess.Status{}, plugins.ErrNetworkAccessProviderNotFound
}

// NodeNetworkAccessConnect brings the provider up on one proxy.
func (h *NodeHandler) NodeNetworkAccessConnect(ctx context.Context, node plugins.NetworkAccessNode, provider string) (netaccess.Status, error) {
	var status netaccess.Status
	err := h.nodeNetworkAccessCall(ctx, node, http.MethodPost, "/network-access/"+url.PathEscape(provider)+"/connect", "status", &status)
	return status, err
}

// NodeNetworkAccessDisconnect tears the provider down on one proxy.
func (h *NodeHandler) NodeNetworkAccessDisconnect(ctx context.Context, node plugins.NetworkAccessNode, provider string) (netaccess.Status, error) {
	var status netaccess.Status
	err := h.nodeNetworkAccessCall(ctx, node, http.MethodPost, "/network-access/"+url.PathEscape(provider)+"/disconnect", "status", &status)
	return status, err
}

// nodeNetworkAccessCall performs one bearer-authenticated request against a
// proxy's network-access routes and decodes the JSON answer into out. A 404
// is the proxy saying the provider is not installed there and maps to
// plugins.ErrNetworkAccessProviderNotFound; every other failure carries the
// proxy's plain-text reason.
func (h *NodeHandler) nodeNetworkAccessCall(ctx context.Context, node plugins.NetworkAccessNode, method, path, operation string, out any) error {
	if h == nil {
		return ErrAdminNodesUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, nodeNetworkAccessTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, nodepool.NodeEndpoint(node.URL, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.jwtSecret)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: nodeNetworkAccessTimeout}
	resp, err := telemetry.DoTrustedNode(client, req, operation)
	if err != nil {
		return fmt.Errorf("proxy node unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxNodeNetworkAccessBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read proxy node response: %w", err)
	}
	if len(body) > maxNodeNetworkAccessBodyBytes {
		return fmt.Errorf("proxy node response exceeds %d bytes", maxNodeNetworkAccessBodyBytes)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return plugins.ErrNetworkAccessProviderNotFound
	case http.StatusUnauthorized:
		return fmt.Errorf("proxy node refused the node bearer")
	case http.StatusServiceUnavailable:
		return fmt.Errorf("proxy node does not host network access providers: %s", trimNodeReason(body))
	default:
		return fmt.Errorf("proxy node answered %d: %s", resp.StatusCode, trimNodeReason(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode proxy node response: %w", err)
	}
	return nil
}

// trimNodeReason keeps a proxy's plain-text failure short enough for a status
// row.
func trimNodeReason(body []byte) string {
	const limit = 200
	reason := string(body)
	for len(reason) > 0 && (reason[len(reason)-1] == '\n' || reason[len(reason)-1] == '\r') {
		reason = reason[:len(reason)-1]
	}
	if len(reason) > limit {
		reason = reason[:limit] + "…"
	}
	return reason
}

var _ plugins.NetworkAccessNodes = (*NodeHandler)(nil)
