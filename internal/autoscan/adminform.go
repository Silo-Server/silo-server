package autoscan

import "encoding/json"

// AdminForm describes per-source configuration fields for a scan source, in the
// same shape the admin UI's generic schema renderer already consumes for plugin
// config (see web/src/components/admin/plugins/SchemaForm.tsx). Reusing that
// shape is deliberate: a scan source's config form is the same kind of thing as
// a plugin's config form, and the renderer for it already exists.
//
// The host never interprets these fields. It carries them from the capability
// manifest to the admin UI, and stores whatever values come back in the
// source's SourceConfig map.
type AdminForm struct {
	Fields      []AdminFormField   `json:"fields"`
	SubmitLabel string             `json:"submit_label,omitempty"`
	Sections    []AdminFormSection `json:"sections,omitempty"`
}

// AdminFormField is one control. Control values match the SDK's
// AdminFormControl enum names as rendered by the admin UI ("TEXT", "TEXTAREA",
// "PASSWORD", "NUMBER", "SWITCH", "SELECT", "MULTI_SELECT"); the host passes
// them through without validating, so a newer control name from a newer plugin
// degrades in the UI rather than being rejected here.
type AdminFormField struct {
	Key            string               `json:"key"`
	Label          string               `json:"label"`
	Description    string               `json:"description,omitempty"`
	Control        string               `json:"control"`
	Placeholder    string               `json:"placeholder,omitempty"`
	Required       bool                 `json:"required,omitempty"`
	Secret         bool                 `json:"secret,omitempty"`
	Multiline      bool                 `json:"multiline,omitempty"`
	DefaultValue   any                  `json:"default_value,omitempty"`
	Options        []AdminFormOption    `json:"options,omitempty"`
	Rows           int                  `json:"rows,omitempty"`
	DynamicOptions bool                 `json:"dynamic_options,omitempty"`
	ShowWhen       []AdminFormCondition `json:"show_when,omitempty"`
	Validation     *AdminFormValidation `json:"validation,omitempty"`
	// FillFrom names a host-known value the admin UI can offer to populate this
	// field from, as a one-click action beside it. It exists so a path-shaped
	// field can be filled from Silo's own library paths without the UI needing
	// to know which plugin it belongs to. Unknown values are ignored by the UI.
	FillFrom string `json:"fill_from,omitempty"`
}

// Control names the admin UI renders. These mirror the SDK's AdminFormControl
// enum; the host only names the ones it builds forms for itself.
const (
	ControlText     = "TEXT"
	ControlTextarea = "TEXTAREA"
	ControlSelect   = "SELECT"
)

// Fill sources the admin UI understands for AdminFormField.FillFrom.
const (
	// FillFromMovieLibraryPaths offers the paths of every enabled movie
	// library; FillFromTVLibraryPaths the same for series libraries. Mixed
	// libraries contribute to both.
	FillFromMovieLibraryPaths = "library_paths_movie"
	FillFromTVLibraryPaths    = "library_paths_tv"
)

type AdminFormOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type AdminFormCondition struct {
	Field  string   `json:"field"`
	Equals []string `json:"equals"`
}

type AdminFormValidation struct {
	HasMin    bool    `json:"has_min,omitempty"`
	Min       float64 `json:"min,omitempty"`
	HasMax    bool    `json:"has_max,omitempty"`
	Max       float64 `json:"max,omitempty"`
	Pattern   string  `json:"pattern,omitempty"`
	MinLength int     `json:"min_length,omitempty"`
	MaxLength int     `json:"max_length,omitempty"`
}

type AdminFormSection struct {
	Key              string               `json:"key"`
	Title            string               `json:"title"`
	Description      string               `json:"description,omitempty"`
	Collapsible      bool                 `json:"collapsible,omitempty"`
	CollapsedDefault bool                 `json:"collapsed_default,omitempty"`
	FieldKeys        []string             `json:"field_keys"`
	ShowWhen         []AdminFormCondition `json:"show_when,omitempty"`
}

// adminFormFromMetadata decodes an admin form out of a decoded-JSON metadata
// value. It round-trips through encoding/json rather than walking the map by
// hand, so the field set stays in sync with the struct tags above.
//
// A malformed form yields nil rather than an error: a plugin with a broken
// config form must still be discoverable and creatable, just without its
// bespoke fields.
func adminFormFromMetadata(value any) *AdminForm {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var form AdminForm
	if err := json.Unmarshal(raw, &form); err != nil {
		return nil
	}
	if len(form.Fields) == 0 {
		return nil
	}
	return &form
}
