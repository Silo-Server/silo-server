package handlers

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

var ErrAdminAutoscanSourceWriteUnavailable = errors.New("autoscan source writing unavailable")
var ErrAdminAutoscanSourceWriteInvalid = errors.New("invalid autoscan source")

type AdminAutoscanSourceWrite struct {
	PluginID            string
	CapabilityID        string
	ConnectionID        *string
	Enabled             bool
	DeliveryMode        string
	PollIntervalSeconds *int
	PathRewrites        []autoscan.PathRewrite
	SourceConfig        map[string]string
	Label               string
}

// normalizedSourceWrite preserves the bridge's full-state source fields. It
// performs no provider request, scheduling or persistence.
func normalizedSourceWrite(in AdminAutoscanSourceWrite) (autoscan.Source, error) {
	if in.PollIntervalSeconds != nil && (*in.PollIntervalSeconds < 1 || *in.PollIntervalSeconds > 2147483647) {
		return autoscan.Source{}, ErrAdminAutoscanSourceWriteInvalid
	}
	if err := validatePathRewrites(in.PathRewrites); err != nil {
		return autoscan.Source{}, ErrAdminAutoscanSourceWriteInvalid
	}
	mode, err := resolveDeliveryMode(in.DeliveryMode, in.PluginID, in.CapabilityID)
	if err != nil {
		return autoscan.Source{}, ErrAdminAutoscanSourceWriteInvalid
	}
	config := normalizeSourceConfig(in.SourceConfig)
	if mode == autoscan.DeliveryModeWebhook {
		if err := validateWebhookProvider(config); err != nil {
			return autoscan.Source{}, ErrAdminAutoscanSourceWriteInvalid
		}
		if provider, ok := config["webhook_provider"]; ok {
			config["webhook_provider"] = strings.ToLower(strings.TrimSpace(provider))
		}
	}
	return autoscan.Source{PluginID: in.PluginID, CapabilityID: in.CapabilityID, ConnectionID: normalizeConnectionID(in.ConnectionID), Enabled: in.Enabled, DeliveryMode: mode, PollIntervalSeconds: in.PollIntervalSeconds, PathRewrites: normalizePathRewrites(in.PathRewrites), SourceConfig: config, Label: autoscan.NormalizeSourceLabel(in.Label)}, nil
}

func (h *AutoscanHandler) CreateAdminAutoscanSource(ctx context.Context, in AdminAutoscanSourceWrite) (AdminAutoscanSourceView, error) {
	if h == nil || h.repo == nil || h.svc == nil {
		return AdminAutoscanSourceView{}, ErrAdminAutoscanSourceWriteUnavailable
	}
	in.PluginID, in.CapabilityID = strings.TrimSpace(in.PluginID), strings.TrimSpace(in.CapabilityID)
	if in.PluginID == "" || in.CapabilityID == "" {
		return AdminAutoscanSourceView{}, ErrAdminAutoscanSourceWriteInvalid
	}
	source, err := normalizedSourceWrite(in)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	available, err := h.svc.ListAvailableScanSources(ctx)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	if !scanSourceInstalled(available, in.PluginID, in.CapabilityID) {
		return AdminAutoscanSourceView{}, ErrAdminAutoscanSourceWriteInvalid
	}
	created, err := h.repo.CreateSource(ctx, source)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	return sourceResponse(created), nil
}

func (h *AutoscanHandler) UpdateAdminAutoscanSource(ctx context.Context, id string, in AdminAutoscanSourceWrite) (AdminAutoscanSourceView, error) {
	if h == nil || h.repo == nil {
		return AdminAutoscanSourceView{}, ErrAdminAutoscanSourceWriteUnavailable
	}
	existing, err := h.repo.GetSource(ctx, strings.TrimSpace(id))
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	in.PluginID, in.CapabilityID = existing.PluginID, existing.CapabilityID
	if strings.TrimSpace(in.DeliveryMode) == "" {
		in.DeliveryMode = existing.DeliveryMode
	}
	source, err := normalizedSourceWrite(in)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	source.ID = strings.TrimSpace(id)
	updated, err := h.repo.UpdateSource(ctx, source)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	return h.sourceResponseWithWebhook(ctx, updated), nil
}
