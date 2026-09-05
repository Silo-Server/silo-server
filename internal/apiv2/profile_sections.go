package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The profile sections domain: the acting profile's customization of the
// home and library pages (hidden, reordered, and profile-built rows). Every
// operation acts on one override set, addressed by scope and, for a library
// page, library_id, the same way on GET, PUT and DELETE. v1 answered the read
// in PascalCase and took the write in snake_case; v2 is snake_case on both.

// SectionConfig is a recipe's configuration document. Its keys are fixed by
// the recipe named in section_type, not by this contract. Presence carries
// meaning: a nil map is an absent member (no config saved, so the section's
// own config applies) and an empty non-nil map is an explicitly empty one,
// so every member of this type is tagged omitzero, never omitempty, which
// would drop {} on the read and let a full-replacement PUT lose it.
type SectionConfig map[string]any

// Schema declares the config as a named extension bag.
func (SectionConfig) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:                 "object",
		Description:          "A recipe's configuration document; its keys are fixed by the recipe named in section_type.",
		AdditionalProperties: true,
		Extensions:           map[string]any{extExtensionBag: "section-config"},
	}
}

// SectionOverride is one saved customization: of an admin section
// (section_id set) or a profile-built section (section_id empty, the recipe
// in user_section_type).
type SectionOverride struct {
	ID              string          `json:"id" doc:"The override's identifier; empty when the client saved none" example:"c6b0f2a8-1a3e-4d55-9a6f-0b7e6c1d2f30"`
	SectionID       string          `json:"section_id" doc:"The admin section this customizes; empty for a profile-built section" example:"s-continue-watching"`
	Position        *int            `json:"position" nullable:"true" doc:"Position override; null keeps the admin order" example:"2"`
	Hidden          bool            `json:"hidden" example:"false"`
	Removed         bool            `json:"removed" doc:"Removed from the page by the profile" example:"false"`
	SectionType     string          `json:"section_type" doc:"Legacy recipe name; empty when unset" example:"recently_added"`
	Title           string          `json:"title" doc:"Title override; empty keeps the admin title" example:"New this week"`
	Featured        *bool           `json:"featured" nullable:"true" doc:"Featured override; null keeps the admin value" example:"false"`
	ItemLimit       *int            `json:"item_limit" nullable:"true" doc:"Item-limit override; null keeps the admin value" example:"20"`
	Config          SectionConfig   `json:"config,omitzero" doc:"Legacy config override; absent when the profile saved none (the section's own config applies), {} when it saved an empty one"`
	IsUserAdded     bool            `json:"is_user_added" doc:"A profile-built section rather than a customization of an admin one" example:"false"`
	UserSectionType string          `json:"user_section_type" doc:"Recipe of a profile-built section; empty otherwise" example:"hidden_gems"`
	UserConfig      SectionConfig   `json:"user_config,omitzero" doc:"Config of a profile-built section; absent when the profile saved none (config applies), {} when it saved an empty one"`
	UserTitle       string          `json:"user_title" doc:"Title of a profile-built section; empty otherwise" example:"Hidden gems"`
	CreatedAt       NullableInstant `json:"created_at" doc:"When the row was saved; null when the store did not record it" example:"2026-01-02T03:04:05.000Z"`
	UpdatedAt       NullableInstant `json:"updated_at" doc:"When the row was last saved; null when the store did not record it" example:"2026-01-02T03:04:05.000Z"`
}

// SectionOverrideCollection is the listProfileSectionOverrides envelope: one
// page's override set, bounded by the page's sections, so not paginated.
type SectionOverrideCollection struct {
	Collection[SectionOverride]
}

// SectionOverrideCollectionOutput is the listProfileSectionOverrides response.
type SectionOverrideCollectionOutput struct {
	Body SectionOverrideCollection
}

