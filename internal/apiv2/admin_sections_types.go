package apiv2

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// AdminSection is the complete saved definition, including disabled rows.
// Recipe-specific configuration retains the established SectionConfig contract.
type AdminSection struct {
	ID          ID            `json:"id"`
	Scope       string        `json:"scope" enum:"home,library"`
	LibraryID   *ID           `json:"library_id" nullable:"true"`
	Position    int           `json:"position"`
	SectionType string        `json:"section_type"`
	Title       string        `json:"title"`
	Featured    bool          `json:"featured"`
	ItemLimit   int           `json:"item_limit"`
	Config      SectionConfig `json:"config"`
	Enabled     bool          `json:"enabled"`
	CreatedAt   Instant       `json:"created_at"`
	UpdatedAt   Instant       `json:"updated_at"`
}
type AdminSectionCreate struct {
	Scope       string        `json:"scope,omitempty" enum:"home,library" default:"home"`
	LibraryID   *ID           `json:"library_id,omitempty" nullable:"false"`
	Position    int           `json:"position,omitempty" minimum:"0"`
	SectionType string        `json:"section_type" minLength:"1" maxLength:"100"`
	Title       string        `json:"title" maxLength:"500"`
	Featured    bool          `json:"featured,omitempty"`
	ItemLimit   int           `json:"item_limit,omitempty" minimum:"0" maximum:"500"`
	Config      SectionConfig `json:"config,omitzero"`
	Enabled     bool          `json:"enabled,omitempty"`
}
type AdminSectionUpdate struct {
	Position    *int          `json:"position,omitempty" nullable:"false" minimum:"0"`
	SectionType *string       `json:"section_type,omitempty" nullable:"false" minLength:"1" maxLength:"100"`
	Title       *string       `json:"title,omitempty" nullable:"false" minLength:"1" maxLength:"500"`
	Featured    *bool         `json:"featured,omitempty" nullable:"false"`
	ItemLimit   *int          `json:"item_limit,omitempty" nullable:"false" minimum:"1" maximum:"500"`
	Config      SectionConfig `json:"config,omitzero"`
	Enabled     *bool         `json:"enabled,omitempty" nullable:"false"`
}
type AdminSectionOrder struct {
	Scope      string `json:"scope" enum:"home,library"`
	LibraryID  *ID    `json:"library_id" nullable:"true"`
	OrderedIDs []ID   `json:"ordered_ids" uniqueItems:"true"`
}
type AdminSectionOrderBody struct {
	OrderedIDs []ID `json:"ordered_ids" uniqueItems:"true" maxItems:"10000"`
}
type AdminSectionDefaults struct {
	ResetProfiles bool `json:"reset_profiles,omitempty"`
}
type AdminSectionScopeInput struct {
	Scope     string `query:"scope" enum:"home,library" default:"home"`
	LibraryID ID     `query:"library_id"`
}
type AdminSectionIDInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminSectionCreateInput struct {
	Body    AdminSectionCreate
	RawBody []byte
}
type AdminSectionUpdateInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminSectionUpdate
	RawBody     []byte
}
type AdminSectionOrderInput struct {
	AdminSectionScopeInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminSectionOrderBody
	RawBody     []byte
}
type AdminSectionDefaultsInput struct {
	AdminSectionScopeInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminSectionDefaults
	RawBody     []byte
}
type AdminSectionOutput struct {
	ETag string `header:"ETag"`
	Body AdminSection
}
type AdminSectionCreatedOutput struct {
	Location string `header:"Location"`
	Body     AdminSection
}
type AdminSectionListOutput struct{ Body Collection[AdminSection] }
type AdminSectionOrderOutput struct {
	ETag string `header:"ETag"`
	Body AdminSectionOrder
}
type AdminSectionCapabilities struct {
	Capability
	Available     bool `json:"available"`
	ResetProfiles bool `json:"reset_profiles"`
	Preview       bool `json:"preview"`
}
type AdminSectionCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminSectionCapabilities
}
type AdminSectionBulkCreate struct {
	Scope       string        `json:"scope,omitempty" enum:"home,library" default:"home"`
	LibraryIDs  []ID          `json:"library_ids,omitempty" uniqueItems:"true" maxItems:"100"`
	SectionType string        `json:"section_type" minLength:"1" maxLength:"100"`
	Title       string        `json:"title" maxLength:"500"`
	Featured    bool          `json:"featured,omitempty"`
	ItemLimit   int           `json:"item_limit,omitempty" minimum:"0" maximum:"500"`
	Config      SectionConfig `json:"config,omitzero"`
	Enabled     bool          `json:"enabled,omitempty"`
}
type AdminSectionBulkInput struct {
	Body    AdminSectionBulkCreate
	RawBody []byte
}
type AdminSectionBulkResult struct {
	Created int `json:"created" minimum:"0"`
}
type AdminSectionBulkOutput struct{ Body AdminSectionBulkResult }
type AdminSectionPreview struct {
	SectionType string        `json:"section_type" minLength:"1" maxLength:"100"`
	Config      SectionConfig `json:"config,omitzero"`
	ItemLimit   int           `json:"item_limit,omitempty" minimum:"0" maximum:"50"`
	LibraryID   *ID           `json:"library_id,omitempty" nullable:"false"`
	LibraryIDs  []ID          `json:"library_ids,omitempty" uniqueItems:"true" maxItems:"1000"`
}
type AdminSectionPreviewInput struct {
	Body    AdminSectionPreview
	RawBody []byte
}
type AdminSectionPreviewResult struct {
	Collection[CatalogItem]
	TotalCount int `json:"total_count" minimum:"0"`
}
type AdminSectionPreviewOutput struct{ Body AdminSectionPreviewResult }

