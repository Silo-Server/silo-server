package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// accountAuthority is what an account may do: its rank (the Owner over
// admin over user), whether it can sign in, break-glass access, its
// effective account policy, and its profiles' restrictions by profile id.
type accountAuthority struct {
	rank       int
	enabled    bool
	breakGlass bool
	policy     access.EffectiveUserPolicy
	// requests is its request policy (requests.LayeredPolicy): whether and
	// how often it may request titles, and whether they auto-approve.
	requests requests.EffectivePolicy
	// libraries are the libraries it can see: its policy's, or, when that
	// names none (every library), every enabled library of its server, so
	// a server with more libraries counts as wider.
	libraries []int
	profiles  map[string]profileLimits
}

// profileLimits are a profile's protections and restrictions: its PIN,
// whether it manages the household (the primary profile), a child
// profile, its rating and advisory-age ceilings, its library restriction,
// its playback-quality ceiling, and the titles allowed above its ceilings.
type profileLimits struct {
	pin                bool
	primary            bool
	child              bool
	ceiling            string
	advisoryAge        *int
	requireAdvisoryAge bool
	restricted         bool
	libraries          []int
	quality            string
	// allowedTitles are the titles the profile may watch above its
	// ceilings: its title allowances and the titles of its live-allowed
	// collections, resolved as the access scope resolves them.
	allowedTitles []string
	// allowedCollections are the collections it is allowed live. They are
	// compared on their own: a collection empty today resolves no title, yet
	// allows whatever is added to it later.
	allowedCollections []string
}

