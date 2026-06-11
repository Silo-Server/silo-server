package manifest_test

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

func TestLoadAcceptsRichAdminForm(t *testing.T) {
	// arr-style rich capability config_schema: one object json_schema with all
	// properties declared, mixing a static-options SELECT (service_kind), a
	// dynamic-options SELECT (root_folder), a dynamic-options MULTI_SELECT
	// (tags), and a switch with a show_when condition. The dynamic fields carry
	// no static options on purpose — they are populated at runtime via
	// ListConfigOptions, so the dynamic_options exemption must let them load.
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"service_kind\":{\"type\":\"string\"},\"root_folder\":{\"type\":\"string\"},\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"integer\"}},\"is_default\":{\"type\":\"boolean\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"service_kind","label":"Service","control":"ADMIN_FORM_CONTROL_SELECT","options":[{"value":"radarr","label":"Radarr"}]},
	          {"key":"root_folder","label":"Root Folder","control":"ADMIN_FORM_CONTROL_SELECT","dynamic_options":true},
	          {"key":"tags","label":"Tags","control":"ADMIN_FORM_CONTROL_MULTI_SELECT","dynamic_options":true},
	          {"key":"is_default","label":"Default","control":"ADMIN_FORM_CONTROL_SWITCH","show_when":[{"field":"service_kind","equals":["radarr"]}],"validation":{"max_length":0}}
	        ],
	        "sections": [{"key":"main","title":"Main","field_keys":["service_kind","root_folder","tags","is_default"]}]
	      }
	    }]
	  }]
	}`)
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	cap := m.GetCapabilities()[0]
	form := cap.GetConfigSchema()[0].GetAdminForm()
	if len(form.GetSections()) != 1 {
		t.Fatalf("expected 1 section, got %d", len(form.GetSections()))
	}
	if got := form.GetFields()[1].GetControl(); got.String() != "ADMIN_FORM_CONTROL_SELECT" {
		t.Fatalf("root_folder control = %v, want SELECT", got)
	}
	if !form.GetFields()[1].GetDynamicOptions() {
		t.Fatal("root_folder should declare dynamic_options")
	}
	if got := form.GetFields()[2].GetControl(); got.String() != "ADMIN_FORM_CONTROL_MULTI_SELECT" {
		t.Fatalf("tags control = %v, want MULTI_SELECT", got)
	}
	if !form.GetFields()[2].GetDynamicOptions() {
		t.Fatal("tags should declare dynamic_options")
	}
	if len(form.GetFields()[3].GetShowWhen()) != 1 {
		t.Fatal("is_default should carry a show_when condition")
	}
}

// TestLoadValidatesCapabilityConfigSchema proves that capability config_schema
// entries now flow through validateConfigSchema. A static SELECT with no
// options (and no dynamic_options flag) must be rejected by Load.
func TestLoadValidatesCapabilityConfigSchema(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"service_kind\":{\"type\":\"string\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"service_kind","label":"Service","control":"ADMIN_FORM_CONTROL_SELECT"}
	        ]
	      }
	    }]
	  }]
	}`)
	if _, err := manifest.Load(raw); err == nil {
		t.Fatal("Load accepted a capability config_schema with a static SELECT lacking options; want error")
	}
}

// TestMultiSelectRequiresArrayProperty ensures that a MULTI_SELECT field mapped
// to a non-array json_schema property is rejected, and that an array-typed
// MULTI_SELECT passes validation.
func TestMultiSelectRequiresArrayProperty(t *testing.T) {
	// Fail: MULTI_SELECT mapped to "string" property.
	rawBad := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"tags\":{\"type\":\"string\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"tags","label":"Tags","control":"ADMIN_FORM_CONTROL_MULTI_SELECT","options":[{"value":"a","label":"A"}]}
	        ]
	      }
	    }]
	  }]
	}`)
	if _, err := manifest.Load(rawBad); err == nil {
		t.Fatal("Load accepted MULTI_SELECT on a non-array property; want error")
	} else if !strings.Contains(err.Error(), "multi_select control requires an array") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Pass: MULTI_SELECT mapped to "array" property with static options.
	rawGood := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"string\"}}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"tags","label":"Tags","control":"ADMIN_FORM_CONTROL_MULTI_SELECT","options":[{"value":"a","label":"A"}]}
	        ]
	      }
	    }]
	  }]
	}`)
	if _, err := manifest.Load(rawGood); err != nil {
		t.Fatalf("Load rejected valid MULTI_SELECT on array property: %v", err)
	}
}