func adminSectionOf(v handlers.AdminSection) (AdminSection, *Problem) {
	out := AdminSection{ID: ID(v.ID), Scope: v.Scope, Position: v.Position, SectionType: v.SectionType, Title: v.Title, Featured: v.Featured, ItemLimit: v.ItemLimit, Enabled: v.Enabled, Config: SectionConfig{}}
	if v.LibraryID != nil {
		out.LibraryID = new(ID(strconv.Itoa(*v.LibraryID)))
	}
	if len(v.Config) > 0 {
		if err := json.Unmarshal(v.Config, &out.Config); err != nil {
			return out, NewProblem(TypeInternalError, "Saved section configuration is invalid.")
		}
	}
	if out.Config == nil {
		out.Config = SectionConfig{}
	}
	created, err := time.Parse(time.RFC3339Nano, v.CreatedAt)
	if err != nil {
		return out, NewProblem(TypeInternalError, "Saved section timestamp is invalid.")
	}
	updated, err := time.Parse(time.RFC3339Nano, v.UpdatedAt)
	if err != nil {
		return out, NewProblem(TypeInternalError, "Saved section timestamp is invalid.")
	}
	out.CreatedAt, out.UpdatedAt = NewInstant(created), NewInstant(updated)
	return out, nil
}
func adminSectionListOf(values []handlers.AdminSection) (Collection[AdminSection], *Problem) {
	out := NewCollection([]AdminSection{})
	for _, v := range values {
		item, p := adminSectionOf(v)
		if p != nil {
			return out, p
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
func adminSectionOrderOf(v handlers.AdminSectionOrderView) AdminSectionOrder {
	out := AdminSectionOrder{Scope: v.Scope, OrderedIDs: make([]ID, len(v.OrderedIDs))}
	if v.LibraryID != nil {
		out.LibraryID = new(ID(strconv.Itoa(*v.LibraryID)))
	}
	for i, id := range v.OrderedIDs {
		out.OrderedIDs[i] = ID(id)
	}
	return out
}
func adminSectionConfig(config SectionConfig) json.RawMessage {
	if config == nil {
		return nil
	}
	raw, _ := json.Marshal(config) // Values have already passed JSON decoding.
	return raw
}
func adminSectionLibrary(scope string, id ID) (*int, *Problem) {
	if scope == scopeHome && id != "" {
		return nil, NewProblem(TypeValidationFailed, "library_id must be omitted for home sections.")
	}
	if scope == scopeLibrary && id == "" {
		return nil, NewProblem(TypeValidationFailed, "library_id is required for library sections.")
	}
	if scope != scopeHome && scope != scopeLibrary {
		return nil, NewProblem(TypeValidationFailed, "scope must be home or library.")
	}
	if id == "" {
		return nil, nil
	}
	n, p := libraryID(id)
	if p != nil {
		return nil, p
	}
	return &n, nil
}

func (c AdminSectionCapabilities) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