// readAuthority reads the authority of the accounts ids in tx's database;
// an account that is not there is left out.
func readAuthority(ctx context.Context, tx pgx.Tx, ids []int) (map[int]accountAuthority, error) {
	out := map[int]accountAuthority{}
	if len(ids) == 0 {
		return out, nil
	}
	var enabled []int
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(id ORDER BY id), '{}') FROM media_folders WHERE enabled`).Scan(&enabled); err != nil {
		return nil, fmt.Errorf("reading libraries: %w", err)
	}
	for _, id := range ids {
		user, err := auth.UserInTransaction(ctx, tx, id)
		if auth.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading account %d: %w", id, err)
		}
		var group *access.GroupPolicy
		if access.GroupApplies(user) {
			if group, err = access.GroupPolicyInTransaction(ctx, tx, id); err != nil {
				return nil, fmt.Errorf("reading account %d's access group: %w", id, err)
			}
		}
		a := accountAuthority{rank: roleRank(user), enabled: user.Enabled, breakGlass: user.BreakGlass,
			policy: access.ApplyGroupPolicy(user, group)}
		a.libraries = enabled
		if a.policy.LibraryIDs != nil {
			a.libraries = a.policy.LibraryIDs
		}
		if a.requests, err = requestPolicy(ctx, tx, user, a.policy.RequestsAllowed); err != nil {
			return nil, fmt.Errorf("reading account %d's request policy: %w", id, err)
		}
		if a.profiles, err = readProfileLimits(ctx, tx, id); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, nil
}

func roleRank(user *models.User) int {
	switch {
	case user.IsOwner:
		return 2
	case user.Role == models.RoleAdmin:
		return 1
	}
	return 0
}

func readProfileLimits(ctx context.Context, tx pgx.Tx, userID int) (map[string]profileLimits, error) {
	allowedTitles, allowedCollections, err := allowanceSQL(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id, coalesce(p.pin_hash, '') <> '', p.is_primary, p.is_child, coalesce(p.max_content_rating, ''), p.max_advisory_age::int,
			p.require_advisory_age, p.library_restrictions_enabled,
			coalesce((SELECT array_agg(a.library_id ORDER BY a.library_id) FROM user_profile_allowed_libraries a WHERE a.user_id = p.user_id AND a.profile_id = p.id), '{}'),
			coalesce(p.max_playback_quality, ''),
			`+allowedTitles+`, `+allowedCollections+`
		FROM user_profiles p WHERE p.user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("reading account %d's profiles: %w", userID, err)
	}
	defer rows.Close()
	out := map[string]profileLimits{}
	for rows.Next() {
		var id string
		var p profileLimits
		if err := rows.Scan(&id, &p.pin, &p.primary, &p.child, &p.ceiling, &p.advisoryAge, &p.requireAdvisoryAge, &p.restricted, &p.libraries,
			&p.quality, &p.allowedTitles, &p.allowedCollections); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// allowanceSQL is the select expressions for a profile row p's allowed
// titles and its allowed collections. A backup taken before title allowances
// existed has neither table, and its profiles allow nothing above their
// limits.
func allowanceSQL(ctx context.Context, q queryer) (titles, collections string, err error) {
	for _, table := range []string{"profile_title_allowances", "profile_collection_allowances"} {
		present, err := tableExists(ctx, q, table)
		if err != nil {
			return "", "", err
		}
		if !present {
			return "'{}'::text[]", "'{}'::text[]", nil
		}
	}
	return "ARRAY(" + pgstore.ProfileAllowedTitleIDsSQL("p.user_id", "p.id") + ")",
		"ARRAY(SELECT ca.collection_id FROM profile_collection_allowances ca WHERE ca.user_id = p.user_id AND ca.profile_id = p.id ORDER BY 1)", nil
}

// widerThan reports whether restored grants anything live does not: a
// higher rank, sign-in or break-glass access live lacks, a wider account
// policy (access.WiderThan), a library it can see that live cannot, a
// household manager (a primary profile) without a PIN where every live one
// has one, a profile only the restored data has, or a profile on both
// sides with a protection or restriction lifted or loosened.
func (restored accountAuthority) widerThan(live accountAuthority) bool {
	if restored.rank > live.rank || restored.enabled && !live.enabled || restored.breakGlass && !live.breakGlass ||
		len(access.WiderThan(restored.policy, live.policy)) > 0 || requestsWider(restored.requests, live.requests) ||
		restored.openManager() && !live.openManager() {
		return true
	}
	for _, id := range restored.libraries {
		if !slices.Contains(live.libraries, id) {
			return true
		}
	}
	// A profile only the restored data has (deleted here since) is one a
	// live credential's holder could select, with whatever protection it
	// had then: wider.
	for id, r := range restored.profiles {
		if l, ok := live.profiles[id]; !ok || r.looserThan(l) {
			return true
		}
	}
	return false
}

// openManager reports whether a primary profile has no PIN: whoever acts
// as it manages the household (loosens or deletes the other profiles)
// without one.
func (a accountAuthority) openManager() bool {
	for _, p := range a.profiles {
		if p.primary && !p.pin {
			return true
		}
	}
	return false
}

func (p profileLimits) looserThan(live profileLimits) bool {
	if live.pin && !p.pin || p.primary && !live.primary || live.child && !p.child || live.requireAdvisoryAge && !p.requireAdvisoryAge {
		return true
	}
	if ceiling := access.NormalizePlaybackQuality(live.quality); ceiling != "" {
		if q := access.NormalizePlaybackQuality(p.quality); q == "" || access.CompareQuality(q, ceiling) > 0 {
			return true
		}
	}
	if access.HasCeiling(live.ceiling) && p.ceiling != live.ceiling {
		liveAge, ok := access.AgeForCeiling(live.ceiling)
		age, known := access.AgeForCeiling(p.ceiling)
		if !access.HasCeiling(p.ceiling) || !ok || !known || *age > *liveAge {
			return true
		}
	}
	if live.advisoryAge != nil && (p.advisoryAge == nil || *p.advisoryAge > *live.advisoryAge) {
		return true
	}
	if live.restricted {
		if !p.restricted {
			return true
		}
		for _, id := range p.libraries {
			if !slices.Contains(live.libraries, id) {
				return true
			}
		}
	}
	for _, id := range p.allowedTitles {
		if !slices.Contains(live.allowedTitles, id) {
			return true
		}
	}
	for _, id := range p.allowedCollections {
		if !slices.Contains(live.allowedCollections, id) {
			return true
		}
	}
	return false
}

// credentialOwners lists the accounts the live rows of the carried tables
// belong to, by their owner columns.
func credentialOwners(tables []secretTable, live []string) ([]int, error) {
	var ids []int
	for i, t := range tables {
		if len(t.carry.owners) == 0 {
			continue
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(live[i]), &rows); err != nil {
			return nil, fmt.Errorf("%s: %w", t.name, err)
		}
		for _, row := range rows {
			for _, column := range t.carry.owners {
				if id, ok := row[column].(float64); ok {
					ids = append(ids, int(id))
				}
			}
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// inviteeGrant is what an account created from an invitation or an invite
// code gets: a role, the invitation's libraries, and an access group (the
// default group when none is named, unless the role is admin).
type inviteeGrant struct {
	role       string
	libraryIDs []int
	groupID    *int64
}

// grantOf reads the grant of a carried row of table, if it grants one.
func grantOf(table string, row map[string]any) (inviteeGrant, bool) {
	switch table {
	case "invite_codes":
		return inviteeGrant{role: models.RoleUser}, true
	case "plugin_auth_bindings":
		// An account a provider creates is a user in the default group
		// (auth.AccountResolver); one the provider makes an admin is the
		// provider's decision, on both servers alike.
		on, _ := row[columnAutoProvision].(bool)
		return inviteeGrant{role: models.RoleUser}, on
	case "invitations":
		g := inviteeGrant{role: models.RoleUser}
		if role, ok := row["role"].(string); ok {
			g.role = role
		}
		if ids, ok := row["library_ids"].([]any); ok {
			g.libraryIDs = []int{}
			for _, id := range ids {
				if n, ok := id.(float64); ok {
					g.libraryIDs = append(g.libraryIDs, int(n))
				}
			}
		}
		if id, ok := row["access_group_id"].(float64); ok {
			g.groupID = new(int64(id))
		}
		return g, true
	}
	return inviteeGrant{}, false
}

// grantPolicy is what an account created from a grant would get: its
// effective access policy and its request policy.
type grantPolicy struct {
	access   access.EffectiveUserPolicy
	requests requests.EffectivePolicy
}

// widerThan reports whether the policy grants anything live does not.
func (p grantPolicy) widerThan(live grantPolicy) bool {
	return len(access.WiderThan(p.access, live.access)) > 0 || requestsWider(p.requests, live.requests)
}

// inviteePolicy is the policy an account created now from g would get in
// tx's database, as account creation resolves it, its request policy
// included.
func inviteePolicy(ctx context.Context, tx pgx.Tx, g inviteeGrant) (grantPolicy, error) {
	user, policy, err := inviteeAccess(ctx, tx, g)
	if err != nil {
		return grantPolicy{}, err
	}
	reqs, err := requestPolicy(ctx, tx, user, policy.RequestsAllowed)
	return grantPolicy{access: policy, requests: reqs}, err
}

// inviteeAccess is the account an account created now from g would be,
// and its effective access policy.
func inviteeAccess(ctx context.Context, tx pgx.Tx, g inviteeGrant) (*models.User, access.EffectiveUserPolicy, error) {
	// The permissions account creation stores (auth.NewUserPermissions), so
	// a group's allowed permissions mask the same list here as there.
	permissions, err := auth.NewUserPermissions(models.CreateUserInput{Role: g.role, LibraryIDs: g.libraryIDs, AccessGroupID: g.groupID})
	if err != nil {
		return nil, access.EffectiveUserPolicy{}, err
	}
	user := &models.User{Role: g.role, LibraryIDs: g.libraryIDs, AccessGroupID: g.groupID, Permissions: permissions}
	if user.AccessGroupID == nil && g.role != models.RoleAdmin {
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM access_groups WHERE is_default`).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, access.EffectiveUserPolicy{}, fmt.Errorf("reading the default access group: %w", err)
		}
		if err == nil {
			user.AccessGroupID = &id
		}
	}
	if !access.GroupApplies(user) {
		return user, access.ApplyGroupPolicy(user, nil), nil
	}
	group, err := access.ReadGroupInTransaction(ctx, tx, *user.AccessGroupID)
	if errors.Is(err, access.ErrGroupNotFound) {
		// The carry drops an invitation whose group is gone.
		return user, access.ApplyGroupPolicy(user, nil), nil
	}
	if err != nil {
		return nil, access.EffectiveUserPolicy{}, fmt.Errorf("reading access group %d: %w", *user.AccessGroupID, err)
	}
	policy := group.Policy()
	return user, access.ApplyGroupPolicy(user, &policy), nil
}