// SectionOverrideWrite is one override as the client saves it. Omitted
// members take v1's defaults (false, empty, no override); position, featured
// and item_limit also admit null as "no override".
type SectionOverrideWrite struct {
	ID              *string       `json:"id,omitempty" nullable:"false" maxLength:"128" doc:"Client-chosen identifier; a UUID by convention" example:"c6b0f2a8-1a3e-4d55-9a6f-0b7e6c1d2f30"`
	SectionID       *string       `json:"section_id,omitempty" nullable:"false" maxLength:"128" doc:"The admin section to customize; omitted or empty for a profile-built section" example:"s-continue-watching"`
	Position        *int          `json:"position,omitempty" nullable:"true" minimum:"0" doc:"Position override; null or omitted keeps the admin order" example:"2"`
	Hidden          *bool         `json:"hidden,omitempty" nullable:"false" example:"false"`
	Removed         *bool         `json:"removed,omitempty" nullable:"false" example:"false"`
	SectionType     *string       `json:"section_type,omitempty" nullable:"false" maxLength:"64" doc:"Legacy recipe name; user_section_type is preferred for a profile-built section" example:"recently_added"`
	Title           *string       `json:"title,omitempty" nullable:"false" maxLength:"200" doc:"Title override; empty keeps the admin title" example:"New this week"`
	Featured        *bool         `json:"featured,omitempty" nullable:"true" doc:"Featured override; null or omitted keeps the admin value" example:"false"`
	ItemLimit       *int          `json:"item_limit,omitempty" nullable:"true" minimum:"1" doc:"Item-limit override; null or omitted keeps the admin value" example:"20"`
	Config          SectionConfig `json:"config,omitzero" doc:"Legacy config override; {} saves an explicitly empty one, omitted saves none. Validated by the recipe on a profile-built section"`
	IsUserAdded     *bool         `json:"is_user_added,omitempty" nullable:"false" doc:"A profile-built section; an empty section_id implies it" example:"false"`
	UserSectionType *string       `json:"user_section_type,omitempty" nullable:"false" maxLength:"64" doc:"Recipe of a profile-built section; must be registered on this server" example:"hidden_gems"`
	UserConfig      SectionConfig `json:"user_config,omitzero" doc:"Config of a profile-built section; {} saves an explicitly empty one, omitted saves none. Validated by the recipe"`
	UserTitle       *string       `json:"user_title,omitempty" nullable:"false" maxLength:"200" example:"Hidden gems"`
}

// SectionOverrideSet is the replaceProfileSectionOverrides body: the whole
// override set for the addressed page. An empty array clears it.
type SectionOverrideSet struct {
	Overrides []SectionOverrideWrite `json:"overrides" doc:"The page's complete override set; replaces what was saved" maxItems:"500"`
}

// SectionOverridesScopeInput addresses one override set.
type SectionOverridesScopeInput struct {
	Scope     string `query:"scope" enum:"home,library" default:"home" doc:"The page the overrides apply to" example:"home"`
	LibraryID ID     `query:"library_id" doc:"The library; required when scope is library and refused otherwise" example:"1"`
}

// SectionOverridesReplaceInput is the replaceProfileSectionOverrides request.
type SectionOverridesReplaceInput struct {
	SectionOverridesScopeInput
	Body SectionOverrideSet
	// RawBody is the document as sent; see ProfileUpdateInput.
	RawBody []byte
}

// ProfileSectionSetting is one row of the page as the profile sees it in
// the customization screen: the admin definition with the profile's
// overrides applied.
type ProfileSectionSetting struct {
	ID          string        `json:"id" example:"s-continue-watching"`
	SectionType string        `json:"section_type" doc:"The recipe; the set is extensible" example:"continue_watching"`
	Title       string        `json:"title" example:"Continue Watching"`
	Featured    bool          `json:"featured" example:"false"`
	ItemLimit   int           `json:"item_limit" example:"20"`
	Hidden      bool          `json:"hidden" doc:"Hidden by the profile" example:"false"`
	IsCustom    bool          `json:"is_custom" doc:"Built by the profile rather than defined by an administrator" example:"false"`
	Customized  bool          `json:"customized" doc:"An admin section the profile has changed" example:"true"`
	Position    int           `json:"position" example:"0"`
	Config      SectionConfig `json:"config,omitzero" doc:"The recipe's effective config; absent when none, {} when explicitly empty"`
}

// ProfileSectionSettingCollection is the getProfileSectionSettings envelope.
type ProfileSectionSettingCollection struct {
	Collection[ProfileSectionSetting]
}

