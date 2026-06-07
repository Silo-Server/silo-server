package handlers

import (
	"encoding/json"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// TestAdminFormToJSONSchemaFields locks the bridge between the SDK proto
// AdminForm descriptor and the JSON the frontend SchemaForm consumes. Without
// this, the schema-driven request-connection form silently loses its
// dynamic dropdowns (dynamic_options), conditional visibility (show_when),
// validation, sections, and the MULTI_SELECT control mapping, which makes the
// new form non-functional (required fields can never be set, save stays
// disabled).
func TestAdminFormToJSONSchemaFields(t *testing.T) {
	descriptor := &pluginv1.AdminFormDescriptor{
		SubmitLabel: "Connect",
		Fields: []*pluginv1.AdminFormField{
			{
				Key:            "quality_profile_ids",
				Label:          "Quality Profiles",
				Control:        pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_MULTI_SELECT,
				DynamicOptions: true,
				ShowWhen: []*pluginv1.AdminFormCondition{
					{Field: "service_type", Equals: []string{"radarr", "sonarr"}},
				},
				Validation: &pluginv1.AdminFormValidation{
					HasMin:    true,
					Min:       1,
					HasMax:    true,
					Max:       10,
					Pattern:   "^[0-9]+$",
					MinLength: 1,
					MaxLength: 64,
				},
			},
		},
		Sections: []*pluginv1.AdminFormSection{
			{
				Key:              "advanced",
				Title:            "Advanced",
				Description:      "Optional tuning",
				Collapsible:      true,
				CollapsedDefault: true,
				FieldKeys:        []string{"quality_profile_ids"},
				ShowWhen: []*pluginv1.AdminFormCondition{
					{Field: "service_type", Equals: []string{"radarr"}},
				},
			},
		},
	}

	form := adminFormToJSON(descriptor)
	if form == nil {
		t.Fatal("adminFormToJSON returned nil")
	}

	raw, err := json.Marshal(form)
	if err != nil {
		t.Fatalf("marshal admin form: %v", err)
	}

	var decoded struct {
		Fields []struct {
			Control        string `json:"control"`
			DynamicOptions bool   `json:"dynamic_options"`
			ShowWhen       []struct {
				Field  string   `json:"field"`
				Equals []string `json:"equals"`
			} `json:"show_when"`
			Validation *struct {
				HasMin    bool    `json:"has_min"`
				Min       float64 `json:"min"`
				HasMax    bool    `json:"has_max"`
				Max       float64 `json:"max"`
				Pattern   string  `json:"pattern"`
				MinLength int32   `json:"min_length"`
				MaxLength int32   `json:"max_length"`
			} `json:"validation"`
		} `json:"fields"`
		Sections []struct {
			Key              string   `json:"key"`
			Title            string   `json:"title"`
			Description      string   `json:"description"`
			Collapsible      bool     `json:"collapsible"`
			CollapsedDefault bool     `json:"collapsed_default"`
			FieldKeys        []string `json:"field_keys"`
			ShowWhen         []struct {
				Field  string   `json:"field"`
				Equals []string `json:"equals"`
			} `json:"show_when"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal admin form: %v\njson: %s", err, raw)
	}

	if len(decoded.Fields) != 1 {
		t.Fatalf("expected 1 field, got %d", len(decoded.Fields))
	}
	field := decoded.Fields[0]

	// Control must serialize to the SHORT form the frontend switches on.
	if field.Control != "MULTI_SELECT" {
		t.Errorf("control = %q, want %q", field.Control, "MULTI_SELECT")
	}
	if !field.DynamicOptions {
		t.Error("dynamic_options = false, want true")
	}
	if len(field.ShowWhen) != 1 || field.ShowWhen[0].Field != "service_type" ||
		len(field.ShowWhen[0].Equals) != 2 {
		t.Errorf("field show_when not serialized correctly: %+v", field.ShowWhen)
	}
	if field.Validation == nil {
		t.Fatal("validation not serialized")
	}
	if !field.Validation.HasMin || field.Validation.Min != 1 ||
		!field.Validation.HasMax || field.Validation.Max != 10 ||
		field.Validation.Pattern != "^[0-9]+$" ||
		field.Validation.MinLength != 1 || field.Validation.MaxLength != 64 {
		t.Errorf("validation not serialized correctly: %+v", field.Validation)
	}

	if len(decoded.Sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(decoded.Sections))
	}
	section := decoded.Sections[0]
	if section.Key != "advanced" || section.Title != "Advanced" ||
		section.Description != "Optional tuning" || !section.Collapsible ||
		!section.CollapsedDefault {
		t.Errorf("section scalar fields not serialized correctly: %+v", section)
	}
	if len(section.FieldKeys) != 1 || section.FieldKeys[0] != "quality_profile_ids" {
		t.Errorf("section field_keys not serialized correctly: %+v", section.FieldKeys)
	}
	if len(section.ShowWhen) != 1 || section.ShowWhen[0].Field != "service_type" ||
		len(section.ShowWhen[0].Equals) != 1 {
		t.Errorf("section show_when not serialized correctly: %+v", section.ShowWhen)
	}
}
