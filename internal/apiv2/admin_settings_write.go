package apiv2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminSettingsWriteService interface {
	InspectAdminSettingsSnapshot(context.Context) (handlers.AdminSettingsSnapshot, error)
	UpdateAdminSettings(context.Context, map[string]string, func(handlers.AdminSettingsSnapshot) error) (handlers.AdminSettingsUpdateResult, error)
	UpdateAdminSetting(context.Context, string, string, func(handlers.AdminSettingsSnapshot) error) (handlers.AdminSettingUpdateResult, error)
}

func (reg *Registry) adminSettingsSnapshotTag(ctx context.Context, s handlers.AdminSettingsSnapshot) (EntityTag, error) {
	if len(reg.deps.CursorSecret) == 0 {
		return EntityTag{}, unavailable("settings validator")
	}
	stored, effective := s.ValidatorValues()
	raw, err := json.Marshal(struct{ Stored, Effective map[string]string }{stored, effective})
	if err != nil {
		return EntityTag{}, err
	}
	mac := hmac.New(sha256.New, reg.deps.CursorSecret)
	_, _ = mac.Write(raw)
	return RenderETag("admin-settings:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx), hex.EncodeToString(mac.Sum(nil)), 1), nil
}

type AdminSettingsUpdateInput struct {
	RawBody     []byte
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Values AdminSettingValues `json:"values"`
	}
}
type AdminSettingUpdateInput struct {
	RawBody     []byte
	Key         string `path:"key" minLength:"1" maxLength:"256"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Value string `json:"value"`
	}
}
type AdminSettingsUpdateOutput struct {
	ETag string `header:"ETag"`
	Body AdminSettingsUpdateResult
}
type AdminSettingUpdateOutput struct{ Body AdminSettingValue }
type AdminSettingValue struct {
	Key             string `json:"key"`
	Value           string `json:"value"`
	RestartRequired bool   `json:"restart_required,omitempty"`
}
type AdminSettingsUpdateResult struct {
	Values              AdminSettingValues `json:"values"`
	RestartRequired     bool               `json:"restart_required"`
	RestartRequiredKeys []string           `json:"restart_required_keys,omitempty"`
}

func (reg *Registry) settingsWriteGuard(ctx context.Context, match, none string) func(handlers.AdminSettingsSnapshot) error {
	return func(snapshot handlers.AdminSettingsSnapshot) error {
		tag, err := reg.adminSettingsSnapshotTag(ctx, snapshot)
		if err != nil {
			return err
		}
		if p := EvaluateGuardedPreconditions(match, none, tag); p != nil {
			if p.Status == 412 {
				return StaleVersionProblem(tag)
			}
			return p
		}
		return nil
	}
}
func adminSettingsWriteProblem(err error) error {
	if p, ok := errors.AsType[*Problem](err); ok {
		return p
	}
	if e, ok := errors.AsType[*handlers.APIError](err); ok && e.Status == 400 {
		if e.Code == "storage_unavailable" {
			return NewProblem(TypeValidationFailed, "Storage prerequisites could not be verified.")
		}
		return NewProblem(TypeValidationFailed, e.Message)
	}
	return adminSettingsInspectionProblem(err)
}
func registerAdminSettingsWrite(reg *Registry) {
	op := func(path, id, summary string) Operation {
		o := Operation{Operation: humaOp("PUT", Prefix+"/admin/settings"+path, id, "admin-settings", summary), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNonRetryable}
		o.MaxBodyBytes = 1 << 20
		return o
	}
	Register(reg, op("", "updateAdminSettings", "Validate and merge captured settings under the existing transaction guard; prerequisite probes and runtime notifications are not replayable receipts."), func(ctx context.Context, in *AdminSettingsUpdateInput) (*AdminSettingsUpdateOutput, error) {
		if reg.deps.AdminSettingsWrite == nil {
			return nil, unavailable("administrator settings")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		var raw struct {
			Values map[string]json.RawMessage `json:"values"`
		}
		_ = json.Unmarshal(in.RawBody, &raw)
		for _, value := range raw.Values {
			if string(value) == "null" {
				return nil, NewProblem(TypeValidationFailed, "Setting values cannot be null.")
			}
		}
		out, err := reg.deps.AdminSettingsWrite.UpdateAdminSettings(ctx, in.Body.Values, reg.settingsWriteGuard(ctx, in.IfMatch, in.IfNoneMatch))
		if err != nil {
			return nil, adminSettingsWriteProblem(err)
		}
		response := &AdminSettingsUpdateOutput{Body: AdminSettingsUpdateResult{Values: out.Values, RestartRequired: out.RestartRequired, RestartRequiredKeys: out.RestartRequiredKeys}}
		if out.CommittedSnapshot != nil {
			tag, err := reg.adminSettingsSnapshotTag(ctx, *out.CommittedSnapshot)
			if err != nil {
				return nil, adminSettingsWriteProblem(err)
			}
			response.ETag = tag.String()
		}
		return response, nil
	})
	single := op("/{key}", "updateAdminSetting", "Validate and replace one setting with the established single-key validation rules and transaction guard.")
	single.GuardedReceipt = true
	Register(reg, single, func(ctx context.Context, in *AdminSettingUpdateInput) (*AdminSettingUpdateOutput, error) {
		if reg.deps.AdminSettingsWrite == nil {
			return nil, unavailable("administrator settings")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		out, err := reg.deps.AdminSettingsWrite.UpdateAdminSetting(ctx, in.Key, in.Body.Value, reg.settingsWriteGuard(ctx, in.IfMatch, in.IfNoneMatch))
		if err != nil {
			return nil, adminSettingsWriteProblem(err)
		}
		return &AdminSettingUpdateOutput{Body: AdminSettingValue{Key: out.Key, Value: out.Value, RestartRequired: out.RestartRequired}}, nil
	})
}
