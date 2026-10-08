package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

type AdminNodeDrainStore interface {
	ReadDrain(context.Context, int) (nodepool.DrainConfiguration, error)
	BeginDrain(context.Context, int, func(int64) error) (nodepool.DrainConfiguration, error)
	CancelDrain(context.Context, int, func(int64) error) (*nodepool.Node, error)
}

type AdminNodeDrainView struct {
	Node   *nodepool.Node
	Status workerdrain.Status
}

func (h *NodeHandler) SetDrainStore(store AdminNodeDrainStore) { h.drainStore = store }

func (h *NodeHandler) BeginAdminNodeDrain(ctx context.Context, id int, guard func(int64) error) (AdminNodeDrainView, error) {
	if h == nil || h.drainStore == nil {
		return AdminNodeDrainView{}, ErrAdminNodesUnavailable
	}
	config, err := h.drainStore.BeginDrain(ctx, id, guard)
	if err != nil {
		return AdminNodeDrainView{}, err
	}
	h.configurationChanged()
	return h.observeAdminNodeDrain(ctx, config, http.MethodPost)
}

func (h *NodeHandler) ReadAdminNodeDrain(ctx context.Context, id int) (AdminNodeDrainView, error) {
	if h == nil || h.drainStore == nil {
		return AdminNodeDrainView{}, ErrAdminNodesUnavailable
	}
	config, err := h.drainStore.ReadDrain(ctx, id)
	if err != nil {
		return AdminNodeDrainView{}, err
	}
	return h.observeAdminNodeDrain(ctx, config, http.MethodGet)
}

func (h *NodeHandler) CancelAdminNodeDrain(ctx context.Context, id int, guard func(int64) error) (*nodepool.Node, error) {
	if h == nil || h.drainStore == nil {
		return nil, ErrAdminNodesUnavailable
	}
	node, err := h.drainStore.CancelDrain(ctx, id, guard)
	if err == nil {
		h.configurationChanged()
	}
	return node, err
}

func (h *NodeHandler) observeAdminNodeDrain(ctx context.Context, config nodepool.DrainConfiguration, method string) (AdminNodeDrainView, error) {
	if config.Node == nil || config.FenceID != "" && config.Node.Enabled {
		return AdminNodeDrainView{}, nodepool.ErrNodeDrainConflict
	}
	request, err := http.NewRequestWithContext(ctx, method, nodepool.NodeEndpoint(config.Node.URL, "/admin/drain"), nil)
	if err != nil {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+h.jwtSecret)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	requestedAt := time.Now()
	response, err := telemetry.DoTrustedNode(client, request, "drain")
	if err != nil {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err = decoder.Decode(&fields); err != nil {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	for _, name := range []string{"node_id", "fence_id", "worker_instance_id", "native_server_id", "fenced", "drained", "active_jobs", "active_requests", "active_reservations", "observed_at"} {
		value, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return AdminNodeDrainView{}, workerdrain.ErrUnavailable
		}
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	var status workerdrain.Status
	if err = json.Unmarshal(body, &status); err != nil {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	// Receipt time must belong to this request, allowing bounded clock skew
	// between hosts but never accepting a cached observation as fresh proof.
	if status.ObservedAt.Before(requestedAt.Add(-30*time.Second)) || status.ObservedAt.After(time.Now().Add(30*time.Second)) {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	if status.NodeID != config.Node.ID || status.FenceID != config.FenceID || status.NativeServerID == "" || status.NativeServerID != config.NativeServerID || status.WorkerInstanceID == "" || status.ObservedAt.IsZero() || status.ActiveJobs < 0 || status.ActiveRequests < 0 || status.ActiveReservations < 0 || status.Fenced != (config.FenceID != "") || status.Drained && (!status.Fenced || status.ActiveJobs != 0 || status.ActiveRequests != 0 || status.ActiveReservations != 0) {
		return AdminNodeDrainView{}, workerdrain.ErrUnavailable
	}
	current, err := h.drainStore.ReadDrain(ctx, config.Node.ID)
	if err != nil {
		return AdminNodeDrainView{}, err
	}
	if current.Node == nil || current.Node.AdminRevision != config.Node.AdminRevision || current.Node.URL != config.Node.URL || current.FenceID != config.FenceID || current.NativeServerID != config.NativeServerID {
		return AdminNodeDrainView{}, nodepool.ErrNodeDrainConflict
	}
	return AdminNodeDrainView{Node: config.Node, Status: status}, nil
}