// TestLoadRejectsUndeclaredCapabilityField proves the json_schema.properties
// declaration rule also applies to capability config_schema: an admin_form
// field whose key is absent from json_schema.properties is rejected.
func TestLoadRejectsUndeclaredCapabilityField(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"service_kind\":{\"type\":\"string\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"ghost","label":"Ghost","control":"ADMIN_FORM_CONTROL_TEXT"}
	        ]
	      }
	    }]
	  }]
	}`)
	if _, err := manifest.Load(raw); err == nil {
		t.Fatal("Load accepted a capability admin_form field absent from json_schema.properties; want error")
	}
}

// TestCrossFieldReferences covers second-pass admin-form cross-field reference
// validation: show_when referencing unknown fields, section.field_keys
// referencing unknown fields, and forward references (declared after usage).

// TestShowWhenReferencesUnknownField ensures a field show_when referencing an
// undeclared field key is rejected.
func TestShowWhenReferencesUnknownField(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"mode\":{\"type\":\"string\"},\"advanced\":{\"type\":\"boolean\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"mode","label":"Mode","control":"ADMIN_FORM_CONTROL_TEXT"},
	          {"key":"advanced","label":"Advanced","control":"ADMIN_FORM_CONTROL_SWITCH",
	           "show_when":[{"field":"nonexistent","equals":["1"]}]}
	        ]
	      }
	    }]
	  }]
	}`)
	_, err := manifest.Load(raw)
	if err == nil {
		t.Fatal("Load accepted show_when referencing unknown field; want error")
	}
	if !strings.Contains(err.Error(), "show_when references unknown field") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestSectionFieldKeysReferencesUnknownField ensures a section.field_keys entry
// referencing an undeclared field key is rejected.
func TestSectionFieldKeysReferencesUnknownField(t *testing.T) {
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"mode\":{\"type\":\"string\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"mode","label":"Mode","control":"ADMIN_FORM_CONTROL_TEXT"}
	        ],
	        "sections": [{"key":"main","title":"Main","field_keys":["mode","ghost"]}]
	      }
	    }]
	  }]
	}`)
	_, err := manifest.Load(raw)
	if err == nil {
		t.Fatal("Load accepted section field_keys referencing unknown field; want error")
	}
	if !strings.Contains(err.Error(), "references unknown field") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestForwardReferenceShowWhenPassesValidation ensures that the rich admin form
// from TestLoadAcceptsRichAdminForm still passes after cross-field ref
// validation is added. The is_default field references service_kind in its
// show_when — a forward reference is valid as long as service_kind is declared
// somewhere in the fields list.
func TestForwardReferenceShowWhenPassesValidation(t *testing.T) {
	// Re-use the same payload as TestLoadAcceptsRichAdminForm to confirm the
	// rich form still passes (regression guard for the second-pass check).
	raw := []byte(`{
	  "plugin_id": "silo.example",
	  "version": "1.0.0",
	  "silo_api_version": "v1",
	  "capabilities": [{
	    "type": "request_router.v1", "id": "arr", "display_name": "X", "description": "Y",
	    "config_schema": [{
	      "key": "connection",
	      "title": "Connection",
	      "json_schema": "{\"type\":\"object\",\"properties\":{\"service_kind\":{\"type\":\"string\"},\"root_folder\":{\"type\":\"string\"},\"tags\":{\"type\":\"array\",\"items\":{\"type\":\"integer\"}},\"is_default\":{\"type\":\"boolean\"}}}",
	      "admin_form": {
	        "fields": [
	          {"key":"service_kind","label":"Service","control":"ADMIN_FORM_CONTROL_SELECT","options":[{"value":"radarr","label":"Radarr"}]},
	          {"key":"root_folder","label":"Root Folder","control":"ADMIN_FORM_CONTROL_SELECT","dynamic_options":true},
	          {"key":"tags","label":"Tags","control":"ADMIN_FORM_CONTROL_MULTI_SELECT","dynamic_options":true},
	          {"key":"is_default","label":"Default","control":"ADMIN_FORM_CONTROL_SWITCH","show_when":[{"field":"service_kind","equals":["radarr"]}]}
	        ],
	        "sections": [{"key":"main","title":"Main","field_keys":["service_kind","root_folder","tags","is_default"]}]
	      }
	    }]
	  }]
	}`)
	if _, err := manifest.Load(raw); err != nil {
		t.Fatalf("Load rejected valid rich admin form with forward-referencing show_when: %v", err)
	}
}
