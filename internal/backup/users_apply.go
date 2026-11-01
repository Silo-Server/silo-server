package backup

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// UserSelection chooses one backup account: the target its preview showed
// (the local account id, or "new"), and the profiles to restore with the
// data types of each.
type UserSelection struct {
	Account  string             `json:"account"`
	Target   string             `json:"target"`
	Profiles []ProfileSelection `json:"profiles,omitempty"`
}

// ProfileSelection chooses one profile of a selected account.
type ProfileSelection struct {
	Profile   string   `json:"profile"`
	Target    string   `json:"target"`
	DataTypes []string `json:"data_types,omitempty"`
}

// ReportAccount is a restored account's outcome.
type ReportAccount struct {
	LocalID  int             `json:"local_id,omitempty"`
	Created  bool            `json:"created"`
	Profiles []ReportProfile `json:"profiles"`
	// Unmatched lists, by title, items the account's data names that this
	// server's catalog does not hold; their entries were left out.
	Unmatched []string `json:"unmatched,omitempty"`
	// Hidden lists, by title, items a profile's access here hides (its
	// libraries or maturity limits); their entries were left out.
	Hidden []string `json:"hidden,omitempty"`
}

// ReportProfile is one chosen profile's outcome.
type ReportProfile struct {
	ID      string       `json:"id"`
	Label   string       `json:"label"`
	LocalID string       `json:"local_id,omitempty"`
	Created bool         `json:"created"`
	Outcome string       `json:"outcome"`
	Reason  string       `json:"reason,omitempty"`
	Message string       `json:"message,omitempty"`
	Notes   []string     `json:"notes,omitempty"`
	Data    []ReportData `json:"data"`
}

// ReportData is what importing one data type did.
type ReportData struct {
	Type string `json:"type"`
	userdata.Result
}

// validateUserSelection checks a request's users before it is queued.
func validateUserSelection(users []UserSelection) error {
	seen := map[string]bool{}
	known := map[string]bool{}
	for _, id := range UserDataTypeIDs() {
		known[id] = true
	}
	validTarget := func(t string) bool { return t == TargetNew || (t != "" && len(t) <= 128) }
	for _, u := range users {
		if !validItemSegment(u.Account) || seen[u.Account] || !validTarget(u.Target) {
			return &SelectionError{Message: "users lists an invalid or repeated account, or one without a target"}
		}
		if u.Target != TargetNew {
			if n, err := strconv.Atoi(u.Target); err != nil || n <= 0 {
				return &SelectionError{Message: fmt.Sprintf("account %s names an invalid target %q", u.Account, u.Target)}
			}
		}
		seen[u.Account] = true
		profiles := map[string]bool{}
		for _, p := range u.Profiles {
			if !userdata.ValidContentID(p.Profile) || profiles[p.Profile] || !validTarget(p.Target) {
				return &SelectionError{Message: fmt.Sprintf("account %s lists an invalid or repeated profile", u.Account)}
			}
			profiles[p.Profile] = true
			types := map[string]bool{}
			for _, t := range p.DataTypes {
				if !known[t] || types[t] {
					return &SelectionError{Message: fmt.Sprintf("profile %s lists an unknown or repeated data type %q", p.Profile, t)}
				}
				types[t] = true
			}
		}
	}
	return nil
}

