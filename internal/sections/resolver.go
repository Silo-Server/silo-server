package sections

import (
	"encoding/json"
	"sort"
)

const jsonNullLiteral = "null"

// Resolve merges admin sections with profile overrides, producing the final ordered list.
func Resolve(admin []*PageSection, overrides []ProfileSectionOverride) []ResolvedSection {
	overrideBySection := make(map[string]*ProfileSectionOverride)
	var userAdded []ProfileSectionOverride
	for i := range overrides {
		o := &overrides[i]
		if o.SectionID != "" {
			overrideBySection[o.SectionID] = o
		} else {
			userAdded = append(userAdded, *o)
		}
	}

	var result []ResolvedSection

	for _, s := range admin {
		o, hasOverride := overrideBySection[s.ID]

		if hasOverride && o.Removed {
			continue
		}
		if hasOverride && o.Hidden {
			continue
		}

		rs := ResolvedSection{
			ID:          s.ID,
			SectionType: s.SectionType,
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			Config:      s.Config,
			Position:    s.Position,
		}

		if hasOverride {
			rs.Customized = true
			if o.Position != nil {
				rs.Position = *o.Position
			}
			if o.Title != "" {
				rs.Title = o.Title
			}
			if o.Featured != nil {
				rs.Featured = *o.Featured
			}
			if o.ItemLimit != nil {
				rs.ItemLimit = *o.ItemLimit
			}
			if len(o.Config) > 0 && string(o.Config) != "" && string(o.Config) != jsonNullLiteral {
				rs.Config = o.Config
			}
		}

		result = append(result, rs)
	}

	for _, o := range userAdded {
		if o.Removed || o.Hidden {
			continue
		}
		result = append(result, resolveUserAdded(o))
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Position < result[j].Position
	})

	return result
}

// ResolveForSettings is like Resolve but includes hidden sections with Hidden=true.
// Used by the settings UI so users can toggle visibility.
func ResolveForSettings(admin []*PageSection, overrides []ProfileSectionOverride) []ResolvedSection {
	overrideBySection := make(map[string]*ProfileSectionOverride)
	var userAdded []ProfileSectionOverride
	for i := range overrides {
		o := &overrides[i]
		if o.SectionID != "" {
			overrideBySection[o.SectionID] = o
		} else {
			userAdded = append(userAdded, *o)
		}
	}

	var result []ResolvedSection

	for _, s := range admin {
		o, hasOverride := overrideBySection[s.ID]

		if hasOverride && o.Removed {
			continue
		}
		rs := ResolvedSection{
			ID:          s.ID,
			SectionType: s.SectionType,
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			Config:      s.Config,
			Position:    s.Position,
		}

		if hasOverride {
			rs.Customized = true
			rs.Hidden = o.Hidden
			if o.Position != nil {
				rs.Position = *o.Position
			}
			if o.Title != "" {
				rs.Title = o.Title
			}
			if o.Featured != nil {
				rs.Featured = *o.Featured
			}
			if o.ItemLimit != nil {
				rs.ItemLimit = *o.ItemLimit
			}
			if len(o.Config) > 0 && string(o.Config) != "" && string(o.Config) != jsonNullLiteral {
				rs.Config = o.Config
			}
		}

		result = append(result, rs)
	}

	for _, o := range userAdded {
		if o.Removed {
			continue
		}
		result = append(result, resolveUserAdded(o))
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Position < result[j].Position
	})

	return result
}

// resolveUserAdded converts a user-added ProfileSectionOverride into a
// ResolvedSection. When IsUserAdded is true the explicit User* fields take
// precedence; the legacy SectionType / Title / Config fields are used as a
// fallback for backward compatibility with existing data.
func resolveUserAdded(o ProfileSectionOverride) ResolvedSection {
	pos := 0
	if o.Position != nil {
		pos = *o.Position
	}
	limit := 20
	if o.ItemLimit != nil {
		limit = *o.ItemLimit
	}
	featured := false
	if o.Featured != nil {
		featured = *o.Featured
	}

	// Prefer the explicit user-added fields when present; fall back to the
	// legacy fields for backward compatibility with existing data.
	sectionType := o.SectionType
	if o.UserSectionType != "" {
		sectionType = o.UserSectionType
	}
	title := o.Title
	if o.UserTitle != "" {
		title = o.UserTitle
	}
	cfg := json.RawMessage(`{}`)
	if len(o.UserConfig) > 0 {
		cfg = o.UserConfig
	} else if len(o.Config) > 0 {
		cfg = o.Config
	}

	return ResolvedSection{
		ID:          o.ID,
		SectionType: sectionType,
		Title:       title,
		Featured:    featured,
		ItemLimit:   limit,
		Config:      cfg,
		Position:    pos,
		IsCustom:    true,
		Hidden:      o.Hidden,
	}
}