// ProfileSectionSettingCollectionOutput is the getProfileSectionSettings response.
type ProfileSectionSettingCollectionOutput struct {
	Body ProfileSectionSettingCollection
}

// ProfileSectionFlags is what this server lets profiles do to their pages.
type ProfileSectionFlags struct {
	AllowProfileCustomSections bool `json:"allow_profile_custom_sections" doc:"Whether non-admin profiles may build sections from admin-only recipes" example:"false"`
}

// ProfileSectionFlagsOutput is the getProfileSectionFlags response.
type ProfileSectionFlagsOutput struct {
	Body ProfileSectionFlags
}

const (
	locationScope     = "query.scope"
	locationLibraryID = "query.library_id"
	locationOverrides = locationBody + ".overrides"
)

// The two page scopes an override set can address.
const (
	scopeHome    = "home"
	scopeLibrary = "library"
)

// sectionOverrideWriteNullable names the override members whose null means
// "no override"; null on any other member is a type failure.
var sectionOverrideWriteNullable = map[string]bool{"position": true, "featured": true, "item_limit": true}

func registerProfileSections(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/profile/sections", "listProfileSectionOverrides", "profile_sections",
			"List the acting profile's saved section overrides for one page."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.listProfileSectionOverrides)

	replace := humaOp(http.MethodPut, Prefix+"/profile/sections", "replaceProfileSectionOverrides", "profile_sections",
		"Replace the acting profile's section overrides for one page.")
	// v1 refuses a profile-built section of an admin-only recipe with 403
	// custom_disabled unless the server allows it; the status is this
	// operation's own, not one the class implies.
	replace.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{
		Operation:      replace,
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.replaceProfileSectionOverrides)

	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, Prefix+"/profile/sections", "resetProfileSectionOverrides", "profile_sections",
			"Delete the acting profile's section overrides for one page, restoring the admin layout."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.resetProfileSectionOverrides)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/profile/sections/settings", "getProfileSectionSettings", "profile_sections",
			"Get one page's sections as the acting profile sees them, with its overrides applied."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.getProfileSectionSettings)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/profile/sections/flags", "getProfileSectionFlags", "profile_sections",
			"Get what this server lets profiles do to their pages."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.getProfileSectionFlags)
}

// overridesQuery reduces the addressed page to the store's key: scope and
// library_id agree (v1 stored whatever it was sent), and the library id is
// in its canonical form.
func overridesQuery(ctx context.Context, in SectionOverridesScopeInput) (handlers.SectionOverridesQuery, *int, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return handlers.SectionOverridesQuery{}, nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	q := handlers.SectionOverridesQuery{UserID: claims.UserID, ProfileID: profileFrom(ctx), Scope: in.Scope}
	var libraryID *int
	switch {
	case in.Scope == scopeLibrary && in.LibraryID == "":
		return q, nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationLibraryID, Code: codeRequired, Detail: "library_id is required for library sections"})
	case in.Scope == scopeHome && in.LibraryID != "":
		return q, nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationLibraryID, Code: codeInvalid, Detail: "library_id must not be set for home sections"})
	case in.LibraryID != "":
		n, err := intOfID(in.LibraryID)
		if err != nil || n <= 0 {
			return q, nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationLibraryID, Code: codeInvalid, Detail: "expected a library identifier"})
		}
		libraryID = &n
		q.LibraryID = strconv.Itoa(n)
	}
	return q, libraryID, nil
}

// listProfileSectionOverrides answers from the same read v1 GET
// /profile/sections uses.
func (reg *Registry) listProfileSectionOverrides(ctx context.Context, in *SectionOverridesScopeInput) (*SectionOverrideCollectionOutput, error) {
	if reg.deps.ProfileSections == nil {
		return nil, unavailable("section")
	}
	q, _, p := overridesQuery(ctx, *in)
	if p != nil {
		return nil, p
	}
	rows, err := reg.deps.ProfileSections.ListProfileOverrides(ctx, q)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]SectionOverride, 0, len(rows))
	for _, row := range rows {
		item, p := sectionOverrideOf(row)
		if p != nil {
			return nil, p
		}
		items = append(items, item)
	}
	return &SectionOverrideCollectionOutput{Body: SectionOverrideCollection{Collection: NewCollection(items)}}, nil
}

