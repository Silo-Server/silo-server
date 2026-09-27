package handlers

import (
	"context"
	"errors"
	"maps"
	"slices"
)

var ErrAdminSettingsUnavailable = errors.New("administrator settings unavailable")

// InspectAdminSettings shares runtime defaults and secret redaction with the
// bridge handlers. Clone before redacting: stores may return a cached map.
func (h *AdminHandler) InspectAdminSettings(ctx context.Context, effective bool) (map[string]string, error) {
	if h.SettingsRepo == nil {
		return nil, ErrAdminSettingsUnavailable
	}
	values, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	values = maps.Clone(values)
	if effective {
		values = h.effectiveAdminSettings(values)
	}
	if values == nil {
		values = map[string]string{}
	}
	redactAdminSettings(values)
	return values, nil
}

type AdminSensitiveSettingsStatus struct {
	Configured   []string
	ManagedByEnv []string
}

func (h *AdminHandler) InspectAdminSensitiveSettings(ctx context.Context) (AdminSensitiveSettingsStatus, error) {
	if h.SettingsRepo == nil {
		return AdminSensitiveSettingsStatus{}, ErrAdminSettingsUnavailable
	}
	all, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return AdminSensitiveSettingsStatus{}, err
	}
	return h.adminSensitiveSettingsStatus(all), nil
}

func (h *AdminHandler) adminSensitiveSettingsStatus(all map[string]string) AdminSensitiveSettingsStatus {
	configured := map[string]struct{}{}
	for key := range sensitiveSettingKeys {
		if all[key] != "" || h.BootstrapSensitiveConfigured[key] || h.BootstrapSensitiveValues[key] != "" {
			configured[key] = struct{}{}
		}
	}
	out := AdminSensitiveSettingsStatus{Configured: slices.Sorted(maps.Keys(configured)), ManagedByEnv: []string{}}
	if out.Configured == nil {
		out.Configured = []string{}
	}
	for key, present := range h.BootstrapSensitiveConfigured {
		if present {
			out.ManagedByEnv = append(out.ManagedByEnv, key)
		}
	}
	slices.Sort(out.ManagedByEnv)
	return out
}

// AdminSettingsSnapshot stays inside the server. Raw values are used only to
// construct a keyed validator; transports must return the redacted projections.
type AdminSettingsSnapshot struct {
	Stored           map[string]string
	Effective        map[string]string
	VisibleStored    map[string]string
	VisibleEffective map[string]string
}

// ValidatorValues keeps secret settings in the guarded settings revision while
// excluding internal task state that administrators cannot read or edit.
func (s AdminSettingsSnapshot) ValidatorValues() (stored, effective map[string]string) {
	stored = maps.Clone(s.Stored)
	effective = maps.Clone(s.Effective)
	isMachineManaged := func(key, _ string) bool { return machineManagedSettingKeys[key] }
	maps.DeleteFunc(stored, isMachineManaged)
	maps.DeleteFunc(effective, isMachineManaged)
	return stored, effective
}

func (h *AdminHandler) adminSettingsSnapshot(stored map[string]string) AdminSettingsSnapshot {
	raw := maps.Clone(stored)
	effective := h.effectiveAdminSettings(raw)
	visibleStored := maps.Clone(raw)
	visibleEffective := maps.Clone(effective)
	redactAdminSettings(visibleStored)
	redactAdminSettings(visibleEffective)
	return AdminSettingsSnapshot{Stored: raw, Effective: effective, VisibleStored: visibleStored, VisibleEffective: visibleEffective}
}
func (h *AdminHandler) InspectAdminSettingsSnapshot(ctx context.Context) (AdminSettingsSnapshot, error) {
	if h.SettingsRepo == nil {
		return AdminSettingsSnapshot{}, ErrAdminSettingsUnavailable
	}
	stored, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return AdminSettingsSnapshot{}, err
	}
	return h.adminSettingsSnapshot(stored), nil
}
