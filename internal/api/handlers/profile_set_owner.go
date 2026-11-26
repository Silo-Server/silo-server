package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The kinds of an account's edit records, as the administrator reads them
// (ListProfileSetEdits).
const (
	// ProfileSetEditOverride is a field the owner set on this server, which a
	// set leaves alone until it is cleared.
	ProfileSetEditOverride = "override"
	// ProfileSetEditHidden is a profile of the set the owner removed on this
	// server, which a set does not bring back until it is cleared.
	ProfileSetEditHidden = "hidden"
	// ProfileSetEditUnmerged is the person's own edit on this server (a field,
	// or with no field a removed profile) that their app has not merged yet.
	ProfileSetEditUnmerged = "unmerged"
	// ProfileSetEditSettled is the person's edit of a field the owner had
	// set, kept after their app merged it as the record that the owner's
	// value no longer applies.
	ProfileSetEditSettled = "settled"
)

// ProfileSetEditActor is the account that made an edit: the administrator,
// or the person. Username is "" once that account no longer exists.
type ProfileSetEditActor struct {
	UserID   int
	Username string
}

// ProfileSetEditView is one edit record of an account's profiles from a set.
type ProfileSetEditView struct {
	Kind   string
	SyncID string
	// ProfileID and ProfileName are the profile's; a removed profile has no
	// ID and the name it had.
	ProfileID   string
	ProfileName string
	// Field is an item field or settings.<key>; "" for a removal.
	Field string
	By    string
	Actor ProfileSetEditActor
	At    time.Time
	// Value is the profile's value in force, as a set item encodes it: for a
	// setting, what the profile resolves to, inheriting included (Inherited
	// says the profile has no value of its own). nil for a removal and for a
	// picture whose upload this server kept no hash for.
	Value     json.RawMessage
	Inherited bool
	// SetValue is the set's value of the field, as its last accepted set
	// sent it; nil when the server holds none. SetResets marks a setting the
	// set resets, so the profile inherits.
	SetValue  json.RawMessage
	SetResets bool
	// ClearApplies, on an override, is whether clearing it puts the set's
	// value back at once. A PIN the set sent as digits, or kept, waits for
	// the person's app to send a newer set; so does a picture this server
	// doesn't hold and a field the set never carried.
	ClearApplies bool
	// ReplacedOwner, on an unmerged edit, marks one that replaced a value
	// the owner had set.
	ReplacedOwner bool
}

// ProfileSetClearCommand addresses one edit record of an account's profiles:
// a field of a profile (ProfileID and Field), or a removed profile (SyncID).
type ProfileSetClearCommand struct {
	UserID    int
	ProfileID string
	Field     string
	SyncID    string
}

// ProfileSetClearResult is what clearing an owner's record did.
type ProfileSetClearResult struct {
	// Applied is whether the set's value was written now.
	Applied bool
	// WakeQueued is whether the person's apps were told (profileedits.Waker).
	WakeQueued bool
}

// errProfileSetEditNotFound is a record that does not exist.
func errProfileSetEditNotFound() error {
	return apiError(http.StatusNotFound, "not_found", "No such edit on this profile")
}

