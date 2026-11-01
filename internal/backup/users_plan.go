package backup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// How a backup account or profile was matched here.
const (
	// MatchAccountID: the backup is this server's, and the account id
	// exists here.
	MatchAccountID = "account_id"
	// MatchRestored: an earlier restore of this backup's server created it.
	MatchRestored = "restored"
	// MatchLogin: the one account whose username or email is the backup
	// account's username or email.
	MatchLogin = "username_email"
	// MatchProfileID: the backup is this server's, and the profile id
	// exists in the mapped account.
	MatchProfileID = "profile_id"
	// MatchProfileName: the one profile of the mapped account with the
	// name.
	MatchProfileName = "name"
	// MatchNew: nothing here; restoring creates it.
	MatchNew = "new"
)

// TargetNew is the target a selection names for an account or profile the
// restore creates.
const TargetNew = "new"

// Reasons a users item or profile cannot be chosen.
const (
	ReasonAmbiguousAccount     = "ambiguous_account"
	ReasonAmbiguousProfile     = "ambiguous_profile"
	ReasonPolicyWider          = "policy_wider"
	ReasonGroupNotOnThisServer = "group_not_on_this_server"
	ReasonLoginTaken           = "login_taken"
	ReasonProfileLimit         = "profile_limit"
	ReasonUserStore            = "user_store_unsupported"
)

// KindAccount is the users section's item kind.
const KindAccount = "account"

// PreviewAccount describes a backup account against this server.
type PreviewAccount struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	// Admin marks an administrator account: restoring it creates an
	// administrator here.
	Admin bool `json:"admin"`
	// Match is how the account was matched; Target is what a selection
	// names: the local account id, or "new".
	Match         string `json:"match"`
	Target        string `json:"target"`
	LocalID       *int   `json:"local_id,omitempty"`
	LocalUsername string `json:"local_username,omitempty"`
	// Group is the account's access group in the backup; a new account
	// joins the group of that name here.
	Group string `json:"group,omitempty"`
	// PolicyDifferences lists what this server's policy for a new account
	// grants beyond the backup's.
	PolicyDifferences []PolicyDifference `json:"policy_differences,omitempty"`
	// LibrariesLeftOut counts the account's library entries with no library
	// here; they are dropped, never widened.
	LibrariesLeftOut int `json:"libraries_left_out"`
	// ProfileRoom is how many new profiles the account can take here.
	ProfileRoom int              `json:"profile_room"`
	Profiles    []PreviewProfile `json:"profiles"`
}

// PolicyDifference is one field where this server would grant more.
type PolicyDifference struct {
	Field  string `json:"field"`
	Backup string `json:"backup"`
	Here   string `json:"here"`
}