// replaceProfileSectionOverrides runs the same recipe gate and write v1 PUT
// /profile/sections does.
func (reg *Registry) replaceProfileSectionOverrides(ctx context.Context, in *SectionOverridesReplaceInput) (*struct{}, error) {
	if reg.deps.ProfileSections == nil {
		return nil, unavailable("section")
	}
	q, _, p := overridesQuery(ctx, in.SectionOverridesScopeInput)
	if p != nil {
		return nil, p
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	if p := rejectOverrideNulls(in.RawBody); p != nil {
		return nil, p
	}
	writes := make([]handlers.SectionOverrideWrite, 0, len(in.Body.Overrides))
	for i, o := range in.Body.Overrides {
		w, p := o.toWrite(i)
		if p != nil {
			return nil, p
		}
		writes = append(writes, w)
	}
	if err := reg.deps.ProfileSections.SaveProfileOverrides(ctx, q, writes); err != nil {
		return nil, sectionProblem(err)
	}
	return nil, nil
}

// resetProfileSectionOverrides runs the same delete v1 DELETE
// /profile/sections/reset does.
func (reg *Registry) resetProfileSectionOverrides(ctx context.Context, in *SectionOverridesScopeInput) (*struct{}, error) {
	if reg.deps.ProfileSections == nil {
		return nil, unavailable("section")
	}
	q, _, p := overridesQuery(ctx, *in)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.ProfileSections.ResetProfileOverrides(ctx, q); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

// getProfileSectionSettings answers from the same resolution v1 GET
// /profile/sections/settings uses, including its viewer-access filter.
func (reg *Registry) getProfileSectionSettings(ctx context.Context, in *SectionOverridesScopeInput) (*ProfileSectionSettingCollectionOutput, error) {
	if reg.deps.ProfileSections == nil {
		return nil, unavailable("section")
	}
	q, libraryID, p := overridesQuery(ctx, *in)
	if p != nil {
		return nil, p
	}
	// The filter's device id only scopes device settings, which this read
	// has none of; the library and rating limits come from the viewer scope.
	resolved, err := reg.deps.ProfileSections.ResolveProfileSectionSettings(ctx, q.UserID, q.ProfileID, q.Scope, libraryID, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]ProfileSectionSetting, 0, len(resolved))
	for _, s := range resolved {
		config, p := sectionConfigOf(string(s.Config))
		if p != nil {
			return nil, p
		}
		items = append(items, ProfileSectionSetting{
			ID:          s.ID,
			SectionType: string(s.SectionType),
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			Hidden:      s.Hidden,
			IsCustom:    s.IsCustom,
			Customized:  s.Customized,
			Position:    s.Position,
			Config:      config,
		})
	}
	return &ProfileSectionSettingCollectionOutput{Body: ProfileSectionSettingCollection{Collection: NewCollection(items)}}, nil
}

// getProfileSectionFlags reads the same setting v1 GET /profile/sections/flags
// does.
func (reg *Registry) getProfileSectionFlags(ctx context.Context, _ *struct{}) (*ProfileSectionFlagsOutput, error) {
	if reg.deps.SectionFlags == nil {
		return nil, unavailable("section")
	}
	return &ProfileSectionFlagsOutput{Body: ProfileSectionFlags{AllowProfileCustomSections: reg.deps.SectionFlags.AllowProfileCustomSections(ctx)}}, nil
}

// sectionProblem maps the v1 decision onto problem types: a rejected recipe
// or config is a validation failure at the override set, and the
// custom-sections refusal keeps its 403.
func sectionProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // SaveProfileOverrides returns the value directly
	if !ok {
		return serviceProblem(err)
	}
	if apiErr.Status == http.StatusBadRequest {
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationOverrides, Code: codeInvalid, Detail: apiErr.Message})
	}
	return serviceProblem(err)
}