// applyAccount restores one selected account in the item's transaction. The
// account and its profiles are matched again here; a mapping that changed
// since the preview is left out. An account or profile found here is never
// changed (password, role, permissions, group, login and owner flags, PIN,
// and access stay); only missing profiles are added and chosen data merged.
func (a *applier) applyAccount(ctx context.Context, tx pgx.Tx, sel UserSelection) (applied, error) {
	item := ReportItem{Kind: KindAccount, Label: sel.Account}
	var doc *UserDocument
	for i := range a.rs.Users {
		if strconv.Itoa(a.rs.Users[i].Account.ID) == sel.Account {
			doc = &a.rs.Users[i]
		}
	}
	if doc == nil {
		return leftOut(item, ReasonNotInBackup, "The backup has no such account."), nil
	}
	planner := a.s.newUserPlanner(a.rs, tx, a.serverID, a.libraryMap())
	plan, err := planner.plan(ctx, doc)
	if err != nil {
		return applied{}, err
	}
	item.Label = plan.item.Label
	if !plan.item.Selectable {
		return leftOutFor(item, plan.item), nil
	}
	if !sameTarget(sel.Target, plan.target(), plan.item.Account.Match) {
		return leftOut(item, ReasonChanged, "The account this backup account maps to changed since the preview. Open the backup again."), nil
	}
	report := &ReportAccount{Profiles: []ReportProfile{}}
	item.Account = report
	if plan.localID == 0 {
		// A new account's id is this transaction's own; nothing else can
		// hold its locks before it commits.
		if plan.localID, err = a.createAccount(ctx, tx, doc, plan); err != nil {
			return applied{}, err
		}
		report.Created = true
	}
	// The account locks come first, before any profile is created or any
	// data row written (userdata.Preparer).
	if err := pgstore.LockAccountForImport(ctx, tx, plan.localID); err != nil {
		return applied{}, err
	}
	report.LocalID = plan.localID
	r := &accountRestore{a: a, tx: tx, doc: doc, plan: &plan, report: report, sameServer: planner.sameServer,
		profiles: map[string]string{}, changed: report.Created}
	if r.items, err = newItemResolver(ctx, tx, doc.Items, documentItems(doc)); err != nil {
		return applied{}, err
	}
	// Profiles of this account here, by source id: matched ones, and the
	// ones this restore creates, so shared collections reach them.
	for _, pp := range plan.profiles {
		if pp.localID != "" {
			r.profiles[pp.entry.ID] = pp.localID
		}
	}
	var chosen []chosenProfile
	for _, ps := range sel.Profiles {
		c, ok, err := r.choose(ctx, ps)
		if err != nil {
			return applied{}, err
		}
		if ok {
			chosen = append(chosen, c)
		}
	}
	if err := prepareImport(ctx, tx, r.preparedDocs(chosen)); err != nil {
		return applied{}, err
	}
	for _, c := range chosen {
		if err := r.importData(ctx, c); err != nil {
			return applied{}, err
		}
	}
	report.Unmatched, report.Hidden = r.items.unmatchedTitles(), r.items.hiddenTitles()
	if len(report.Unmatched) > 0 {
		item.Notes = append(item.Notes, fmt.Sprintf("%d items are not in this server's catalog and were left out.", len(r.items.unmatched)))
	}
	return applied{item: accountOutcome(item, report, r.changed)}, nil
}

// accountOutcome is restored when anything changed, else left out with the
// first left-out profile's reason, else unchanged.
func accountOutcome(item ReportItem, report *ReportAccount, changed bool) ReportItem {
	if changed {
		item.Outcome = OutcomeRestored
		if report.Created {
			item.Message = "Created the account."
		}
		return item
	}
	item.Outcome = OutcomeUnchanged
	for _, rp := range report.Profiles {
		if rp.Outcome == OutcomeLeftOut {
			item.Outcome, item.Reason, item.Message = OutcomeLeftOut, rp.Reason, rp.Message
			break
		}
	}
	return item
}

// documentItems lists the content ids every profile's data names.
func documentItems(doc *UserDocument) []string {
	var out []string
	for _, p := range doc.Profiles {
		for _, t := range userDataTypes {
			if raw, ok := p.Data[t.ID()]; ok {
				// The reader validated every document.
				sum, _ := t.Validate(raw)
				out = append(out, sum.Items...)
			}
		}
	}
	return out
}

