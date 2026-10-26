package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

var ErrAdminAutoscanSourceWriteUnavailable = errors.New("autoscan source writing unavailable")
var ErrAdminAutoscanSourceWriteInvalid = errors.New("invalid autoscan source")

// ErrAdminAutoscanSourceConnectionRequired rejects an enabled poll source with
// no connection when its descriptor requires one. It wraps
// ErrAdminAutoscanSourceWriteInvalid so callers that only know the general
// validation error still classify it correctly.
var ErrAdminAutoscanSourceConnectionRequired = fmt.Errorf("%w: connection required", ErrAdminAutoscanSourceWriteInvalid)

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
	if missingRequiredConnection(available, source) {
		return AdminAutoscanSourceView{}, ErrAdminAutoscanSourceConnectionRequired
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
	if h.updateMissesRequiredConnection(ctx, source) {
		return AdminAutoscanSourceView{}, ErrAdminAutoscanSourceConnectionRequired
	}
	updated, err := h.repo.UpdateSource(ctx, source)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	return h.sourceResponseWithWebhook(ctx, updated), nil
}

// missingRequiredConnection reports whether source would poll without the
// connection its resolved descriptor requires. Only an enabled poll source is
// held to it: webhook delivery never uses a connection, and a disabled source
// never polls, so an operator can still switch off a source that was saved
// without a server before this rule existed. A capability that is not in the
// discovered list has no resolvable descriptor and is not blocked.
func missingRequiredConnection(available []autoscan.AvailableScanSource, source autoscan.Source) bool {
	if source.ConnectionID != nil || !source.Enabled || source.DeliveryMode != autoscan.DeliveryModePoll {
		return false
	}
	for _, a := range available {
		if a.PluginID == source.PluginID && a.CapabilityID == source.CapabilityID {
			return a.Descriptor.Connection == autoscan.ConnectionRequired
		}
	}
	return false
}

// updateMissesRequiredConnection applies missingRequiredConnection to an
// update. Updates may target an orphaned source whose plugin is gone, so a
// missing service or a failed listing leaves the descriptor unresolved and the
// write proceeds rather than being blocked on discovery.
func (h *AutoscanHandler) updateMissesRequiredConnection(ctx context.Context, source autoscan.Source) bool {
	if h.svc == nil || source.ConnectionID != nil || !source.Enabled || source.DeliveryMode != autoscan.DeliveryModePoll {
		return false
	}
	available, err := h.svc.ListAvailableScanSources(ctx)
	if err != nil {
		slog.WarnContext(ctx, "autoscan: list scan sources for connection requirement failed", "component", "api", "source_id", source.ID, "err", err)
		return false
	}
	return missingRequiredConnection(available, source)
}