// rejectOverrideNulls is the omitted-versus-null rule for the members of
// each override: the framework treats null on an optional member as absent,
// and only position, featured and item_limit admit that reading.
func rejectOverrideNulls(raw []byte) *Problem {
	var body struct {
		Overrides []map[string]json.RawMessage `json:"overrides"`
	}
	_ = json.Unmarshal(raw, &body)
	var errs []ProblemError
	for i, members := range body.Overrides {
		for name, v := range members {
			if bytes.Equal(bytes.TrimSpace(v), jsonNull) && !sectionOverrideWriteNullable[name] {
				errs = append(errs, ProblemError{Location: locationOverrides + "[" + strconv.Itoa(i) + "]." + name, Code: codeInvalidType, Detail: "null is not a value for this member; omit it"})
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Location < errs[j].Location })
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").WithErrors(errs...)
}

// toWrite lowers one override onto the v1 member shape, where an omitted
// member is the zero value v1 decodes for an absent one.
func (o SectionOverrideWrite) toWrite(index int) (handlers.SectionOverrideWrite, *Problem) {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	flag := func(p *bool) bool { return p != nil && *p }
	config, p := rawConfigOf(o.Config, index, "config")
	if p != nil {
		return handlers.SectionOverrideWrite{}, p
	}
	userConfig, p := rawConfigOf(o.UserConfig, index, "user_config")
	if p != nil {
		return handlers.SectionOverrideWrite{}, p
	}
	return handlers.SectionOverrideWrite{
		ID:              str(o.ID),
		SectionID:       str(o.SectionID),
		Position:        o.Position,
		Hidden:          flag(o.Hidden),
		Removed:         flag(o.Removed),
		SectionType:     str(o.SectionType),
		Title:           str(o.Title),
		Featured:        o.Featured,
		ItemLimit:       o.ItemLimit,
		Config:          config,
		IsUserAdded:     flag(o.IsUserAdded),
		UserSectionType: str(o.UserSectionType),
		UserConfig:      userConfig,
		UserTitle:       str(o.UserTitle),
	}, nil
}

// rawConfigOf re-encodes a config document for the v1 member, where an
// absent one is empty and {} is stored as sent, so v1's recipe gate
// validates an explicitly empty user_config instead of falling back to the
// legacy config.
func rawConfigOf(c SectionConfig, index int, member string) (json.RawMessage, *Problem) {
	if c == nil {
		return nil, nil
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationOverrides + "[" + strconv.Itoa(index) + "]." + member, Code: codeInvalid, Detail: "expected a JSON object"})
	}
	return raw, nil
}

// sectionConfigOf decodes a stored config document. The store holds what a
// client sent through the recipe gate, so a document that is not an object
// is a server defect, never a client one. An empty string is absent (nil);
// a stored {} decodes to an empty non-nil map and stays present on the wire.
func sectionConfigOf(stored string) (SectionConfig, *Problem) {
	if stored == "" {
		return nil, nil
	}
	var config SectionConfig
	if err := json.Unmarshal([]byte(stored), &config); err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return config, nil
}

// storeNullableInstant is storeInstant for a timestamp the store may not
// have recorded.
func storeNullableInstant(raw string) (NullableInstant, *Problem) {
	if raw == "" {
		return NullableInstant{}, nil
	}
	t, p := storeInstant(raw)
	if p != nil {
		return NullableInstant{}, p
	}
	return NullableInstant{Valid: true, Time: t}, nil
}

func sectionOverrideOf(row userstore.SectionOverride) (SectionOverride, *Problem) {
	config, p := sectionConfigOf(row.Config)
	if p != nil {
		return SectionOverride{}, p
	}
	userConfig, p := sectionConfigOf(row.UserConfig)
	if p != nil {
		return SectionOverride{}, p
	}
	created, p := storeNullableInstant(row.CreatedAt)
	if p != nil {
		return SectionOverride{}, p
	}
	updated, p := storeNullableInstant(row.UpdatedAt)
	if p != nil {
		return SectionOverride{}, p
	}
	return SectionOverride{
		ID:              row.ID,
		SectionID:       row.SectionID,
		Position:        row.Position,
		Hidden:          row.Hidden,
		Removed:         row.Removed,
		SectionType:     row.SectionType,
		Title:           row.Title,
		Featured:        row.Featured,
		ItemLimit:       row.ItemLimit,
		Config:          config,
		IsUserAdded:     row.IsUserAdded,
		UserSectionType: row.UserSectionType,
		UserConfig:      userConfig,
		UserTitle:       row.UserTitle,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}, nil
}
