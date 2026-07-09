package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// NodeRepository defines the operations the NodeHandler needs on the node store.
type NodeRepository interface {
	List(ctx context.Context) ([]*nodepool.Node, error)
	GetByID(ctx context.Context, id int) (*nodepool.Node, error)
	Create(ctx context.Context, input nodepool.CreateNodeInput) (*nodepool.Node, error)
	Update(ctx context.Context, id int, input nodepool.UpdateNodeInput) (*nodepool.Node, error)
	Delete(ctx context.Context, id int) error
	UpdateHealth(ctx context.Context, id int, healthy bool, activeJobs, egressKbps int) error
}

// NodeListEnabled queries enabled nodes by type for pool reload.
type NodeListEnabled interface {
	ListEnabled(ctx context.Context, nodeType string) ([]*nodepool.Node, error)
}

// NodeHandler handles CRUD operations and health checks for stream nodes.
type NodeHandler struct {
	repo          NodeRepository
	proxyPool     *nodepool.ProxyPool
	transcodePool *nodepool.TranscodePool
	lister        NodeListEnabled
	eventBus      cache.EventBus
	redisClient   *redis.Client // for reading session keys
	jwtSecret     string        // for bearer auth when calling force-reload on nodes
}

// NewNodeHandler creates a new NodeHandler.
func NewNodeHandler(repo NodeRepository, proxyPool *nodepool.ProxyPool, transcodePool *nodepool.TranscodePool, lister NodeListEnabled, eventBus cache.EventBus, redisClient *redis.Client, jwtSecret string) *NodeHandler {
	return &NodeHandler{
		repo:          repo,
		proxyPool:     proxyPool,
		transcodePool: transcodePool,
		lister:        lister,
		eventBus:      eventBus,
		redisClient:   redisClient,
		jwtSecret:     jwtSecret,
	}
}