// grantKey names a carried row that grants an account: its table and id.
type grantKey struct {
	table string
	id    int64
}

// inviteePolicies reads, in tx's database, the policy each carried row
// that grants an account would give it.
func inviteePolicies(ctx context.Context, tx pgx.Tx, tables []secretTable, live []string) (map[grantKey]grantPolicy, error) {
	out := map[grantKey]grantPolicy{}
	for i, t := range tables {
		var rows []map[string]any
		if err := json.Unmarshal([]byte(live[i]), &rows); err != nil {
			return nil, fmt.Errorf("%s: %w", t.name, err)
		}
		for _, row := range rows {
			g, ok := grantOf(t.name, row)
			id, hasID := row["id"].(float64)
			if !ok || !hasID {
				continue
			}
			policy, err := inviteePolicy(ctx, tx, g)
			if err != nil {
				return nil, err
			}
			out[grantKey{table: t.name, id: int64(id)}] = policy
		}
	}
	return out, nil
}

// requestPolicy reads, in tx's database, the request policy of user, whose
// requests switch is allowed: the server's request settings, its access
// group's limits, and its own (requests.LayeredPolicy). A table an older
// backup does not have counts as unset.
func requestPolicy(ctx context.Context, tx pgx.Tx, user *models.User, allowed bool) (requests.EffectivePolicy, error) {
	settings := requests.Settings{GlobalMaxRequests: 5, GlobalWindowDays: 7}
	err := optionalRow(ctx, tx, `SELECT requests_enabled, global_max_requests, global_window_days, global_auto_approval_enabled
		FROM request_settings WHERE id = true`, nil, &settings.RequestsEnabled, &settings.GlobalMaxRequests, &settings.GlobalWindowDays, &settings.GlobalAutoApprovalEnabled)
	if err != nil {
		return requests.EffectivePolicy{}, err
	}
	var own *requests.UserLimit
	var limit requests.UserLimit
	if err := optionalRow(ctx, tx, `SELECT limit_mode, max_requests, window_days, approval_mode FROM request_user_limits WHERE user_id = $1`,
		[]any{user.ID}, &limit.LimitMode, &limit.MaxRequests, &limit.WindowDays, &limit.ApprovalMode); err != nil {
		return requests.EffectivePolicy{}, err
	}
	if limit.LimitMode != "" || limit.ApprovalMode != "" {
		own = &limit
	}
	var group *requests.GroupLimit
	if access.GroupApplies(user) {
		var g requests.GroupLimit
		if err := optionalRow(ctx, tx, `SELECT limit_mode, max_requests, window_days, approval_mode FROM request_group_limits WHERE group_id = $1`,
			[]any{*user.AccessGroupID}, &g.LimitMode, &g.MaxRequests, &g.WindowDays, &g.ApprovalMode); err != nil {
			return requests.EffectivePolicy{}, err
		}
		if g.LimitMode != "" || g.ApprovalMode != "" {
			group = &g
		}
	}
	return requests.LayeredPolicy(settings, allowed, group, own), nil
}

// optionalRow scans one row of sql into dest in a savepoint: no row, or a
// table or column the database does not have, leaves dest as it is.
func optionalRow(ctx context.Context, tx pgx.Tx, sql string, args []any, dest ...any) error {
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		return sp.QueryRow(ctx, sql, args...).Scan(dest...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if missingInSchema(err) {
		return nil
	}
	return err
}

// requestsWider reports whether a restored request policy lets the account
// request more than live does: at all, without limit, more titles or over
// a shorter window, or with automatic approval.
func requestsWider(restored, live requests.EffectivePolicy) bool {
	canRequest := func(p requests.EffectivePolicy) bool { return p.RequestsEnabled && !p.Blocked }
	switch {
	case canRequest(restored) && !canRequest(live):
		return true
	case !canRequest(restored):
		return false
	case restored.AutoApprove && !live.AutoApprove:
		return true
	case live.Unlimited:
		return false
	case restored.Unlimited:
		return true
	}
	return restored.MaxRequests > live.MaxRequests || restored.WindowDays < live.WindowDays
}