// createAccount creates a new account with the backup's fields and records
// that this restore made it.
func (a *applier) createAccount(ctx context.Context, tx pgx.Tx, doc *UserDocument, plan accountPlan) (int, error) {
	user, err := auth.CreateRestoredAccountInTx(ctx, tx, restoredAccount(doc.Account, plan))
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO backup_restored_accounts (source_server_id, source_account_id, user_id) VALUES ($1, $2, $3)
		ON CONFLICT (source_server_id, source_account_id) DO UPDATE SET user_id = EXCLUDED.user_id, restored_at = now()`,
		a.rs.Index.ServerID, doc.Account.ID, user.ID); err != nil {
		return 0, fmt.Errorf("recording the restored account: %w", err)
	}
	return user.ID, nil
}

// accountRestore is one account's restore in progress.
type accountRestore struct {
	a          *applier
	tx         pgx.Tx
	doc        *UserDocument
	plan       *accountPlan
	report     *ReportAccount
	items      *itemResolver
	sameServer bool
	// profiles maps the account's source profile ids to profiles here.
	profiles map[string]string
	changed  bool
}

// chosenProfile is a profile whose data the restore merges.
type chosenProfile struct {
	plan *profilePlan
	sel  ProfileSelection
	// rep indexes report.Profiles.
	rep int
}

// choose revalidates one chosen profile, creating it when it is new, and
// adds it to the report. It returns false for a profile left out.
func (r *accountRestore) choose(ctx context.Context, ps ProfileSelection) (chosenProfile, bool, error) {
	rp := ReportProfile{ID: ps.Profile, Label: ps.Profile, Data: []ReportData{}}
	pp, err := r.resolveProfile(ctx, ps, &rp)
	if err != nil {
		return chosenProfile{}, false, err
	}
	c := chosenProfile{plan: pp, sel: ps, rep: len(r.report.Profiles)}
	r.report.Profiles = append(r.report.Profiles, rp)
	return c, pp != nil, nil
}

// resolveProfile finds the selected profile's plan here, or records in rp
// why it is left out and returns nil.
func (r *accountRestore) resolveProfile(ctx context.Context, ps ProfileSelection, rp *ReportProfile) (*profilePlan, error) {
	var pp *profilePlan
	for i := range r.plan.profiles {
		if r.plan.profiles[i].entry.ID == ps.Profile {
			pp = &r.plan.profiles[i]
		}
	}
	if pp == nil {
		rp.Outcome, rp.Reason, rp.Message = OutcomeLeftOut, ReasonNotInBackup, "The backup has no such profile."
		return nil, nil
	}
	rp.Label = pp.entry.Name
	switch {
	case !pp.preview.Selectable:
		rp.Outcome, rp.Reason, rp.Message = OutcomeLeftOut, pp.preview.Reason, pp.preview.Message
		return nil, nil
	case !sameTarget(ps.Target, pp.target(), pp.preview.Match):
		rp.Outcome, rp.Reason = OutcomeLeftOut, ReasonChanged
		rp.Message = "The profile this backup profile maps to changed since the preview. Open the backup again."
		return nil, nil
	}
	if pp.localID == "" {
		created, err := r.a.createProfile(ctx, r.tx, r.doc, *r.plan, pp, rp)
		if err != nil || !created {
			return nil, err
		}
		r.profiles[pp.entry.ID] = pp.localID
		r.changed = true
	}
	rp.LocalID = pp.localID
	return pp, nil
}

// visibleItems is the account's items as the profile here may see them:
// its libraries under the account's policy, and its maturity limits.
func (r *accountRestore) visibleItems(ctx context.Context, profileID string) (*filteredItems, error) {
	profile, err := pgstore.ProfileInTransaction(ctx, r.tx, r.plan.localID, profileID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, fmt.Errorf("profile %s is gone", profileID)
	}
	libraries, restricted := access.ProfileLibraries(r.plan.effective.LibraryIDs, profile)
	if !restricted {
		libraries = nil
	}
	allowUnrated := r.a.s.opts.AllowUnratedContent != nil && r.a.s.opts.AllowUnratedContent(ctx)
	return r.items.visibleTo(ctx, r.tx, contentFilter{libraries: libraries, limits: access.ProfileMaturity(profile, allowUnrated)})
}

// preparedDocs lists the documents the chosen profiles import, by type.
func (r *accountRestore) preparedDocs(chosen []chosenProfile) map[string][]userdata.Prepared {
	out := map[string][]userdata.Prepared{}
	for _, c := range chosen {
		for _, t := range userDataTypes {
			raw, ok := c.plan.entry.Data[t.ID()]
			if ok && slices.Contains(c.sel.DataTypes, t.ID()) {
				out[t.ID()] = append(out[t.ID()], userdata.Prepared{Owner: userdata.Owner{UserID: r.plan.localID, ProfileID: c.plan.localID}, Data: raw})
			}
		}
	}
	return out
}

// importData merges the chosen data types of one profile.
func (r *accountRestore) importData(ctx context.Context, c chosenProfile) error {
	rep := &r.report.Profiles[c.rep]
	a := r.a
	items, err := r.visibleItems(ctx, c.plan.localID)
	if err != nil {
		return err
	}
	target := userdata.Target{
		Owner:      userdata.Owner{UserID: r.plan.localID, ProfileID: c.plan.localID},
		SameServer: r.sameServer, Items: items, Now: a.s.opts.Now().UTC(),
		Library: mapIn(a.libraryMap()),
		Profile: func(id string) (string, bool) { local, ok := r.profiles[id]; return local, ok },
		WriteBlob: func(ctx context.Context, store, from, to string) error {
			return a.writeBlobFamily(ctx, blobStoreName(store), from, to)
		},
		HasBlob:            a.rs.HasBlob,
		PluginInstallation: a.pluginInstallation(ctx, r.tx),
	}
	if a.adminCollections == nil {
		a.adminCollections = map[string]string{}
	}
	target.Collection = newCollectionMap(a.planner(r.tx), r.tx, a.libraryMap(), r.sameServer, a.adminCollections, r.doc).lookup(target)
	changed := rep.Created
	for _, t := range userDataTypes {
		raw, ok := c.plan.entry.Data[t.ID()]
		if !ok || !slices.Contains(c.sel.DataTypes, t.ID()) {
			continue
		}
		res, err := t.Import(ctx, r.tx, target, raw)
		if err != nil {
			return fmt.Errorf("profile %s %s: %w", c.plan.entry.Name, t.ID(), err)
		}
		rep.Data = append(rep.Data, ReportData{Type: t.ID(), Result: res})
		changed = changed || res.Changed()
	}
	rep.Outcome = OutcomeUnchanged
	if changed {
		rep.Outcome, r.changed = OutcomeRestored, true
	}
	return nil
}

// sameTarget reports whether a selection's target is still the mapping: the
// same local id, or "new" for something an earlier run of this restore has
// since created (matched through the restore's own record).
func sameTarget(want, got, match string) bool {
	return want == got || (want == TargetNew && match == MatchRestored)
}

func blobStoreName(store string) string {
	if store == userdata.StoreOperational {
		return storeOperational
	}
	return storeAssets
}

// restoredAccount is the account a restore creates from a backup entry, with
// library ids mapped and the group the planner chose.
func restoredAccount(a AccountEntry, plan accountPlan) auth.RestoredAccount {
	p := a.Policy
	return auth.RestoredAccount{Email: a.Email, Username: a.Username, PasswordHash: a.PasswordHash, LocalPasswordLoginEnabled: a.LocalPasswordLoginEnabled,
		PasswordChangeRequired: a.PasswordChangeRequired, Enabled: a.Enabled, Role: a.Role, Permissions: a.Permissions, LibraryIDs: plan.libraries,
		MaxPlaybackQuality: p.MaxPlaybackQuality, MaxStreams: p.MaxStreams, MaxTranscodes: p.MaxTranscodes,
		MaxRemoteStreamBitrateKbps: p.MaxRemoteStreamBitrateKbps, MaxLocalStreamBitrateKbps: p.MaxLocalStreamBitrateKbps,
		TranscodeAllowed: p.TranscodeAllowed, AudioTranscodeAllowed: p.AudioTranscodeAllowed, MaxProfiles: p.MaxProfiles,
		DownloadAllowed: p.DownloadAllowed, DownloadTranscodeAllowed: p.DownloadTranscodeAllowed, RequestsAllowed: p.RequestsAllowed,
		AccessGroupID: plan.groupID}
}

// createProfile adds a missing profile with the backup's name, avatar, PIN
// hash, and access fields, its allowed libraries mapped (entries with no
// library here dropped, the restriction flag kept). It reports false when
// the account's profile limit here leaves no room.
func (a *applier) createProfile(ctx context.Context, tx pgx.Tx, doc *UserDocument, plan accountPlan, pp *profilePlan, rp *ReportProfile) (bool, error) {
	count, err := pgstore.LockProfilesInTx(ctx, tx, plan.localID)
	if err != nil {
		return false, err
	}
	if plan.profileLimit > 0 && count >= plan.profileLimit {
		rp.Outcome, rp.Reason = OutcomeLeftOut, ReasonProfileLimit
		rp.Message = fmt.Sprintf("The account already has %d profiles, its limit here.", count)
		return false, nil
	}
	e := pp.entry
	id := pgstore.NewProfileID()
	avatar, err := a.restoreAvatar(ctx, e.Avatar, plan.localID, id, rp)
	if err != nil {
		return false, err
	}
	if err := pgstore.CreateRestoredProfileInTx(ctx, tx, plan.localID, pgstore.RestoredProfile{
		ID: id, Name: e.Name, Avatar: avatar, IsPrimary: e.IsPrimary, PINHash: e.PINHash, IsChild: e.IsChild,
		MaxContentRating: e.MaxContentRating, MaxAdvisoryAge: e.MaxAdvisoryAge, RequireAdvisoryAge: e.RequireAdvisoryAge,
		LibraryRestrictionsEnabled: e.LibraryRestrictionsEnabled, AllowedLibraryIDs: pp.allowed, MaxPlaybackQuality: e.MaxPlaybackQuality,
	}); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO backup_restored_profiles (source_server_id, source_account_id, source_profile_id, user_id, profile_id) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source_server_id, source_account_id, source_profile_id) DO UPDATE
			SET user_id = EXCLUDED.user_id, profile_id = EXCLUDED.profile_id, restored_at = now()`,
		a.rs.Index.ServerID, doc.Account.ID, e.ID, plan.localID, id); err != nil {
		return false, fmt.Errorf("recording the restored profile: %w", err)
	}
	pp.localID, rp.Created = id, true
	if pp.preview.LibrariesLeftOut > 0 {
		rp.Notes = append(rp.Notes, fmt.Sprintf("%d of its allowed libraries are not on this server and were left out.", pp.preview.LibrariesLeftOut))
	}
	if pp.preview.NoLibraryAccess {
		rp.Notes = append(rp.Notes, "None of its allowed libraries are on this server, so it has no library access.")
	}
	return true, nil
}

