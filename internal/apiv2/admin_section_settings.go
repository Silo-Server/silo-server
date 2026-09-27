package apiv2

import (
	"context"
	"errors"
	"strconv"
)

type AdminSectionSettingsWriteService interface {
	UpdateAdminSectionSettings(context.Context, bool, func(bool) error) error
}
type AdminSectionSettingsReadInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminSectionSettingsWriteInput struct {
	RawBody     []byte
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        ProfileSectionFlags
}
type AdminSectionSettingsOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   ProfileSectionFlags
}

func adminSectionSettingsTag(ctx context.Context, enabled bool) EntityTag {
	return RenderETag("admin-section-settings:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx), strconv.FormatBool(enabled), 1)
}
func registerAdminSectionSettings(reg *Registry) {
	read := Operation{Operation: humaOp("GET", Prefix+"/admin/settings/sections", "getAdminSectionSettings", "admin-settings", "Read section permission configuration; read failures retain the disabled default."), Class: ClassActingAdmin, ServiceBacked: true, Conditional: true}
	Register(reg, read, func(ctx context.Context, in *AdminSectionSettingsReadInput) (*AdminSectionSettingsOutput, error) {
		if reg.deps.SectionFlags == nil {
			return nil, unavailable("section settings")
		}
		enabled := reg.deps.SectionFlags.AllowProfileCustomSections(ctx)
		tag := adminSectionSettingsTag(ctx, enabled)
		out := &AdminSectionSettingsOutput{ETag: tag.String(), Body: ProfileSectionFlags{AllowProfileCustomSections: enabled}}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	write := Operation{Operation: humaOp("PUT", Prefix+"/admin/settings/sections", "updateAdminSectionSettings", "admin-settings", "Replace section permission configuration under the existing settings transaction guard."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, write, func(ctx context.Context, in *AdminSectionSettingsWriteInput) (*AdminSectionSettingsOutput, error) {
		if reg.deps.AdminSectionSettingsWrite == nil {
			return nil, unavailable("section settings")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		enabled := in.Body.AllowProfileCustomSections
		err := reg.deps.AdminSectionSettingsWrite.UpdateAdminSectionSettings(ctx, enabled, func(current bool) error {
			tag := adminSectionSettingsTag(ctx, current)
			if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
				if p.Status == 412 {
					return StaleVersionProblem(tag)
				}
				return p
			}
			return nil
		})
		if err != nil {
			if p, ok := errors.AsType[*Problem](err); ok {
				return nil, p
			}
			return nil, serviceProblem(err)
		}
		return &AdminSectionSettingsOutput{ETag: adminSectionSettingsTag(ctx, enabled).String(), Body: ProfileSectionFlags{AllowProfileCustomSections: enabled}}, nil
	})
}
