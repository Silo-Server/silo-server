package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/auth/passwordhash"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileSetRun is one ApplyProfileSet: the parsed set and what each
// attempt decided, then what the committed attempt leaves to do.
type profileSetRun struct {
	h        *ProfileHandler
	cmd      ProfileSetCommand
	doc      *profileSetDoc
	store    userstore.UserStore
	setStore userstore.ProfileSetStore
	contract *settingscontract.Manifest
	settings []profileSetSettings
	limit    int

	// Decided before the transaction, on the unlocked read.
	repeat     bool
	pinMatches map[int]bool
	newPINs    map[int]string
	devices    map[string]profileDevicesRead
	// pinOverrides is the items whose PIN the owner set, on that read.
	pinOverrides []int

	// edits is the account's edits on this server as the set's items see
	// them: from the unlocked read, then from the read under the
	// transaction's locks.
	edits itemEdits
	// moves maps a profile's sync ID to the one its item gives it, for each
	// profile whose ID the set changes; its edit rows move with it.
	moves map[string]string

	// Done by the committed transaction.
	noop      bool
	audits    []*auditmutation.Entry
	deleted   []profileSetDeleted
	changed   []profileSetSettingChange
	signals   []string
	pinResets []string
	avatars   []string
}

type profileDevicesRead struct {
	registry userstore.DeviceRegistry
	devices  []string
}

type profileSetDeleted struct {
	profile     userstore.Profile
	collections []string
}

type profileSetSettingChange struct{ profileID, key string }

// profileSetApplied is the record of what the set last wrote to a profile.
type profileSetApplied struct {
	Name               string                     `json:"name"`
	Picture            string                     `json:"picture"`
	PIN                string                     `json:"pin"`
	IsChild            bool                       `json:"is_child"`
	MaxContentRating   string                     `json:"max_content_rating"`
	MaxAdvisoryAge     *int                       `json:"max_advisory_age"`
	RequireAdvisoryAge bool                       `json:"require_advisory_age"`
	RequestsBlocked    bool                       `json:"requests_blocked"`
	Settings           map[string]json.RawMessage `json:"settings"`
	SettingsReset      []string                   `json:"settings_reset"`
	// AwaitingPin marks a profile created from keep, until it has a PIN.
	AwaitingPin bool `json:"awaiting_pin,omitempty"`
}

// attempt plans the set on an unlocked read, does the slow work there (PIN
// hashing, reading devices), then applies it in the set's transaction,
// which plans again under its locks and starts over when the household
// changed in between.
func (r *profileSetRun) attempt(ctx context.Context) error {
	*r = profileSetRun{h: r.h, cmd: r.cmd, doc: r.doc, store: r.store, setStore: r.setStore,
		contract: r.contract, settings: r.settings, limit: r.limit}
	snap, err := r.setStore.ReadProfileSet(ctx)
	if err != nil {
		return err
	}
	var active *userstore.Profile
	if !r.cmd.Signed {
		if active, err = requireSetPrimary(snap.Rows, r.cmd.ActiveProfileID, r.cmd.VerifyProfile); err != nil {
			return err
		}
	}
	edits := newProfileSetEdits(snap.Edits)
	plan, err := planProfileSet(r.doc, snap.Rows, r.limit, edits)
	if err != nil {
		return err
	}
	r.edits = edits.forItems(r.doc, plan)
	r.pinOverrides = r.edits.pinOverridden()
	for i, row := range plan.rowOf {
		// An upload stored before hashes were kept gets its label now, so a
		// set built from this server's own answer matches it.
		if isUploadedAvatarRef(row.Avatar) && row.AvatarSourceSHA256 == "" && r.doc.Items[i].Picture != "" {
			if _, err := r.h.profileSetPicture(ctx, r.store, &row.Profile); err != nil {
				return err
			}
		}
	}
	r.repeat = r.isRepeat(snap.State)
	if r.pinMatches, err = r.comparePINs(ctx, plan, r.repeat); err != nil {
		return err
	}
	if r.noop, err = r.checkVersion(snap.State, plan); err != nil || r.noop {
		return err
	}
	// A refusal before any PIN is hashed; the check under the locks is the
	// one that counts.
	if token := profileSetEditsToken(snap); token != "" && r.doc.EditsSeen != token {
		return ErrProfileSetStale
	}
	if err := r.hashPINs(ctx, plan); err != nil {
		return err
	}
	r.devices = map[string]profileDevicesRead{}
	for _, row := range plan.removed {
		registry, devices := r.h.profileDevices(ctx, r.store, r.cmd.UserID, row.ID)
		r.devices[row.ID] = profileDevicesRead{registry: registry, devices: devices}
	}
	return r.setStore.WithProfileSetTransaction(ctx, r.doc.syncIDs(), func(w userstore.ProfileSetWriter) error {
		return r.apply(ctx, w, plan, active)
	})
}

