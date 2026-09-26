package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/danielgtaylor/huma/v2"
)

type AdminAutoscanAvailableSourcesService interface {
	ReadAdminAutoscanAvailableSources(context.Context) ([]autoscan.AvailableScanSource, error)
}
type AdminAutoscanAvailableSource struct {
	PluginID     string                    `json:"plugin_id"`
	CapabilityID string                    `json:"capability_id"`
	DisplayName  string                    `json:"display_name"`
	Description  string                    `json:"description,omitempty"`
	Descriptor   AdminScanSourceDescriptor `json:"descriptor"`
}

// ScanSourceDefaultValue carries only the manifest's form default, not stored configuration.
type ScanSourceDefaultValue json.RawMessage

func (v ScanSourceDefaultValue) MarshalJSON() ([]byte, error) {
	return json.RawMessage(v).MarshalJSON()
}
func (ScanSourceDefaultValue) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Description: "Plugin-defined form default JSON value.", Extensions: map[string]any{extExtensionBag: "scan-source-form-default"}}
}

type AdminScanSourceDescriptor struct {
	DeliveryModes    []string             `json:"delivery_modes"`
	Connection       string               `json:"connection"`
	ConnectionKinds  []string             `json:"connection_kinds"`
	EmitsNativePaths bool                 `json:"emits_native_paths"`
	Summary          string               `json:"summary"`
	IconURL          string               `json:"icon_url"`
	ConfigForm       *AdminScanSourceForm `json:"config_form,omitempty"`
}
type AdminAutoscanAvailableSourcesInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminAutoscanAvailableSourcesOutput struct {
	Body Collection[AdminAutoscanAvailableSource]
}
type adminAutoscanAvailableSourcePosition struct{ PluginID, CapabilityID string }

func compareAdminAutoscanAvailableSourcePosition(a, b adminAutoscanAvailableSourcePosition) int {
	return cmp.Or(cmp.Compare(a.PluginID, b.PluginID), cmp.Compare(a.CapabilityID, b.CapabilityID))
}
func adminAutoscanAvailableSourceOf(c autoscan.AvailableScanSource) (AdminAutoscanAvailableSource, error) {
	d := c.Descriptor
	out := AdminAutoscanAvailableSource{PluginID: c.PluginID, CapabilityID: c.CapabilityID, DisplayName: c.DisplayName, Description: c.Description, Descriptor: AdminScanSourceDescriptor{DeliveryModes: append([]string{}, d.DeliveryModes...), Connection: string(d.Connection), ConnectionKinds: append([]string{}, d.ConnectionKinds...), EmitsNativePaths: d.EmitsNativePaths, Summary: d.Summary, IconURL: d.IconURL}}
	if d.ConfigForm != nil {
		form := &AdminScanSourceForm{Fields: make([]AdminScanSourceFormField, 0, len(d.ConfigForm.Fields)), SubmitLabel: d.ConfigForm.SubmitLabel, Sections: scanSourceFormSections(d.ConfigForm.Sections)}
		for _, f := range d.ConfigForm.Fields {
			var def ScanSourceDefaultValue
			if f.DefaultValue != nil {
				raw, err := json.Marshal(f.DefaultValue)
				if err != nil {
					return AdminAutoscanAvailableSource{}, err
				}
				def = raw
			}
			form.Fields = append(form.Fields, AdminScanSourceFormField{Key: f.Key, Label: f.Label, Description: f.Description, Control: f.Control, Placeholder: f.Placeholder, Required: f.Required, Secret: f.Secret, Multiline: f.Multiline, DefaultValue: def, Options: scanSourceFormOptions(f.Options), Rows: f.Rows, DynamicOptions: f.DynamicOptions, ShowWhen: scanSourceFormConditions(f.ShowWhen), Validation: (*AdminScanSourceFormValidation)(f.Validation), FillFrom: f.FillFrom})
		}
		out.Descriptor.ConfigForm = form
	}
	return out, nil
}
func registerAdminAutoscanAvailableSources(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/autoscan/scan-source-plugins", "listAdminAutoscanAvailableSources", "admin-autoscan", "Read installed and built-in scan-source setup descriptors without invoking providers. Each page enumerates the full current discovery list; this is not a snapshot."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanAvailableSourcesInput) (*AdminAutoscanAvailableSourcesOutput, error) {
		if reg.deps.AdminAutoscanAvailableSources == nil {
			return nil, unavailable("autoscan source descriptors")
		}
		scope := CursorScope{OperationID: "listAdminAutoscanAvailableSources", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: strconv.Itoa(in.Limit), Sort: "plugin_id", Tiebreaker: "capability_id"}
		var after adminAutoscanAvailableSourcePosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminAutoscanAvailableSources.ReadAdminAutoscanAvailableSources(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows = slices.Clone(rows)
		slices.SortFunc(rows, func(a, b autoscan.AvailableScanSource) int {
			return compareAdminAutoscanAvailableSourcePosition(adminAutoscanAvailableSourcePosition{a.PluginID, a.CapabilityID}, adminAutoscanAvailableSourcePosition{b.PluginID, b.CapabilityID})
		})
		// Duplicate identities cannot be represented by the continuation tuple.
		for i := 1; i < len(rows); i++ {
			if rows[i].PluginID == rows[i-1].PluginID && rows[i].CapabilityID == rows[i-1].CapabilityID {
				return nil, serviceProblem(errors.New("duplicate scan-source identity"))
			}
		}
		items := make([]AdminAutoscanAvailableSource, 0, in.Limit)
		next := ""
		var last adminAutoscanAvailableSourcePosition
		for _, row := range rows {
			pos := adminAutoscanAvailableSourcePosition{row.PluginID, row.CapabilityID}
			if in.Cursor != "" && compareAdminAutoscanAvailableSourcePosition(pos, after) <= 0 {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			item, err := adminAutoscanAvailableSourceOf(row)
			if err != nil {
				return nil, serviceProblem(err)
			}
			items = append(items, item)
			last = pos
		}
		return &AdminAutoscanAvailableSourcesOutput{Body: Paginated(items, next)}, nil
	})
}