// ForceReloadResult represents the result of a force-reload on a single node.
type ForceReloadResult struct {
	NodeID   int    `json:"node_id"`
	NodeName string `json:"node_name"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// checkNodeResult is the JSON response for a node health check.
type checkNodeResult struct {
	Healthy    bool `json:"healthy"`
	ActiveJobs int  `json:"active_jobs"`
	EgressKbps int  `json:"egress_kbps"`
}

// HandleListNodes handles GET /admin/nodes.
func (h *NodeHandler) HandleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.repo.List(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "listing nodes", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list nodes")
		return
	}

	writeJSON(w, http.StatusOK, nodes)
}

// HandleCreateNode handles POST /admin/nodes.
func (h *NodeHandler) HandleCreateNode(w http.ResponseWriter, r *http.Request) {
	var input nodepool.CreateNodeInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	node, err := h.repo.Create(r.Context(), input)
	if err != nil {
		// Validation errors from CreateNodeInput.Validate() are treated as 400.
		if !errors.Is(err, nodepool.ErrNodeNotFound) {
			// Check if it's a validation error (non-sentinel, non-wrapped).
			// The repository calls input.Validate() which returns plain errors.
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "creating node", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create node")
		return
	}

	writeJSON(w, http.StatusCreated, node)
	h.reloadPools(r.Context())
}

// HandleUpdateNode handles PUT /admin/nodes/{id}.
func (h *NodeHandler) HandleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid node ID")
		return
	}

	var input nodepool.UpdateNodeInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	node, err := h.repo.Update(r.Context(), id, input)
	if err != nil {
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Node not found")
			return
		}
		slog.ErrorContext(r.Context(), "updating node", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update node")
		return
	}

	writeJSON(w, http.StatusOK, node)
	h.reloadPools(r.Context())
}

// HandleDeleteNode handles DELETE /admin/nodes/{id}.
func (h *NodeHandler) HandleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid node ID")
		return
	}

	err = h.repo.Delete(r.Context(), id)
	if err != nil {
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Node not found")
			return
		}
		slog.ErrorContext(r.Context(), "deleting node", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete node")
		return
	}

	w.WriteHeader(http.StatusNoContent)
	h.reloadPools(r.Context())
}

// HandleCheckNode handles POST /admin/nodes/{id}/check.
func (h *NodeHandler) HandleCheckNode(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid node ID")
		return
	}

	node, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Node not found")
			return
		}
		slog.ErrorContext(r.Context(), "fetching node for check", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch node")
		return
	}

	healthy, activeJobs, egressKbps := nodepool.CheckNode(r.Context(), node)

	if err := h.repo.UpdateHealth(r.Context(), id, healthy, activeJobs, egressKbps); err != nil {
		slog.ErrorContext(r.Context(), "persisting health check result", "component", "api", "node_id", id, "error", err)
	}

	writeJSON(w, http.StatusOK, checkNodeResult{
		Healthy:    healthy,
		ActiveJobs: activeJobs,
		EgressKbps: egressKbps,
	})
}

// HandleForceReloadNodes handles POST /admin/nodes/force-reload — sends a
// force-reload signal to every enabled node in parallel.
func (h *NodeHandler) HandleForceReloadNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	allNodes, err := h.repo.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var nodes []*nodepool.Node
	for _, n := range allNodes {
		if n.Enabled {
			nodes = append(nodes, n)
		}
	}

	results := make([]ForceReloadResult, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(idx int, node *nodepool.Node) {
			defer wg.Done()
			result := ForceReloadResult{NodeID: node.ID, NodeName: node.Name}
			client := &http.Client{Timeout: 10 * time.Second}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, node.URL+"/admin/force-reload", nil)
			if err != nil {
				result.Status = "error"
				result.Error = err.Error()
				results[idx] = result
				return
			}
			req.Header.Set("Authorization", "Bearer "+h.jwtSecret)
			resp, err := client.Do(req)
			if err != nil {
				result.Status = "error"
				result.Error = err.Error()
				results[idx] = result
				return
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
				result.Status = "ok"
			} else {
				result.Status = "error"
				result.Error = fmt.Sprintf("unexpected status %d", resp.StatusCode)
			}
			results[idx] = result
		}(i, n)
	}
	wg.Wait()

	type forceReloadResponse struct {
		Results []ForceReloadResult `json:"results"`
	}
	writeJSON(w, http.StatusOK, forceReloadResponse{Results: results})
}

// HandleForceReloadNode handles POST /admin/nodes/{id}/force-reload — sends a
// force-reload signal to a single node identified by its ID.
func (h *NodeHandler) HandleForceReloadNode(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "node ID must be an integer")
		return
	}

	node, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, node.URL+"/admin/force-reload", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+h.jwtSecret)

	resp, err := client.Do(req)
	if err != nil {
		type forceReloadResponse struct {
			Results []ForceReloadResult `json:"results"`
		}
		writeJSON(w, http.StatusOK, forceReloadResponse{Results: []ForceReloadResult{{
			NodeID: node.ID, NodeName: node.Name,
			Status: "error", Error: err.Error(),
		}}})
		return
	}
	resp.Body.Close()

	status := "ok"
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		status = "error"
	}
	type forceReloadResponse struct {
		Results []ForceReloadResult `json:"results"`
	}
	writeJSON(w, http.StatusOK, forceReloadResponse{Results: []ForceReloadResult{{
		NodeID: node.ID, NodeName: node.Name, Status: status,
	}}})
}

// HandleListSessions handles GET /admin/nodes/sessions — lists active playback
// sessions from Redis, optionally filtered by node_id query parameter.
func (h *NodeHandler) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	if h.redisClient == nil {
		writeError(w, http.StatusServiceUnavailable, "redis_unavailable", "Redis not configured")
		return
	}

	ctx := r.Context()
	pattern := "silo:sessions:*"

	if nodeIDStr := r.URL.Query().Get("node_id"); nodeIDStr != "" {
		nodeID, err := strconv.Atoi(nodeIDStr)
		if err == nil {
			node, err := h.repo.GetByID(ctx, nodeID)
			if err == nil {
				hashBytes := sha256.Sum256([]byte(node.URL))
				nodeHash := hex.EncodeToString(hashBytes[:4])
				pattern = "silo:sessions:" + nodeHash + ":*"
			}
		}
	}

	var sessions []json.RawMessage
	var cursor uint64
	for {
		keys, next, err := h.redisClient.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "redis_error", err.Error())
			return
		}
		for _, key := range keys {
			val, err := h.redisClient.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			sessions = append(sessions, json.RawMessage(val))
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	if sessions == nil {
		sessions = []json.RawMessage{}
	}

	type sessionsResponse struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	writeJSON(w, http.StatusOK, sessionsResponse{Sessions: sessions})
}

// reloadPools refreshes the in-memory proxy and transcode pools from the database.
func (h *NodeHandler) reloadPools(ctx context.Context) {
	if h.lister == nil {
		return
	}
	proxyNodes, proxyErr := h.lister.ListEnabled(ctx, nodepool.NodeTypeProxy)
	transcodeNodes, tcErr := h.lister.ListEnabled(ctx, nodepool.NodeTypeTranscode)
	if proxyErr != nil || tcErr != nil {
		slog.WarnContext(ctx, "node pool reload failed", "component", "api", "proxy_err", proxyErr, "transcode_err", tcErr)
		return
	}
	if h.proxyPool != nil {
		h.proxyPool.SetNodes(proxyNodes)
	}
	if h.transcodePool != nil {
		h.transcodePool.SetNodes(transcodeNodes)
	}

	if h.eventBus != nil {
		_ = h.eventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventNodePoolChanged})
	}
}