// restoreAvatar copies an uploaded avatar under the new profile's key
// before the row that references it, and returns the avatar to store. An
// uploaded avatar the backup lacks is dropped; a preset or https avatar is
// kept as it is.
func (a *applier) restoreAvatar(ctx context.Context, avatar string, userID int, profileID string, rp *ReportProfile) (string, error) {
	key := avatarBlob(avatar)
	switch {
	case key == "" && strings.Contains(avatar, "://") && !strings.HasPrefix(avatar, "https://"):
		return "", nil
	case key == "":
		return avatar, nil
	case !a.rs.HasBlob(key):
		rp.Notes = append(rp.Notes, "Its avatar was missing from the backup.")
		return "", nil
	}
	to := fmt.Sprintf("profile-avatars/%d/%s/%s", userID, profileID, path.Base(key))
	if err := a.writeBlobFamily(ctx, storeOperational, key, to); err != nil {
		return "", err
	}
	return uploadedAvatarPrefix + to, nil
}

// writeBlobFamily writes a carried original and the variants the archive
// carries beside it into the directory of to.
func (a *applier) writeBlobFamily(ctx context.Context, store, from, to string) error {
	family := a.rs.blobFamily(from)
	if len(family) == 0 {
		return fmt.Errorf("%s: %w", from, ErrNotFound)
	}
	dir := path.Dir(to)
	for _, key := range family {
		dest := to
		if key != from {
			dest = dir + "/" + path.Base(key)
		}
		if err := a.writeBlobTo(ctx, store, key, dest); err != nil {
			return err
		}
	}
	return nil
}