// ListProfileSetEdits lists the edit records of an account's profiles from a
// set, for an administrator: the owner's overrides and hidden profiles, the
// person's edits their app has not merged, and the settled records of the
// person's edits over the owner's. A store that does not keep sets lists
// none.
func (h *ProfileHandler) ListProfileSetEdits(ctx context.Context, userID int) ([]ProfileSetEditView, error) {
	store, setStore, err := h.profileSetStore(ctx, userID)
	if errors.Is(err, userstore.ErrProfileSetUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snap, err := setStore.ReadProfileSet(ctx)
	if err != nil {
		return nil, err
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return nil, err
	}
	bySyncID := map[string]*userstore.ProfileSetRow{}
	for i := range snap.Rows {
		if snap.Rows[i].SyncID != "" {
			bySyncID[snap.Rows[i].SyncID] = &snap.Rows[i]
		}
	}
	actors := map[int]string{}
	var out []ProfileSetEditView
	for _, e := range snap.Edits {
		view := ProfileSetEditView{SyncID: e.SyncID, Field: e.Field, By: e.By, At: e.At, ReplacedOwner: e.ReplacedOwner && e.Open()}
		row := bySyncID[e.SyncID]
		switch {
		case e.Hidden():
			view.Kind = ProfileSetEditHidden
		case e.Field == profileedits.FieldRemoval && e.Open():
			view.Kind = ProfileSetEditUnmerged
		case e.Field == profileedits.FieldRemoval || row == nil:
			// A removal a set has settled, or a field of a profile that is
			// gone: nothing left to show.
			continue
		case e.Override():
			view.Kind = ProfileSetEditOverride
		case e.Open():
			view.Kind = ProfileSetEditUnmerged
		default:
			view.Kind = ProfileSetEditSettled
		}
		if row == nil {
			view.ProfileName = e.ProfileName
		} else {
			view.ProfileID, view.ProfileName = row.ID, row.Name
			if err := h.describeEditValues(ctx, store, contract, row, snap.Settings[row.ID], &view); err != nil {
				return nil, err
			}
		}
		view.Actor = ProfileSetEditActor{UserID: e.ActorUserID, Username: h.actorUsername(ctx, actors, e.ActorUserID)}
		out = append(out, view)
	}
	return out, nil
}

// actorUsername is the username of account id, "" once it is gone or
// can't be read; seen caches the answers of one listing.
func (h *ProfileHandler) actorUsername(ctx context.Context, seen map[int]string, id int) string {
	if id <= 0 || h.UserRepo == nil {
		return ""
	}
	if name, ok := seen[id]; ok {
		return name
	}
	name := ""
	if user, err := h.UserRepo.GetByID(ctx, id); err == nil && user != nil {
		name = user.Username
	}
	seen[id] = name
	return name
}

// describeEditValues fills a field record's value in force and the set's
// value.
func (h *ProfileHandler) describeEditValues(ctx context.Context, store userstore.UserStore, contract *settingscontract.Manifest,
	row *userstore.ProfileSetRow, settings map[string]json.RawMessage, view *ProfileSetEditView,
) error {
	if key, isSetting := strings.CutPrefix(view.Field, profileedits.FieldSettingsPrefix); isSetting {
		value, inherited, err := h.settingInForce(ctx, store, contract, row.ID, settings, key)
		if err != nil {
			return err
		}
		view.Value, view.Inherited = value, inherited
	} else {
		item := heldItemStored(contract, row, settings)
		view.Value = overrideValue(&item, view.Field)
	}
	record, ok, err := restoreRecord(contract, row)
	if err != nil || !ok {
		return err
	}
	view.SetValue, view.SetResets = record.value(view.Field)
	if view.Kind == ProfileSetEditOverride {
		view.ClearApplies = record.applies(&row.Profile, view.Field)
	}
	return nil
}

// settingInForce is the profile's value of key: its own, or (inherited)
// the value it resolves to without one.
func (h *ProfileHandler) settingInForce(ctx context.Context, store userstore.UserStore, contract *settingscontract.Manifest,
	profileID string, settings map[string]json.RawMessage, key string,
) (json.RawMessage, bool, error) {
	if own, has := settings[key]; has {
		return own, false, nil
	}
	resolved, err := h.resolveProfileSetting(ctx, store, contract, profileID, key)
	return resolved, true, err
}

// resolveProfileSetting is the value profileID inherits for key, as the
// settings read routes resolve it: the account's value where the key
// resolves through the account, else the administrator's default, else the
// contract's.
func (h *ProfileHandler) resolveProfileSetting(ctx context.Context, store userstore.UserStore, contract *settingscontract.Manifest, profileID, key string) (json.RawMessage, error) {
	resolver := settingsresolve.New(contract)
	if h.AdminDefaults != nil {
		resolver = resolver.WithAdminDefaults(h.AdminDefaults)
	}
	values, err := resolver.Resolve(ctx, store, settingsresolve.Context{ProfileID: profileID}, []string{key}, nil)
	if err != nil || len(values) == 0 {
		return nil, err
	}
	return values[0].Value, nil
}

// restoredRecord is what a clear puts back on a profile: the record of
// what its last accepted set asked of it, as the set item it rebuilds, and
// the settings plan that item makes, mirror halves included. The list's
// set_value, set_resets and clear_applies, and the clear's write, all read
// it, so they can't disagree.
type restoredRecord struct {
	applied  profileSetApplied
	item     profileSetItem
	settings profileSetSettings
}

// decodeApplied reads a profile's applied record. A profile with none, or
// one this server can't read, has nothing for a clear to restore.
func decodeApplied(raw json.RawMessage, into *profileSetApplied) bool {
	return len(raw) > 0 && json.Unmarshal(raw, into) == nil
}

// restoreRecord rebuilds the record of row's last accepted set; ok is false
// when the profile has none.
func restoreRecord(contract *settingscontract.Manifest, row *userstore.ProfileSetRow) (restoredRecord, bool, error) {
	var out restoredRecord
	if !decodeApplied(row.SyncApplied, &out.applied) {
		return out, false, nil
	}
	a := out.applied
	out.item = profileSetItem{
		SyncID: row.SyncID, Name: a.Name, Picture: a.Picture, PIN: profileSetPIN{kind: a.PIN},
		IsChild: a.IsChild, MaxContentRating: a.MaxContentRating, RequireAdvisoryAge: a.RequireAdvisoryAge,
		RequestsBlocked: a.RequestsBlocked, Settings: a.Settings, SettingsReset: a.SettingsReset,
	}
	if a.MaxAdvisoryAge != nil {
		out.item.MaxAdvisoryAge = *a.MaxAdvisoryAge
	}
	var err error
	out.settings, err = planProfileSetSettings(contract, out.item)
	return out, true, err
}

// value is the set's value of field, encoded as a set item encodes it;
// resets marks a setting the set resets. For a setting it is what the clear
// writes, the half of a mirrored pair the set didn't send included. nil
// when the set asks nothing of it.
func (r restoredRecord) value(field string) (value json.RawMessage, resets bool) {
	applied := r.applied
	var v any
	switch field {
	case profileedits.FieldName:
		v = applied.Name
	case profileedits.FieldPicture:
		v = applied.Picture
	case profileedits.FieldPIN:
		v = applied.PIN
	case profileedits.FieldIsChild:
		v = applied.IsChild
	case profileedits.FieldMaxContentRating:
		v = applied.MaxContentRating
	case profileedits.FieldMaxAdvisoryAge:
		v = applied.MaxAdvisoryAge
	case profileedits.FieldRequireAdvisoryAge:
		v = applied.RequireAdvisoryAge
	case profileedits.FieldRequestsBlocked:
		v = applied.RequestsBlocked
	default:
		key, _ := strings.CutPrefix(field, profileedits.FieldSettingsPrefix)
		if slices.Contains(r.settings.reset, key) {
			return nil, true
		}
		return r.settings.write[key], false
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return encoded, false
}

// applies reports whether clearing the owner's field writes the set's
// value now: the set asks something of it (for a setting, either half of a
// mirrored pair it sent counts), and it is not a PIN the set sent as digits
// or kept (this server never stores digits), nor a picture it can't show.
func (r restoredRecord) applies(row *userstore.Profile, field string) bool {
	switch field {
	case profileedits.FieldPIN:
		return r.applied.PIN == profileSetPINClear
	case profileedits.FieldPicture:
		_, _, available := heldPicture(row, r.applied.Picture)
		return available
	}
	if key, isSetting := strings.CutPrefix(field, profileedits.FieldSettingsPrefix); isSetting {
		_, written := r.settings.write[key]
		return written || slices.Contains(r.settings.reset, key)
	}
	return true
}

// clearedFields is the owner's rows a clear of field removes: the field,
// and for a setting the contract mirrors in another key, that key's row
// too. A set writes the pair together or not at all, so clearing one half
// clears both.
func clearedFields(edits profileSetEdits, syncID, field string) []string {
	fields := []string{field}
	key, isSetting := strings.CutPrefix(field, profileedits.FieldSettingsPrefix)
	if !isSetting {
		return fields
	}
	if mirror, ok := settingscontract.MirrorKey(key); ok && edits.overridden(syncID, profileedits.FieldSettingsPrefix+mirror) {
		fields = append(fields, profileedits.FieldSettingsPrefix+mirror)
	}
	return fields
}

// ClearProfileSetOverride clears an owner's override of one field and puts
// the set's value back at once, from the record of what the account's last
// accepted set asked of the profile, with the set's own per-field apply and
// in a transaction holding the set's locks. Nothing it writes is recorded as
// an edit. A PIN the set sent as digits can't be put back (the server never
// stores them): the PIN stays until the person's app sends a newer set. Then
// the person's apps are woken.
func (h *ProfileHandler) ClearProfileSetOverride(ctx context.Context, cmd ProfileSetClearCommand) (ProfileSetClearResult, error) {
	var out ProfileSetClearResult
	run, err := h.ownerClearRun(ctx, cmd.UserID)
	if err != nil {
		return out, err
	}
	ctx = profileedits.WithoutRecording(ctx)
	err = run.inSetTransaction(ctx, func(w userstore.ProfileSetWriter, snap userstore.ProfileSetSnapshot) error {
		applied, err := run.clearOverride(ctx, w, snap, cmd)
		out.Applied = applied
		return err
	})
	if err != nil {
		return ProfileSetClearResult{}, err
	}
	run.afterCommit(ctx)
	out.WakeQueued = h.wakeAfterClear(ctx, cmd.UserID)
	return out, nil
}

// wakeAfterClear tells the account's apps about a clear that has committed,
// and reports whether a wake was queued. The clear stands whatever the waker
// does: a waker that panics answers false, logged with the account and the
// panic's type only, since its value may carry anything.
func (h *ProfileHandler) wakeAfterClear(ctx context.Context, userID int) (queued bool) {
	defer func() {
		if r := recover(); r != nil {
			queued = false
			slog.ErrorContext(ctx, "profile set wake after a clear panicked", "component", "api", "user_id", userID,
				"panic_type", fmt.Sprintf("%T", r))
		}
	}()
	return h.ProfileSetWaker.Wake(ctx, userID)
}

// ClearProfileSetHidden clears an owner's removal of a profile of the set.
// It does not bring the profile back: the next accepted newer set does.
// Then the person's apps are woken.
func (h *ProfileHandler) ClearProfileSetHidden(ctx context.Context, cmd ProfileSetClearCommand) (ProfileSetClearResult, error) {
	run, err := h.ownerClearRun(ctx, cmd.UserID)
	if err != nil {
		return ProfileSetClearResult{}, err
	}
	err = run.inSetTransaction(ctx, func(w userstore.ProfileSetWriter, _ userstore.ProfileSetSnapshot) error {
		n, err := w.ClearOwnerEdits(ctx, cmd.SyncID, []string{profileedits.FieldRemoval})
		if err != nil {
			return err
		}
		if n == 0 {
			return errProfileSetEditNotFound()
		}
		return run.auditOwnerClear(ctx, w, auditmutation.ActionProfileHiddenCleared, cmd.SyncID, auditmutation.Removed("sync_id", cmd.SyncID))
	})
	if err != nil {
		return ProfileSetClearResult{}, err
	}
	run.afterCommit(ctx)
	return ProfileSetClearResult{WakeQueued: h.wakeAfterClear(ctx, cmd.UserID)}, nil
}

// DismissProfileSetEdit deletes the settled record of the person's edit of
// a field the owner had set. The person's apps don't read it, so none is
// woken.
func (h *ProfileHandler) DismissProfileSetEdit(ctx context.Context, cmd ProfileSetClearCommand) error {
	run, err := h.ownerClearRun(ctx, cmd.UserID)
	if err != nil {
		return err
	}
	err = run.inSetTransaction(ctx, func(w userstore.ProfileSetWriter, snap userstore.ProfileSetSnapshot) error {
		row := profileSetRowByID(snap.Rows, cmd.ProfileID)
		if row == nil || row.SyncID == "" {
			return errProfileSetEditNotFound()
		}
		ok, err := w.DismissSettledEdit(ctx, row.SyncID, cmd.Field)
		if err != nil {
			return err
		}
		if !ok {
			return errProfileSetEditNotFound()
		}
		return run.auditOwnerClear(ctx, w, auditmutation.ActionProfileEditDismissed, row.ID, auditmutation.Removed("settled_edit", cmd.Field))
	})
	if err != nil {
		return err
	}
	run.afterCommit(ctx)
	return nil
}

// ownerClearRun checks the Owner rules for an administrator's change to
// userID's profiles and opens the run a clear works in.
func (h *ProfileHandler) ownerClearRun(ctx context.Context, userID int) (*profileSetRun, error) {
	if err := h.checkAccountAdmin(ctx, true, userID); err != nil {
		return nil, err
	}
	store, setStore, err := h.profileSetStore(ctx, userID)
	if err != nil {
		return nil, err
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return nil, err
	}
	return &profileSetRun{h: h, cmd: ProfileSetCommand{UserID: userID}, store: store, setStore: setStore, contract: contract}, nil
}

// inSetTransaction runs fn in the profile set's transaction, with the set's
// locks in the set's order and every profile of the set named, so it
// removes none. It starts over when a profile joined the set in between.
func (r *profileSetRun) inSetTransaction(ctx context.Context, fn func(userstore.ProfileSetWriter, userstore.ProfileSetSnapshot) error) error {
	for range profileSetAttempts {
		snap, err := r.setStore.ReadProfileSet(ctx)
		if err != nil {
			return err
		}
		inSet := []string{}
		for _, row := range snap.Rows {
			if row.SyncID != "" {
				inSet = append(inSet, row.SyncID)
			}
		}
		// What an attempt that rolled back left to do goes with it.
		r.audits, r.changed, r.signals, r.pinResets, r.avatars = nil, nil, nil, nil, nil
		err = r.setStore.WithProfileSetTransaction(ctx, inSet, func(w userstore.ProfileSetWriter) error {
			if len(w.Removable()) > 0 {
				return errProfileSetRetry
			}
			locked, err := w.Snapshot(ctx)
			if err != nil {
				return err
			}
			return fn(w, locked)
		})
		if !profileSetRetryable(err) {
			return err
		}
	}
	return apiError(http.StatusConflict, "conflict", "The profiles changed while the change was made; try again")
}

// clearOverride is ClearProfileSetOverride's transaction body. It reports
// whether the set's value was written.
func (r *profileSetRun) clearOverride(ctx context.Context, w userstore.ProfileSetWriter, snap userstore.ProfileSetSnapshot, cmd ProfileSetClearCommand) (bool, error) {
	row := profileSetRowByID(snap.Rows, cmd.ProfileID)
	if row == nil || row.SyncID == "" {
		return false, errProfileSetEditNotFound()
	}
	edits := newProfileSetEdits(snap.Edits)
	if !edits.overridden(row.SyncID, cmd.Field) {
		return false, errProfileSetEditNotFound()
	}
	fields := clearedFields(edits, row.SyncID, cmd.Field)
	applied := false
	record, ok, err := restoreRecord(r.contract, row)
	if err != nil {
		return false, err
	}
	if ok {
		applied = record.applies(&row.Profile, cmd.Field)
		if err := checkRestoredName(snap.Rows, row, record.applied, fields); err != nil {
			return false, err
		}
		if err := r.applyRecordFields(ctx, w, row, record, edits, fields); err != nil {
			return false, err
		}
	}
	n, err := w.ClearOwnerEdits(ctx, row.SyncID, fields)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, errProfileSetEditNotFound()
	}
	return applied, r.auditOwnerClear(ctx, w, auditmutation.ActionProfileOverrideCleared, row.ID, auditmutation.Removed("override", cmd.Field))
}

// applyRecordFields writes the set's value of fields to the profile, from
// the record of what its last accepted set asked, with the set's own apply
// (updateProfile) limited to those fields.
func (r *profileSetRun) applyRecordFields(ctx context.Context, w userstore.ProfileSetWriter, row *userstore.ProfileSetRow,
	record restoredRecord, edits profileSetEdits, fields []string,
) error {
	r.doc = &profileSetDoc{Items: []profileSetItem{record.item}}
	r.settings = []profileSetSettings{record.settings}
	r.edits = itemEdits{profileSetEdits: edits, ids: []string{row.SyncID}}
	r.only = map[string]bool{}
	for _, field := range fields {
		r.only[field] = true
	}
	r.moves, r.newPINs = map[string]string{}, map[int]string{}
	audited, recording := profileAuditWriter(ctx, w)
	return r.updateProfile(ctx, w, audited, recording, 0, row)
}

// auditOwnerClear records an administrator's clear as a domain action when
// the request records them.
func (r *profileSetRun) auditOwnerClear(ctx context.Context, w userstore.ProfileSetWriter, action, target string, changes []auditmutation.Change) error {
	audited, recording := profileAuditWriter(ctx, w)
	if !recording {
		return nil
	}
	entry, err := auditmutation.RecordMutation(ctx, audited.AuditTx(), action, auditmutation.TargetProfile, target, http.StatusOK, changes)
	if err != nil {
		return err
	}
	r.audits = append(r.audits, entry)
	return nil
}

// checkRestoredName refuses, before anything is written, a clear that would
// give the profile back the set's name while another profile on the account
// has it, compared as every other profile write compares names. The answer
// is the one an administrator's rename to that name gets.
func checkRestoredName(rows []userstore.ProfileSetRow, row *userstore.ProfileSetRow, record profileSetApplied, fields []string) error {
	if !slices.Contains(fields, profileedits.FieldName) || sameProfileName(row.Name, record.Name) {
		return nil
	}
	profiles := make([]userstore.Profile, len(rows))
	for i := range rows {
		profiles[i] = rows[i].Profile
	}
	if profileNameConflicts(profiles, record.Name, row.ID) {
		return apiError(http.StatusConflict, "name_conflict", "A profile with this name already exists")
	}
	return nil
}

func profileSetRowByID(rows []userstore.ProfileSetRow, id string) *userstore.ProfileSetRow {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}