// comparePINs compares each set PIN with the stored hash of the profile it
// lands on, outside any lock. On a repeat of the held version the answer
// (200 or profile_set_stale) tells whether the PIN matched, so each
// comparison there is a PIN guess: it spends the profile's PIN attempts as
// verifyProfilePIN does, a match clears them as it does there, and a locked
// profile answers that check's 429 without comparing. On a newer version the
// answer never depends on the match, which only decides whether to write.
func (r *profileSetRun) comparePINs(ctx context.Context, plan *profileSetPlan, repeat bool) (map[int]bool, error) {
	matches := map[int]bool{}
	for i := range r.doc.Items {
		row, mapped := plan.rowOf[i]
		item := r.doc.Items[i]
		if !mapped || item.PIN.kind != profileSetPINSet || row.PINHash == "" || r.edits.itemOverridden(i, profileedits.FieldPIN) {
			continue
		}
		attemptKey := ratelimit.ProfilePINKey(r.cmd.UserID, row.ID)
		if repeat {
			if retryAfter, ok := r.h.PINAttempts.Reserve(ctx, attemptKey); !ok {
				return nil, PINLockedError(retryAfter)
			}
		}
		err := passwordhash.Compare(ctx, []byte(row.PINHash), []byte(item.PIN.digits))
		if apiErr := busyError(err); apiErr != nil {
			return nil, apiErr
		}
		matches[i] = err == nil
		if repeat && matches[i] {
			r.h.PINAttempts.Reset(ctx, attemptKey)
		}
	}
	return matches, nil
}

// checkVersion applies the version rule. An older version is stale. The
// held version again is never applied: other content is stale, and so is
// a PIN that differs from the stored one (a set PIN that doesn't match, a
// clear that finds a PIN); otherwise it changes nothing (noop). A PIN
// changes only with a newer version. A PIN the owner set on this server is
// not the set's, so it is not compared.
func (r *profileSetRun) checkVersion(state userstore.ProfileSetState, plan *profileSetPlan) (noop bool, err error) {
	if !state.Held || r.doc.Version > state.Version {
		return false, nil
	}
	if r.doc.Version < state.Version || !sameHash(r.doc.Hash, state.SHA256) {
		return false, ErrProfileSetStale
	}
	for i, row := range plan.rowOf {
		if r.edits.itemOverridden(i, profileedits.FieldPIN) {
			continue
		}
		switch r.doc.Items[i].PIN.kind {
		case profileSetPINSet:
			if !r.pinMatches[i] {
				return false, ErrProfileSetStale
			}
		case profileSetPINClear:
			if row.PINHash != "" {
				return false, ErrProfileSetStale
			}
		}
	}
	return true, nil
}

// isRepeat reports a set that is the held version with the held content.
func (r *profileSetRun) isRepeat(state userstore.ProfileSetState) bool {
	return state.Held && r.doc.Version == state.Version && sameHash(r.doc.Hash, state.SHA256)
}

// hashPINs hashes each set PIN that is to be written: on a new profile, or
// on one whose stored PIN differs.
func (r *profileSetRun) hashPINs(ctx context.Context, plan *profileSetPlan) error {
	r.newPINs = map[int]string{}
	for i, item := range r.doc.Items {
		if item.PIN.kind != profileSetPINSet || r.pinMatches[i] || r.edits.itemOverridden(i, profileedits.FieldPIN) {
			continue
		}
		if _, mapped := plan.rowOf[i]; !mapped && !slices.Contains(plan.create, i) {
			continue
		}
		hash, err := passwordhash.Generate(ctx, []byte(item.PIN.digits), bcrypt.DefaultCost)
		if apiErr := busyError(err); apiErr != nil {
			return apiErr
		}
		if err != nil {
			return fmt.Errorf("hashing a profile PIN: %w", err)
		}
		r.newPINs[i] = string(hash)
	}
	return nil
}