type AdminScanSourceForm struct {
	Fields      []AdminScanSourceFormField   `json:"fields"`
	SubmitLabel string                       `json:"submit_label,omitempty"`
	Sections    []AdminScanSourceFormSection `json:"sections,omitempty"`
}

// AdminScanSourceFormField is one control. Control values match the SDK's
// AdminFormControl enum names as rendered by the admin UI ("TEXT", "TEXTAREA",
// "PASSWORD", "NUMBER", "SWITCH", "SELECT", "MULTI_SELECT"); the host passes
// them through without validating, so a newer control name from a newer plugin
// degrades in the UI rather than being rejected here.
type AdminScanSourceFormField struct {
	Key            string                         `json:"key"`
	Label          string                         `json:"label"`
	Description    string                         `json:"description,omitempty"`
	Control        string                         `json:"control"`
	Placeholder    string                         `json:"placeholder,omitempty"`
	Required       bool                           `json:"required,omitempty"`
	Secret         bool                           `json:"secret,omitempty"`
	Multiline      bool                           `json:"multiline,omitempty"`
	DefaultValue   ScanSourceDefaultValue         `json:"default_value,omitempty"`
	Options        []AdminFormOption              `json:"options,omitempty"`
	Rows           int                            `json:"rows,omitempty"`
	DynamicOptions bool                           `json:"dynamic_options,omitempty"`
	ShowWhen       []AdminFormCondition           `json:"show_when,omitempty"`
	Validation     *AdminScanSourceFormValidation `json:"validation,omitempty"`
	// FillFrom names a host-known value the admin UI can offer to populate this
	// field from, as a one-click action beside it. It exists so a path-shaped
	// field can be filled from Silo's own library paths without the UI needing
	// to know which plugin it belongs to. Unknown values are ignored by the UI.
	FillFrom string `json:"fill_from,omitempty"`
}

type AdminScanSourceFormValidation struct {
	HasMin    bool    `json:"has_min,omitempty"`
	Min       float64 `json:"min,omitempty"`
	HasMax    bool    `json:"has_max,omitempty"`
	Max       float64 `json:"max,omitempty"`
	Pattern   string  `json:"pattern,omitempty"`
	MinLength int     `json:"min_length,omitempty"`
	MaxLength int     `json:"max_length,omitempty"`
}

type AdminScanSourceFormSection struct {
	Key              string               `json:"key"`
	Title            string               `json:"title"`
	Description      string               `json:"description,omitempty"`
	Collapsible      bool                 `json:"collapsible,omitempty"`
	CollapsedDefault bool                 `json:"collapsed_default,omitempty"`
	FieldKeys        []string             `json:"field_keys"`
	ShowWhen         []AdminFormCondition `json:"show_when,omitempty"`
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func scanSourceFormOptions(rows []autoscan.AdminFormOption) []AdminFormOption {
	out := make([]AdminFormOption, 0, len(rows))
	for _, row := range rows {
		out = append(out, AdminFormOption(row))
	}
	return out
}
func scanSourceFormConditions(rows []autoscan.AdminFormCondition) []AdminFormCondition {
	out := make([]AdminFormCondition, 0, len(rows))
	for _, row := range rows {
		condition := AdminFormCondition(row)
		if condition.Equals == nil {
			condition.Equals = []string{}
		}
		out = append(out, condition)
	}
	return out
}

// Source forms omit unset controls and use host-width limits; plugin forms
// retain explicit fields and SDK int32 limits. They are distinct projections.
func scanSourceFormSections(rows []autoscan.AdminFormSection) []AdminScanSourceFormSection {
	out := make([]AdminScanSourceFormSection, 0, len(rows))
	for _, row := range rows {
		out = append(out, AdminScanSourceFormSection{Key: row.Key, Title: row.Title, Description: row.Description, Collapsible: row.Collapsible, CollapsedDefault: row.CollapsedDefault, FieldKeys: nonNilStrings(row.FieldKeys), ShowWhen: scanSourceFormConditions(row.ShowWhen)})
	}
	return out
}
