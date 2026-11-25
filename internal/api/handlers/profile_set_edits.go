package handlers

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// ProfileSetFieldChange is a field edited on this server outside a set.
type ProfileSetFieldChange struct {
	Field string
	At    time.Time
	By    string
}

// ProfileSetDeletion is a profile of the set deleted on this server outside
// a set.
type ProfileSetDeletion struct {
	SyncID string
	At     time.Time
	By     string
}

// ProfileSetOverride is a value the owner set on this server, which a set
// leaves alone. Value is the profile's live value as a set item encodes it;
// nil for a setting the owner reset, which then has no value here.
type ProfileSetOverride struct {
	SyncID string
	Field  string
	Value  json.RawMessage
}

// profileSetEdits is the account's edit rows as a set reads them.
type profileSetEdits struct {
	rows []profileedits.Edit
	// overrides holds each profile's owner-set fields, by sync ID.
	overrides map[string]map[string]bool
	// hidden holds the sync IDs the owner removed on this server.
	hidden map[string]bool
}

func newProfileSetEdits(rows []profileedits.Edit) profileSetEdits {
	e := profileSetEdits{rows: rows, overrides: map[string]map[string]bool{}, hidden: map[string]bool{}}
	for _, row := range rows {
		switch {
		case row.Hidden():
			e.hidden[row.SyncID] = true
		case row.Override():
			if e.overrides[row.SyncID] == nil {
				e.overrides[row.SyncID] = map[string]bool{}
			}
			e.overrides[row.SyncID][row.Field] = true
		}
	}
	return e
}

// overridden reports whether the owner set field on the profile syncID.
func (e profileSetEdits) overridden(syncID, field string) bool {
	return e.overrides[syncID][field]
}

// settingOverridden reports whether the owner set key, or the key it
// mirrors, on the profile syncID: a set writes the pair together or not
// at all.
func (e profileSetEdits) settingOverridden(syncID, key string) bool {
	if e.overridden(syncID, profileedits.FieldSettingsPrefix+key) {
		return true
	}
	mirror, ok := settingscontract.MirrorKey(key)
	return ok && e.overridden(syncID, profileedits.FieldSettingsPrefix+mirror)
}

// pinOverridden lists the items whose profile has its PIN set by the owner.
func (e profileSetEdits) pinOverridden(doc *profileSetDoc) []int {
	var out []int
	for i, item := range doc.Items {
		if e.overridden(item.SyncID, profileedits.FieldPIN) {
			out = append(out, i)
		}
	}
	return out
}

// withoutOverrides is an item's settings plan without the keys the owner
// set on this server. applied, the set's value of every key it sent, stays
// whole.
func (e profileSetEdits) withoutOverrides(syncID string, plan profileSetSettings) profileSetSettings {
	if len(e.overrides[syncID]) == 0 {
		return plan
	}
	out := plan
	out.write = maps.Clone(plan.write)
	maps.DeleteFunc(out.write, func(key string, _ json.RawMessage) bool { return e.settingOverridden(syncID, key) })
	out.reset = slices.DeleteFunc(slices.Clone(plan.reset), func(key string) bool { return e.settingOverridden(syncID, key) })
	return out
}

// changedHere lists the open edits of one profile's fields, by field.
func (e profileSetEdits) changedHere(syncID string) []ProfileSetFieldChange {
	var out []ProfileSetFieldChange
	for _, row := range e.rows {
		if row.SyncID == syncID && row.Field != profileedits.FieldRemoval && row.Open() {
			out = append(out, ProfileSetFieldChange{Field: row.Field, At: row.At, By: row.By})
		}
	}
	return out
}

// deletedHere lists the profiles of the set deleted on this server.
func (e profileSetEdits) deletedHere() []ProfileSetDeletion {
	var out []ProfileSetDeletion
	for _, row := range e.rows {
		if row.Field == profileedits.FieldRemoval && row.Open() {
			out = append(out, ProfileSetDeletion{SyncID: row.SyncID, At: row.At, By: row.By})
		}
	}
	return out
}

// hiddenByOwner lists the sync IDs the owner removed on this server.
func (e profileSetEdits) hiddenByOwner() []string {
	out := slices.Collect(maps.Keys(e.hidden))
	slices.Sort(out)
	return out
}

// overridesOf lists the owner's field rows of the held items, each with the
// item's live value, ordered by sync ID and field.
func (e profileSetEdits) overridesOf(items []ProfileSetHeldItem) []ProfileSetOverride {
	bySyncID := map[string]*ProfileSetHeldItem{}
	for i := range items {
		bySyncID[items[i].SyncID] = &items[i]
	}
	var out []ProfileSetOverride
	for _, row := range e.rows {
		item := bySyncID[row.SyncID]
		if !row.Override() || item == nil {
			continue
		}
		out = append(out, ProfileSetOverride{SyncID: row.SyncID, Field: row.Field, Value: overrideValue(item, row.Field)})
	}
	return out
}

// overrideValue is a held item's value of field, encoded as a set item
// encodes it. A PIN is "set" or "clear", never a hash; a setting with no
// profile value is nil.
func overrideValue(item *ProfileSetHeldItem, field string) json.RawMessage {
	var value any
	switch field {
	case profileedits.FieldName:
		value = item.Name
	case profileedits.FieldPicture:
		value = item.Picture
	case profileedits.FieldPIN:
		value = profileSetPINClear
		if item.HasPIN {
			value = profileSetPINSet
		}
	case profileedits.FieldIsChild:
		value = item.IsChild
	case profileedits.FieldMaxContentRating:
		value = item.MaxContentRating
	case profileedits.FieldMaxAdvisoryAge:
		if item.MaxAdvisoryAge != 0 {
			value = item.MaxAdvisoryAge
		}
	case profileedits.FieldRequireAdvisoryAge:
		value = item.RequireAdvisoryAge
	case profileedits.FieldRequestsBlocked:
		value = item.RequestsBlocked
	default:
		key, isSetting := strings.CutPrefix(field, profileedits.FieldSettingsPrefix)
		if !isSetting {
			return nil
		}
		return item.Settings[key]
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}