// samePlanBasis reports whether the plan made under the locks rests on the
// same household as the one made before: the same profile for each item,
// with the same PIN and picture label, and the same removals and creations.
func samePlanBasis(before, after *profileSetPlan) bool {
	if len(before.rowOf) != len(after.rowOf) || !slices.Equal(before.create, after.create) || len(before.removed) != len(after.removed) ||
		!slices.Equal(before.hidden, after.hidden) {
		return false
	}
	for i, a := range before.rowOf {
		b := after.rowOf[i]
		if b == nil || a.ID != b.ID || a.PINHash != b.PINHash || a.AvatarSourceSHA256 != b.AvatarSourceSHA256 {
			return false
		}
	}
	for i := range before.removed {
		if before.removed[i].ID != after.removed[i].ID {
			return false
		}
	}
	return true
}

// apply is the set's transaction body.
func (r *profileSetRun) apply(ctx context.Context, w userstore.ProfileSetWriter, pre *profileSetPlan, active *userstore.Profile) error {
	snap, err := w.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !snap.State.Enabled {
		return auth.ErrUserDisabled
	}
	if r.cmd.Signed && snap.State.Role == models.RoleAdmin {
		return auth.ErrProfileSetAdminAccount
	}
	if err := checkHouseholdPINRevision(ctx, w, active); err != nil {
		return err
	}
	edits := newProfileSetEdits(snap.Edits)
	plan, err := planProfileSet(r.doc, snap.Rows, r.limit, edits)
	if err != nil {
		return err
	}
	locked := edits.forItems(r.doc, plan)
	// A set that became a repeat of the held version since the first read
	// starts over, so its PIN comparisons count as guesses (comparePINs), and
	// so does one whose PIN overrides changed, which decide what was hashed.
	if !samePlanBasis(pre, plan) || (r.isRepeat(snap.State) && !r.repeat) || !slices.Equal(r.pinOverrides, locked.pinOverridden()) {
		return errProfileSetRetry
	}
	r.edits, r.moves = locked, map[string]string{}
	if r.noop, err = r.checkVersion(snap.State, plan); err != nil || r.noop {
		return err
	}
	if token := profileSetEditsToken(snap); token != "" && r.doc.EditsSeen != token {
		return ErrProfileSetStale
	}
	removable := w.Removable()
	if len(removable) != len(plan.removed) {
		return fmt.Errorf("the set's transaction locked %d profiles for removal, the plan removes %d", len(removable), len(plan.removed))
	}
	for i, row := range plan.removed {
		if removable[i] != row.ID {
			return fmt.Errorf("the set's transaction locked profile %s for removal, the plan removes %s", removable[i], row.ID)
		}
	}
	audited, recording := profileAuditWriter(ctx, w)
	for _, row := range plan.removed {
		if err := r.removeProfile(ctx, w, audited, recording, row); err != nil {
			return err
		}
	}
	for i := range r.doc.Items {
		if row, mapped := plan.rowOf[i]; mapped {
			if err := r.updateProfile(ctx, w, audited, recording, i, row); err != nil {
				return err
			}
		}
	}
	for n, i := range plan.create {
		// Only the first profile created in an empty household is its
		// primary; the ones after it are created like any other.
		if err := r.createProfile(ctx, w, audited, recording, i, len(snap.Rows) == 0 && n == 0); err != nil {
			return err
		}
	}
	if err := w.Accept(ctx, r.doc.Version, r.doc.Hash); err != nil {
		return err
	}
	// Last, as in every transaction that writes them: the edit rows. The
	// marks the token covered are settled, and a removed profile's go.
	removed := make([]string, len(plan.removed))
	for i, row := range plan.removed {
		removed[i] = row.SyncID
	}
	if err := w.SettleEdits(ctx, snap.Edits, removed); err != nil {
		return err
	}
	// A profile whose sync ID the set changed keeps its edits, the owner's
	// overrides included, under its new ID.
	for _, from := range slices.Sorted(maps.Keys(r.moves)) {
		if err := w.MoveEdits(ctx, from, r.moves[from]); err != nil {
			return err
		}
	}
	return nil
}