// pluginInstallation maps a source plugin installation to the one here: the
// restore set's plugins section names its plugin, and the one installation
// here of that plugin with the capability is the same one, whether this
// restore installed it or it was here already. None, or more than one, maps
// nothing.
func (a *applier) pluginInstallation(ctx context.Context, q queryer) func(int, string) (int, bool) {
	type key struct {
		id         int
		capability string
	}
	known := map[key]int{}
	return func(installationID int, capabilityID string) (int, bool) {
		k := key{installationID, capabilityID}
		if local, seen := known[k]; seen {
			return local, local > 0
		}
		local, ok := a.lookUpPluginInstallation(ctx, q, installationID, capabilityID)
		known[k] = local
		return local, ok
	}
}

func (a *applier) lookUpPluginInstallation(ctx context.Context, q queryer, installationID int, capabilityID string) (int, bool) {
	pluginID := ""
	for _, d := range a.rs.Plugins {
		if d.InstallationID == installationID {
			pluginID = d.PluginID
		}
	}
	if pluginID == "" {
		return 0, false
	}
	ids, err := collectStringsArgs(ctx, q, `
		SELECT DISTINCT i.id::text FROM plugin_installations i
		JOIN plugin_capabilities c ON c.plugin_installation_id = i.id
		WHERE i.plugin_id = $1 AND c.capability_id = $2`, pluginID, capabilityID)
	if err != nil || len(ids) != 1 {
		return 0, false
	}
	local, err := strconv.Atoi(ids[0])
	return local, err == nil
}