// PreviewProfile describes a backup profile within its account.
type PreviewProfile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Selectable bool   `json:"selectable"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	Match      string `json:"match"`
	Target     string `json:"target"`
	LocalID    string `json:"local_id,omitempty"`
	IsPrimary  bool   `json:"is_primary"`
	// Restricted is library_restrictions_enabled; LibrariesLeftOut counts
	// its allowed libraries with no library here, and NoLibraryAccess is a
	// restricted profile left with none.
	Restricted       bool              `json:"restricted"`
	LibrariesLeftOut int               `json:"libraries_left_out"`
	NoLibraryAccess  bool              `json:"no_library_access"`
	HasPIN           bool              `json:"has_pin"`
	DataTypes        []PreviewDataType `json:"data_types"`
}

// PreviewDataType is one data type a profile carries.
type PreviewDataType struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
	// Pending is, for a profile on this server, how many entries a restore
	// would add or change; absent for a profile a restore creates.
	Pending *int `json:"pending,omitempty"`
}

// accountPlan is one backup account against this server.
type accountPlan struct {
	doc     *UserDocument
	item    PreviewItem
	localID int
	// groupID is the group a new account joins; libraries its library
	// override mapped here, nil kept nil.
	groupID   *int64
	libraries []int
	// effective is the account's policy here.
	effective access.EffectiveUserPolicy
	// profileLimit is the account's effective profile limit here and
	// existing how many profiles it has.
	profileLimit, existing int
	profiles               []profilePlan
}

// profilePlan is one backup profile against its mapped account.
type profilePlan struct {
	entry   *ProfileEntry
	preview PreviewProfile
	localID string
	allowed []int
}

func (p *accountPlan) target() string {
	if p.localID > 0 {
		return strconv.Itoa(p.localID)
	}
	return TargetNew
}

func (p *profilePlan) target() string {
	if p.localID != "" {
		return p.localID
	}
	return TargetNew
}

// mapLibraries maps library ids through libMap, dropping ids with no library
// here. Nil stays nil (unrestricted or inherit) and a list never becomes nil,
// so access can only narrow.
func mapLibraries(ids []int, libMap map[int]int) (mapped []int, dropped int) {
	if ids == nil {
		return nil, 0
	}
	mapped = []int{}
	seen := map[int]bool{}
	for _, id := range ids {
		local, ok := libMap[id]
		if !ok {
			dropped++
			continue
		}
		if !seen[local] {
			seen[local] = true
			mapped = append(mapped, local)
		}
	}
	return mapped, dropped
}

// userPlanner matches user documents against this server through q.
type userPlanner struct {
	q          queryer
	sourceID   string
	sameServer bool
	libMap     map[int]int
	// userStore is false when this server's user store cannot take restored
	// accounts (the SQLite bridge backend).
	userStore bool
	// pending, set for a preview, counts what a matched account's restore
	// would change; nil (a restore) skips the count.
	pending *pendingCompare
}

func (s *Service) newUserPlanner(rs *RestoreSet, q queryer, serverID string, libMap map[int]int) *userPlanner {
	backend := ""
	if s.opts.UserStoreBackend != nil {
		backend = strings.ToLower(strings.TrimSpace(s.opts.UserStoreBackend()))
	}
	return &userPlanner{q: q, sourceID: rs.Index.ServerID, sameServer: rs.Index.ServerID == serverID, libMap: libMap, userStore: backend != "sqlite"}
}

func (u *userPlanner) planAll(ctx context.Context, docs []UserDocument) ([]accountPlan, error) {
	out := make([]accountPlan, 0, len(docs))
	for i := range docs {
		plan, err := u.plan(ctx, &docs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, plan)
	}
	return out, nil
}

// plan matches one account and its profiles.
func (u *userPlanner) plan(ctx context.Context, doc *UserDocument) (accountPlan, error) {
	a := doc.Account
	label := a.Username
	if label == "" {
		label = a.Email
	}
	plan := accountPlan{doc: doc, item: PreviewItem{ID: strconv.Itoa(a.ID), Kind: KindAccount, Label: label}}
	acct := &PreviewAccount{Username: a.Username, Email: a.Email, Role: a.Role, Admin: a.Role == models.RoleAdmin, Profiles: []PreviewProfile{}}
	if a.Group != nil {
		acct.Group = a.Group.Name
	}
	plan.item.Account = acct
	if !u.userStore {
		plan.item.Status, plan.item.Reason = ItemNotOnThisServer, ReasonUserStore
		plan.item.Message = "This server uses the SQLite user store. Import it into PostgreSQL before restoring accounts."
		acct.Match, acct.Target = MatchNew, TargetNew
		return plan, nil
	}
	match, localID, reason, message, err := u.matchAccount(ctx, a)
	if err != nil {
		return plan, err
	}
	acct.Match = match
	if reason != "" {
		plan.item.Status, plan.item.Reason, plan.item.Message = ItemNotOnThisServer, reason, message
		if reason == ReasonAmbiguousAccount {
			plan.item.Status = ItemAmbiguous
		}
		acct.Target = TargetNew
		return plan, nil
	}
	plan.localID = localID
	acct.Target = plan.target()
	if localID > 0 {
		if err := u.planExisting(ctx, &plan); err != nil {
			return plan, err
		}
	} else if err := u.planNew(ctx, &plan); err != nil {
		return plan, err
	}
	if !plan.item.Selectable {
		return plan, nil
	}
	if err := u.planProfiles(ctx, &plan); err != nil {
		return plan, err
	}
	if u.pending != nil && plan.localID > 0 {
		if err := u.planPending(ctx, &plan); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// matchAccount finds the account a backup account is here. It never
// guesses: an automatic match needs exactly one candidate.
func (u *userPlanner) matchAccount(ctx context.Context, a AccountEntry) (match string, localID int, reason, message string, err error) {
	err = u.q.QueryRow(ctx, `SELECT user_id FROM backup_restored_accounts WHERE source_server_id = $1 AND source_account_id = $2`,
		u.sourceID, a.ID).Scan(&localID)
	if err == nil {
		return MatchRestored, localID, "", "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, "", "", fmt.Errorf("reading restored accounts: %w", err)
	}
	if u.sameServer {
		err = u.q.QueryRow(ctx, `SELECT id FROM users WHERE id = $1`, a.ID).Scan(&localID)
		if err == nil {
			return MatchAccountID, localID, "", "", nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", 0, "", "", fmt.Errorf("reading accounts: %w", err)
		}
	}
	// Usernames and emails share one sign-in namespace, so each lookup
	// checks both columns, case-insensitively.
	lookup := func(value string) ([]int, error) {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil
		}
		return collectIntsArgs(ctx, u.q, `SELECT id FROM users WHERE lower(username) = lower($1) OR lower(email) = lower($1) ORDER BY id`, value)
	}
	byUsername, err := lookup(a.Username)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("matching accounts: %w", err)
	}
	byEmail, err := lookup(a.Email)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("matching accounts: %w", err)
	}
	switch {
	case len(byUsername) > 1:
		return MatchNew, 0, ReasonAmbiguousAccount, fmt.Sprintf("%d accounts here sign in as %q.", len(byUsername), a.Username), nil
	case len(byEmail) > 1:
		return MatchNew, 0, ReasonAmbiguousAccount, fmt.Sprintf("%d accounts here sign in as %q.", len(byEmail), a.Email), nil
	case len(byUsername) == 1 && len(byEmail) == 1 && byUsername[0] != byEmail[0]:
		return MatchNew, 0, ReasonAmbiguousAccount, "The username and the email belong to different accounts here.", nil
	case u.sameServer && (len(byUsername) == 1 || len(byEmail) == 1):
		// The backup's own account is gone, and another account here took
		// its sign-in: creating it again would collide.
		return MatchNew, 0, ReasonLoginTaken, "Another account here now signs in with this username or email.", nil
	case len(byUsername) == 1:
		return MatchLogin, byUsername[0], "", "", nil
	case len(byEmail) == 1:
		return MatchLogin, byEmail[0], "", "", nil
	}
	return MatchNew, 0, "", "", nil
}

func collectIntsArgs(ctx context.Context, q queryer, sql string, args ...any) ([]int, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// planExisting describes a matched account: nothing about it changes, so
// only its profile limit matters.
func (u *userPlanner) planExisting(ctx context.Context, plan *accountPlan) error {
	user, err := auth.UserByID(ctx, u.q, plan.localID)
	if err != nil {
		return fmt.Errorf("reading account %d: %w", plan.localID, err)
	}
	var group *access.GroupPolicy
	if access.GroupApplies(user) {
		if group, err = access.GroupPolicyForUser(ctx, u.q, user.ID); err != nil {
			return err
		}
	}
	plan.effective = access.ApplyGroupPolicy(user, group)
	plan.profileLimit = plan.effective.MaxProfiles
	if err := u.q.QueryRow(ctx, `SELECT count(*) FROM user_profiles WHERE user_id = $1`, user.ID).Scan(&plan.existing); err != nil {
		return fmt.Errorf("counting profiles: %w", err)
	}
	acct := plan.item.Account
	acct.LocalID, acct.LocalUsername = new(user.ID), user.Username
	plan.item.Status, plan.item.Selectable = ItemDiffers, true
	plan.item.Message = "The account is on this server and stays as it is. Restoring adds the profiles it lacks and merges the data you choose."
	return nil
}

// planNew computes a new account's policy here with the access resolver and
// compares it with the backup's, field by field. Anything this server would
// grant beyond the backup makes the account unselectable.
func (u *userPlanner) planNew(ctx context.Context, plan *accountPlan) error {
	a := plan.doc.Account
	acct := plan.item.Account
	user := a.user()
	user.LibraryIDs, acct.LibrariesLeftOut = mapLibraries(a.Policy.LibraryIDs, u.libMap)
	plan.libraries = user.LibraryIDs
	var group *access.GroupPolicy
	if a.Role != models.RoleAdmin && a.Group != nil {
		local, err := access.GroupPolicyByName(ctx, u.q, a.Group.Name)
		if err != nil {
			return err
		}
		if local == nil {
			plan.item.Status, plan.item.Reason = ItemNotOnThisServer, ReasonGroupNotOnThisServer
			plan.item.Message = fmt.Sprintf("Its access group %q is not on this server. Silo does not restore groups; create one with that name first.", a.Group.Name)
			return nil
		}
		group, plan.groupID = local, &local.ID
	}
	here := access.ApplyGroupPolicy(user, group)
	limit := a.Effective.access()
	limit.LibraryIDs, _ = mapLibraries(limit.LibraryIDs, u.libMap)
	for _, w := range access.WiderThan(here, limit) {
		acct.PolicyDifferences = append(acct.PolicyDifferences, PolicyDifference{Field: w.Field, Backup: w.Limit, Here: w.Policy})
	}
	plan.effective, plan.profileLimit = here, here.MaxProfiles
	if len(acct.PolicyDifferences) > 0 {
		fields := make([]string, len(acct.PolicyDifferences))
		for i, d := range acct.PolicyDifferences {
			fields[i] = d.Field
		}
		plan.item.Status, plan.item.Reason = ItemNotOnThisServer, ReasonPolicyWider
		plan.item.Message = "This server would grant the account more than the backup did: " + strings.Join(fields, ", ") + "."
		return nil
	}
	plan.item.Status, plan.item.Selectable = ItemNotOnThisServer, true
	switch {
	case a.Role == models.RoleAdmin:
		plan.item.Message = "Creates an administrator account with its password from the backup."
	case a.Group != nil:
		plan.item.Message = fmt.Sprintf("Creates the account in the access group %q, with its password from the backup.", a.Group.Name)
	default:
		plan.item.Message = "Creates the account with no access group, with its password from the backup."
	}
	return nil
}

// planProfiles matches each profile within the mapped account only.
func (u *userPlanner) planProfiles(ctx context.Context, plan *accountPlan) error {
	acct := plan.item.Account
	claimed := map[string][]int{}
	for i := range plan.doc.Profiles {
		e := &plan.doc.Profiles[i]
		pp := profilePlan{entry: e, preview: PreviewProfile{ID: e.ID, Name: e.Name, IsPrimary: e.IsPrimary, Restricted: e.LibraryRestrictionsEnabled,
			HasPIN: e.PINHash != "", DataTypes: []PreviewDataType{}}}
		pp.allowed, pp.preview.LibrariesLeftOut = mapLibraries(e.AllowedLibraryIDs, u.libMap)
		if pp.allowed == nil {
			pp.allowed = []int{}
		}
		pp.preview.NoLibraryAccess = e.LibraryRestrictionsEnabled && len(pp.allowed) == 0
		for _, t := range userDataTypes {
			raw, ok := e.Data[t.ID()]
			if !ok {
				continue
			}
			count, err := e.count(t, raw)
			if err != nil {
				return err
			}
			pp.preview.DataTypes = append(pp.preview.DataTypes, PreviewDataType{ID: t.ID(), Count: count})
		}
		match, localID, reason, message, err := u.matchProfile(ctx, plan, e)
		if err != nil {
			return err
		}
		pp.preview.Match, pp.localID = match, localID
		pp.preview.LocalID = localID
		switch {
		case reason != "":
			pp.preview.Status, pp.preview.Reason, pp.preview.Message = ItemAmbiguous, reason, message
		case localID != "":
			pp.preview.Status, pp.preview.Selectable = ItemDiffers, true
			pp.preview.Message = "The profile is on this server and keeps its PIN and access. The data you choose is merged."
			claimed[localID] = append(claimed[localID], len(plan.profiles))
		default:
			pp.preview.Status, pp.preview.Selectable = ItemNotOnThisServer, true
			pp.preview.Message = "Creates the profile with its PIN and access from the backup."
		}
		pp.preview.Target = pp.target()
		plan.profiles = append(plan.profiles, pp)
	}
	// Two backup profiles matched by name to one profile here: neither is
	// that profile for sure.
	for _, indexes := range claimed {
		if len(indexes) < 2 {
			continue
		}
		for _, i := range indexes {
			pp := &plan.profiles[i]
			if pp.preview.Match != MatchProfileName {
				continue
			}
			pp.localID = ""
			pp.preview.Status, pp.preview.Selectable, pp.preview.Reason = ItemAmbiguous, false, ReasonAmbiguousProfile
			pp.preview.LocalID, pp.preview.Target = "", TargetNew
			pp.preview.Message = fmt.Sprintf("%d profiles in the backup have this name.", len(indexes))
		}
	}
	// New profiles must fit the account's profile limit here. The restore
	// checks the count again before each one it creates, so choosing more
	// than ProfileRoom leaves the rest out.
	acct.ProfileRoom = max(plan.profileLimit-plan.existing, 0)
	for i := range plan.profiles {
		pp := &plan.profiles[i]
		if pp.preview.Selectable && pp.localID == "" && acct.ProfileRoom == 0 {
			pp.preview.Status, pp.preview.Selectable, pp.preview.Reason = ItemNotOnThisServer, false, ReasonProfileLimit
			pp.preview.Message = fmt.Sprintf("The account already has %d profiles, its limit here.", plan.profileLimit)
		}
	}
	for _, pp := range plan.profiles {
		acct.Profiles = append(acct.Profiles, pp.preview)
	}
	return nil
}

func (u *userPlanner) matchProfile(ctx context.Context, plan *accountPlan, e *ProfileEntry) (match, localID, reason, message string, err error) {
	if plan.localID == 0 {
		return MatchNew, "", "", "", nil
	}
	err = u.q.QueryRow(ctx, `
		SELECT r.profile_id FROM backup_restored_profiles r
		WHERE r.source_server_id = $1 AND r.source_account_id = $2 AND r.source_profile_id = $3 AND r.user_id = $4`,
		u.sourceID, plan.doc.Account.ID, e.ID, plan.localID).Scan(&localID)
	if err == nil {
		return MatchRestored, localID, "", "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", "", fmt.Errorf("reading restored profiles: %w", err)
	}
	if u.sameServer {
		err = u.q.QueryRow(ctx, `SELECT id FROM user_profiles WHERE user_id = $1 AND id = $2`, plan.localID, e.ID).Scan(&localID)
		if err == nil {
			return MatchProfileID, localID, "", "", nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", "", fmt.Errorf("reading profiles: %w", err)
		}
		return MatchNew, "", "", "", nil
	}
	ids, err := collectStringsArgs(ctx, u.q, `SELECT id FROM user_profiles WHERE user_id = $1 AND lower(name) = lower($2) ORDER BY id`, plan.localID, e.Name)
	if err != nil {
		return "", "", "", "", fmt.Errorf("matching profiles: %w", err)
	}
	switch len(ids) {
	case 0:
		return MatchNew, "", "", "", nil
	case 1:
		return MatchProfileName, ids[0], "", "", nil
	}
	return MatchNew, "", ReasonAmbiguousProfile, fmt.Sprintf("%d profiles of the account here have this name.", len(ids)), nil
}