func (r *profileSetRun) removeProfile(ctx context.Context, w userstore.ProfileSetWriter, audited userstore.AuditedProfileWriter, recording bool, row *userstore.ProfileSetRow) error {
	var before *userstore.Profile
	if recording {
		var err error
		if before, err = audited.GetProfile(ctx, row.ID); err != nil {
			return err
		}
	}
	collections, err := w.DeleteProfile(ctx, row.ID)
	if err != nil {
		return err
	}
	if recording {
		entry, err := recordProfileMutation(ctx, audited.AuditTx(), before, nil)
		if err != nil {
			return err
		}
		r.audits = append(r.audits, entry)
	}
	r.deleted = append(r.deleted, profileSetDeleted{profile: row.Profile, collections: collections})
	return nil
}

// updateProfile makes a profile equal its item, writing only what differs
// from what is stored now.
// A field the owner set on this server is left as it is.
func (r *profileSetRun) updateProfile(ctx context.Context, w userstore.ProfileSetWriter, audited userstore.AuditedProfileWriter, recording bool, i int, row *userstore.ProfileSetRow) error {
	item := r.doc.Items[i]
	owner := func(field string) bool { return r.edits.itemOverridden(i, field) }
	if row.SyncID != "" && row.SyncID != item.SyncID {
		r.moves[row.SyncID] = item.SyncID
	}
	var input userstore.UpdateProfileInput
	changed, access := false, false
	if row.Name != item.Name && !owner(profileedits.FieldName) {
		input.Name, changed = &item.Name, true
	}
	if target, held, available := heldPicture(&row.Profile, item.Picture); available && !held && !owner(profileedits.FieldPicture) {
		input.Avatar, changed = &target, true
		if avatarRefReplacesUpload(row.Avatar, target) {
			r.avatars = append(r.avatars, row.ID)
		}
	}
	setBool := func(name string, current, want bool, field **bool) {
		if current != want && !owner(name) {
			*field, changed, access = &want, true, true
		}
	}
	setBool(profileedits.FieldIsChild, row.IsChild, item.IsChild, &input.IsChild)
	setBool(profileedits.FieldRequireAdvisoryAge, row.RequireAdvisoryAge, item.RequireAdvisoryAge, &input.RequireAdvisoryAge)
	setBool(profileedits.FieldRequestsBlocked, row.RequestsBlocked, item.RequestsBlocked, &input.RequestsBlocked)
	if row.MaxContentRating != item.MaxContentRating && !owner(profileedits.FieldMaxContentRating) {
		input.MaxContentRating, changed, access = &item.MaxContentRating, true, true
	}
	if row.MaxAdvisoryAge != item.MaxAdvisoryAge && !owner(profileedits.FieldMaxAdvisoryAge) {
		input.MaxAdvisoryAge, changed, access = &item.MaxAdvisoryAge, true, true
	}
	finalPIN := row.PINHash
	pinKind := item.PIN.kind
	if owner(profileedits.FieldPIN) {
		pinKind = profileSetPINKeep
	}
	switch pinKind {
	case profileSetPINClear:
		if row.PINHash != "" {
			finalPIN = ""
		}
	case profileSetPINSet:
		if hash, ok := r.newPINs[i]; ok {
			finalPIN = hash
		}
	}
	if finalPIN != row.PINHash {
		input.PINHash, changed, access = &finalPIN, true, true
		r.pinResets = append(r.pinResets, row.ID)
	}
	before := input
	applyColumnPreferences(r.contract, r.edits.withoutOverrides(r.edits.ids[i], r.settings[i]), &row.Profile, &input)
	changed = changed || input != before
	if changed {
		if err := r.writeProfile(ctx, w, audited, recording, row, input); err != nil {
			return err
		}
	}
	if access {
		r.signals = append(r.signals, row.ID)
	}
	awaiting := profileSetAwaitingPin(row.SyncApplied) && finalPIN == ""
	return r.finishProfile(ctx, w, i, row.ID, awaiting)
}

