package plugins

import (
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// ConfigSchemaView is the transport-safe representation of a manifest config
// schema used by non-admin setup surfaces.
type ConfigSchemaView struct {
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	JSONSchema  string         `json:"json_schema"`
	Required    bool           `json:"required"`
	AdminForm   *AdminFormView `json:"admin_form,omitempty"`
}

type AdminFormView struct {
	Fields      []AdminFormFieldView   `json:"fields"`
	SubmitLabel string                 `json:"submit_label,omitempty"`
	Sections    []AdminFormSectionView `json:"sections,omitempty"`
}

type AdminFormFieldView struct {
	Key                 string                   `json:"key"`
	Label               string                   `json:"label"`
	Description         string                   `json:"description,omitempty"`
	Control             string                   `json:"control"`
	Placeholder         string                   `json:"placeholder,omitempty"`
	Required            bool                     `json:"required"`
	Secret              bool                     `json:"secret"`
	Multiline           bool                     `json:"multiline"`
	DefaultValue        any                      `json:"default_value,omitempty"`
	Options             []AdminFormOptionView    `json:"options,omitempty"`
	Rows                int32                    `json:"rows,omitempty"`
	DynamicOptions      bool                     `json:"dynamic_options,omitempty"`
	ShowWhen            []AdminFormConditionView `json:"show_when,omitempty"`
	Validation          *AdminFormValidationView `json:"validation,omitempty"`
	ExclusiveGroupField string                   `json:"exclusive_group_field,omitempty"`
}

type AdminFormOptionView struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type AdminFormConditionView struct {
	Field  string   `json:"field"`
	Equals []string `json:"equals"`
}

type AdminFormValidationView struct {
	HasMin    bool    `json:"has_min,omitempty"`
	Min       float64 `json:"min,omitempty"`
	HasMax    bool    `json:"has_max,omitempty"`
	Max       float64 `json:"max,omitempty"`
	Pattern   string  `json:"pattern,omitempty"`
	MinLength int32   `json:"min_length,omitempty"`
	MaxLength int32   `json:"max_length,omitempty"`
}

type AdminFormSectionView struct {
	Key              string                   `json:"key"`
	Title            string                   `json:"title"`
	Description      string                   `json:"description,omitempty"`
	Collapsible      bool                     `json:"collapsible"`
	CollapsedDefault bool                     `json:"collapsed_default"`
	FieldKeys        []string                 `json:"field_keys"`
	ShowWhen         []AdminFormConditionView `json:"show_when,omitempty"`
}

func ConfigSchemaViews(schemas []*pluginv1.ConfigSchema) []ConfigSchemaView {
	views := make([]ConfigSchemaView, 0, len(schemas))
	for _, schema := range schemas {
		if schema == nil {
			continue
		}
		views = append(views, ConfigSchemaView{
			Key:         schema.GetKey(),
			Title:       schema.GetTitle(),
			Description: schema.GetDescription(),
			JSONSchema:  schema.GetJsonSchema(),
			Required:    schema.GetRequired(),
			AdminForm:   AdminFormViewFromProto(schema.GetAdminForm()),
		})
	}
	return views
}

func AdminFormViewFromProto(form *pluginv1.AdminFormDescriptor) *AdminFormView {
	if form == nil {
		return nil
	}
	fields := make([]AdminFormFieldView, 0, len(form.GetFields()))
	for _, field := range form.GetFields() {
		if field == nil {
			continue
		}
		options := make([]AdminFormOptionView, 0, len(field.GetOptions()))
		for _, option := range field.GetOptions() {
			if option != nil {
				options = append(options, AdminFormOptionView{Value: option.GetValue(), Label: option.GetLabel(), Description: option.GetDescription()})
			}
		}
		var defaultValue any
		if field.GetDefaultValue() != nil {
			defaultValue = field.GetDefaultValue().AsInterface()
		}
		var validation *AdminFormValidationView
		if value := field.GetValidation(); value != nil {
			validation = &AdminFormValidationView{
				HasMin: value.GetHasMin(), Min: value.GetMin(), HasMax: value.GetHasMax(), Max: value.GetMax(),
				Pattern: value.GetPattern(), MinLength: value.GetMinLength(), MaxLength: value.GetMaxLength(),
			}
		}
		fields = append(fields, AdminFormFieldView{
			Key: field.GetKey(), Label: field.GetLabel(), Description: field.GetDescription(),
			Control:     strings.TrimPrefix(field.GetControl().String(), "ADMIN_FORM_CONTROL_"),
			Placeholder: field.GetPlaceholder(), Required: field.GetRequired(), Secret: field.GetSecret(),
			Multiline: field.GetMultiline(), DefaultValue: defaultValue, Options: options, Rows: field.GetRows(),
			DynamicOptions: field.GetDynamicOptions(), ShowWhen: adminFormConditionViews(field.GetShowWhen()),
			Validation: validation, ExclusiveGroupField: field.GetExclusiveGroupField(),
		})
	}
	sections := make([]AdminFormSectionView, 0, len(form.GetSections()))
	for _, section := range form.GetSections() {
		if section == nil {
			continue
		}
		sections = append(sections, AdminFormSectionView{
			Key: section.GetKey(), Title: section.GetTitle(), Description: section.GetDescription(),
			Collapsible: section.GetCollapsible(), CollapsedDefault: section.GetCollapsedDefault(),
			FieldKeys: append([]string(nil), section.GetFieldKeys()...), ShowWhen: adminFormConditionViews(section.GetShowWhen()),
		})
	}
	return &AdminFormView{Fields: fields, SubmitLabel: form.GetSubmitLabel(), Sections: sections}
}

func adminFormConditionViews(conditions []*pluginv1.AdminFormCondition) []AdminFormConditionView {
	views := make([]AdminFormConditionView, 0, len(conditions))
	for _, condition := range conditions {
		if condition != nil {
			views = append(views, AdminFormConditionView{Field: condition.GetField(), Equals: append([]string(nil), condition.GetEquals()...)})
		}
	}
	return views
}