func (r *profileSetRun) writeProfile(ctx context.Context, w userstore.ProfileSetWriter, audited userstore.AuditedProfileWriter, recording bool, row *userstore.ProfileSetRow, input userstore.UpdateProfileInput) error {
	var before *userstore.Profile
	if recording {
		var err error
		if before, err = audited.GetProfile(ctx, row.ID); err != nil {
			return err
		}
	}
	if err := w.UpdateProfile(ctx, row.ID, input); err != nil {
		return err
	}
	if !recording {
		return nil
	}
	after, err := audited.GetProfile(ctx, row.ID)
	if err != nil || after == nil {
		return err
	}
	entry, err := recordProfileMutation(ctx, audited.AuditTx(), before, after)
	if err != nil {
		return err
	}
	r.audits = append(r.audits, entry)
	return nil
}

// createProfile creates a profile from an item. A child starts limited to
// no library until the person picks them on this server; the first profile
// of an empty household is its primary and has no library limit.
func (r *profileSetRun) createProfile(ctx context.Context, w userstore.ProfileSetWriter, audited userstore.AuditedProfileWriter, recording bool, i int, primary bool) error {
	item := r.doc.Items[i]
	profile := userstore.Profile{
		ID: uuid.New().String(), Name: item.Name, PINHash: r.newPINs[i], IsChild: item.IsChild,
		MaxContentRating: item.MaxContentRating, MaxAdvisoryAge: item.MaxAdvisoryAge,
		RequireAdvisoryAge: item.RequireAdvisoryAge, RequestsBlocked: item.RequestsBlocked,
		ShowForcedSubtitles:        true,
		LibraryRestrictionsEnabled: item.IsChild && !primary,
	}
	if target, _, available := heldPicture(&userstore.Profile{}, item.Picture); available {
		profile.Avatar = target
	}
	if value, ok := r.settings[i].write[settingskeys.PlaybackShowForcedSubtitles]; ok {
		_ = json.Unmarshal(value, &profile.ShowForcedSubtitles)
	}
	var columns userstore.UpdateProfileInput
	applyColumnPreferences(r.contract, r.settings[i], &profile, &columns)
	for _, pair := range []struct {
		value *bool
		field *bool
	}{
		{columns.AutoSkipIntro, &profile.AutoSkipIntro}, {columns.AutoSkipCredits, &profile.AutoSkipCredits},
		{columns.AutoSkipRecap, &profile.AutoSkipRecap}, {columns.AutoPlayNextPreview, &profile.AutoPlayNextPreview},
	} {
		if pair.value != nil {
			*pair.field = *pair.value
		}
	}
	if err := w.CreateProfile(ctx, profile); err != nil {
		return err
	}
	if !profile.ShowForcedSubtitles {
		off := false
		if err := w.UpdateProfile(ctx, profile.ID, userstore.UpdateProfileInput{ShowForcedSubtitles: &off}); err != nil {
			return err
		}
	}
	inherited, err := planInheritedLegacyUserSettings(ctx, w)
	if err != nil {
		return err
	}
	keys, err := writeCanonicalSettingsSync(ctx, w, userstore.SettingIdentity{Scope: settingscontract.ScopeProfile, ProfileID: profile.ID}, inherited)
	if err != nil {
		return err
	}
	for _, key := range keys {
		r.changed = append(r.changed, profileSetSettingChange{profileID: profile.ID, key: key})
	}
	if recording {
		created, err := audited.GetProfile(ctx, profile.ID)
		if err != nil {
			return err
		}
		entry, err := recordProfileMutation(ctx, audited.AuditTx(), nil, created)
		if err != nil {
			return err
		}
		r.audits = append(r.audits, entry)
	}
	return r.finishProfile(ctx, w, i, profile.ID, item.PIN.kind == profileSetPINKeep)
}

// finishProfile writes an item's settings, its place in the set and the
// applied record, each only where it differs from what is stored.
//
// The applied record holds the set's value of every field it carries, one
// the owner overrides here included, as if it had been written: it is what
// the set asks of the profile, not what this server stores.
func (r *profileSetRun) finishProfile(ctx context.Context, w userstore.ProfileSetWriter, i int, profileID string, awaitingPin bool) error {
	item, plan := r.doc.Items[i], r.edits.withoutOverrides(r.edits.ids[i], r.settings[i])
	base := userstore.SettingIdentity{Scope: settingscontract.ScopeProfile, ProfileID: profileID}
	keys := make([]string, 0, len(plan.write))
	for key := range plan.write {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		id := base
		id.Key = key
		wrote, err := w.PutSettingValueIfChanged(ctx, id, plan.write[key])
		if err != nil {
			return fmt.Errorf("storing %s: %w", key, err)
		}
		if wrote {
			r.changed = append(r.changed, profileSetSettingChange{profileID: profileID, key: key})
		}
	}
	for _, key := range plan.reset {
		id := base
		id.Key = key
		removed, err := w.DeleteSettingValue(ctx, id)
		if err != nil {
			return fmt.Errorf("resetting %s: %w", key, err)
		}
		if removed {
			r.changed = append(r.changed, profileSetSettingChange{profileID: profileID, key: key})
		}
	}
	if _, err := w.SetSyncFields(ctx, profileID, item.SyncID, i); err != nil {
		return err
	}
	var age *int
	if item.MaxAdvisoryAge != 0 {
		age = &item.MaxAdvisoryAge
	}
	record, err := json.Marshal(profileSetApplied{
		Name: item.Name, Picture: item.Picture, PIN: item.PIN.kind, IsChild: item.IsChild,
		MaxContentRating: item.MaxContentRating, MaxAdvisoryAge: age, RequireAdvisoryAge: item.RequireAdvisoryAge,
		RequestsBlocked: item.RequestsBlocked, Settings: plan.applied, SettingsReset: append([]string{}, r.settings[i].reset...),
		AwaitingPin: awaitingPin,
	})
	if err != nil {
		return err
	}
	_, err = w.SetSyncApplied(ctx, profileID, record)
	return err
}

// afterCommit runs what a committed set leaves to do outside its
// transaction, each step best-effort and independent of the client.
func (r *profileSetRun) afterCommit(ctx context.Context) {
	if r.noop {
		return
	}
	h, userID := r.h, r.cmd.UserID
	for _, entry := range r.audits {
		auditmutation.CommitMutation(ctx, entry)
	}
	for _, gone := range r.deleted {
		read := r.devices[gone.profile.ID]
		h.afterProfileDeleted(ctx, userID, gone.profile, read.devices, gone.collections, read.registry)
	}
	for _, change := range r.changed {
		publishUserSettingsEvent(ctx, h.EventsHub, userID, change.profileID, change.key, string(settingscontract.ScopeProfile))
	}
	for _, id := range r.signals {
		notifySignalsChanged(ctx, h.RecWorker, userID, id)
	}
	for _, id := range r.pinResets {
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		h.PINAttempts.Reset(resetCtx, ratelimit.ProfilePINKey(userID, id))
		cancel()
	}
	for _, id := range r.avatars {
		if err := deleteUploadedAvatarObjects(ctx, h.AvatarStore, userID, id); err != nil {
			slog.WarnContext(ctx, "profile avatar cleanup failed after a profile set", "component", "api", "user_id", userID, "profile_id", id, "error", err)
		}
	}
}

// result reads the account back and describes it.
func (r *profileSetRun) result(ctx context.Context) (ProfileSetResult, error) {
	snap, err := r.setStore.ReadProfileSet(ctx)
	if err != nil {
		return ProfileSetResult{}, err
	}
	result := buildProfileSetResult(r.doc, snap, r.settings)
	if result.Overrides, err = r.overrides(ctx, snap); err != nil {
		return ProfileSetResult{}, err
	}
	result.JellyfinBareNameSignIn = r.h.jellyfinBareNameSignIn(ctx, r.cmd.UserID, snap.Rows)
	return result, nil
}

// overrides lists the owner's values of the set's profiles, as they stand.
func (r *profileSetRun) overrides(ctx context.Context, snap userstore.ProfileSetSnapshot) ([]ProfileSetOverride, error) {
	edits := newProfileSetEdits(snap.Edits)
	inSet := map[string]bool{}
	for _, item := range r.doc.Items {
		inSet[item.SyncID] = true
	}
	var items []ProfileSetHeldItem
	for i := range snap.Rows {
		row := &snap.Rows[i]
		if !inSet[row.SyncID] || len(edits.overrides[row.SyncID]) == 0 {
			continue
		}
		item, err := r.h.heldItem(ctx, r.store, r.contract, row, snap.Settings[row.ID])
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return edits.overridesOf(items), nil
}
