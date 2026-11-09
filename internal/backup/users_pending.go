package backup

// What restoring a matched account would change, counted for a preview
// without writing anything.

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// pendingItems resolves items for a Pending count: an item is here when
// this catalog has its content id and the profile may see it, which the
// canonical visibility query answers in one (it reads only items that
// exist). Pending asks only about entries that would change, so a fresh
// comparison asks about almost nothing. Unlike an import, it never falls
// back to provider-id matching, so an item only that would find counts as
// left out.
type pendingItems struct {
	tx      pgx.Tx
	filter  contentFilter
	visible map[string]bool
	p       *pendingCompare
	// known is which of the account's ids this server holds, and their
	// provider ids. unmatched counts ids it lacks that a restore would try
	// to match by provider id (itemResolver.find), which a preview does not.
	known     *itemResolver
	unmatched int
}

func (p *pendingItems) Resolve(ctx context.Context, id string) (string, bool, error) {
	local, err := p.ResolveAll(ctx, []string{id})
	_, ok := local[id]
	return id, ok, err
}

func (p *pendingItems) ResolveAll(ctx context.Context, ids []string) (map[string]string, error) {
	var todo []string
	for _, id := range ids {
		if _, known := p.visible[id]; !known {
			p.visible[id] = false
			todo = append(todo, id)
		}
	}
	if len(todo) > 0 {
		started := time.Now()
		ok, err := catalog.FilterAccessibleContentIDsInTransaction(ctx, p.tx, todo, p.filter.libraries, nil, p.filter.limits)
		p.p.time("visibility", started)
		if err != nil {
			for _, id := range todo {
				delete(p.visible, id)
			}
			return nil, fmt.Errorf("checking access to items: %w", err)
		}
		for id := range ok {
			p.visible[id] = ok[id]
		}
		for _, id := range todo {
			if _, matchable := matchRecord(p.known.refs[id]); !p.visible[id] && !p.known.present[id] && matchable {
				p.unmatched++
			}
		}
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if p.visible[id] {
			out[id] = id
		}
	}
	return out, nil
}

// previewUsers plans the users section for a preview in one read-only
// snapshot transaction, on one connection as every preview query before
// it, so the pending counts of every account see the same state. Accounts
// are loaded one at a time and dropped once planned: only their preview
// items are kept.
func (s *Service) previewUsers(ctx context.Context, rs *RestoreSet, serverID string, libMap map[int]int) ([]PreviewItem, error) {
	// Read before the transaction: the setting reader takes its own
	// connection.
	allowUnrated := s.opts.AllowUnratedContent != nil && s.opts.AllowUnratedContent(ctx)
	tx, err := s.opts.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("opening a snapshot of the accounts: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// The comparisons pass a profile's whole document as arrays, from a
	// handful of entries to tens of thousands. The connection caches their
	// statements, and after five runs Postgres would reuse a generic plan
	// that assumes ten entries: an index probe and heap fetch per entry,
	// several times slower on a large history than the hash or merge join a
	// plan for the actual arrays picks.
	if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan"); err != nil {
		return nil, fmt.Errorf("opening a snapshot of the accounts: %w", err)
	}
	// Each comparison is one account's few thousand entries, run one after
	// another for every account: launching parallel workers for one costs
	// more than it saves (tens of milliseconds each on a busy server).
	if _, err := tx.Exec(ctx, "SET LOCAL max_parallel_workers_per_gather = 0"); err != nil {
		return nil, fmt.Errorf("opening a snapshot of the accounts: %w", err)
	}
	u := s.newUserPlanner(rs, tx, serverID, libMap)
	u.pending = &pendingCompare{tx: tx, allowUnrated: allowUnrated, now: s.opts.Now(), phases: map[string]time.Duration{},
		planner: s.newPlanner(rs, tx), adminCollections: map[string]string{}}
	started := time.Now()
	// The next accounts load on other cores while this one is compared.
	loads := loadAhead(len(rs.Users), func(i int) int64 { return rs.Users[i].size }, func(i int) (*UserDocument, error) {
		return rs.loadUser(rs.Users[i])
	})
	defer loads.close()
	items := make([]PreviewItem, 0, len(rs.Users))
	for range rs.Users {
		item, err := u.previewAccount(ctx, loads)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	u.pending.log(ctx, len(rs.Users), time.Since(started))
	return items, nil
}

// previewAccount plans the next loaded account, decoding its large data
// documents ahead of their comparison.
func (u *userPlanner) previewAccount(ctx context.Context, loads *aheadLoads[*UserDocument]) (PreviewItem, error) {
	if err := ctx.Err(); err != nil {
		return PreviewItem{}, err
	}
	started := time.Now()
	doc, err := loads.take()
	u.pending.time("load", started)
	if err != nil {
		return PreviewItem{}, err
	}
	u.pending.decodes = startPendingDecodes(doc, runtime.GOMAXPROCS(0))
	defer u.pending.decodes.stop()
	plan, err := u.plan(ctx, doc)
	return plan.item, err
}

// log writes one line with where the users preview spent its time, so an
// operator can see what a slow preview compared.
func (p *pendingCompare) log(ctx context.Context, accounts int, total time.Duration) {
	attrs := []any{"component", "backup", "accounts", accounts, "matched_accounts", p.accounts, "matched_profiles", p.profiles,
		"total_ms", total.Milliseconds()}
	for _, name := range slices.Sorted(maps.Keys(p.phases)) {
		attrs = append(attrs, name+"_ms", p.phases[name].Milliseconds())
	}
	slog.InfoContext(ctx, "compared the backup's accounts with this server", attrs...)
}

// time adds the time since started to a phase.
func (p *pendingCompare) time(phase string, started time.Time) {
	p.phases[phase] += time.Since(started)
}

// pendingCompare is how a preview counts what a matched account's restore
// would change.
type pendingCompare struct {
	tx           pgx.Tx
	allowUnrated bool
	now          time.Time
	// phases is the time spent per phase and data type; accounts and
	// profiles count what was compared.
	phases             map[string]time.Duration
	accounts, profiles int
	// decodes decodes the large documents ahead.
	decodes *pendingDecodes
	// planner and adminCollections map the collection ids the accounts'
	// data names (collectionMap).
	planner          *planner
	adminCollections map[string]string
}

// planPending counts, for each matched profile, what merging each of its
// data types would change, and settles the account's and the profiles'
// status: a profile with nothing to merge is on this server, and an
// account whose profiles all are, with none to add, is too.
func (u *userPlanner) planPending(ctx context.Context, plan *accountPlan) error {
	p := u.pending
	p.accounts++
	started := time.Now()
	known, err := newItemResolver(ctx, p.tx, plan.doc.Items, documentItems(plan.doc))
	p.time("known_items", started)
	if err != nil {
		return err
	}
	profiles := map[string]string{}
	// created are the profiles this restore can create, by backup id.
	created := map[string]string{}
	for _, pp := range plan.profiles {
		switch {
		case pp.localID != "":
			profiles[pp.entry.ID] = pp.localID
		case pp.preview.Selectable:
			created[pp.entry.ID] = pp.entry.Name
		}
	}
	for i := range plan.profiles {
		pp := &plan.profiles[i]
		if pp.localID == "" || !pp.preview.Selectable {
			continue
		}
		total, unmatched, err := u.profilePending(ctx, plan, pp, profiles, known)
		if err != nil {
			return err
		}
		if total == 0 && unmatched > 0 {
			// The restore may match these titles by provider id and merge
			// them, so the profile stays selectable.
			pp.preview.Message = "The profile is on this server and keeps its PIN and access. Some titles in its data have other ids here; restoring matches them by provider id and merges what differs."
			continue
		}
		if total == 0 {
			if names := sharesWith(pp.entry, created); len(names) > 0 {
				// Sharing is restored from the owner's collections, so the
				// owner stays selectable for the profiles it shares with.
				pp.preview.Message = "The profile is on this server and keeps its PIN and access. Restore it together with " +
					strings.Join(names, ", ") + " to share its collections with them again."
				continue
			}
			pp.preview.Status, pp.preview.Reason, pp.preview.Selectable = ItemOnThisServer, ReasonSame, false
			pp.preview.Message = "The profile and the data the backup has for it are already on this server."
			continue
		}
		pp.preview.Message = fmt.Sprintf("The profile is on this server and keeps its PIN and access. Restoring merges %d changes into its data.", total)
	}
	acct := plan.item.Account
	acct.Profiles = acct.Profiles[:0]
	changes := false
	for _, pp := range plan.profiles {
		acct.Profiles = append(acct.Profiles, pp.preview)
		changes = changes || pp.preview.Selectable
	}
	if !changes {
		plan.item.Status, plan.item.Reason, plan.item.Selectable = ItemOnThisServer, ReasonSame, false
		plan.item.Message = "The account, its profiles, and the data the backup has for them are already on this server."
	}
	return nil
}

// sharesWith names, in order, the profiles of created (backup id to name)
// that a profile's personal collections are shared with.
func sharesWith(e *ProfileEntry, created map[string]string) []string {
	raw, ok := e.Data[userdata.Collections]
	t, found := userdata.Find(userDataTypes, userdata.Collections)
	if !ok || !found || len(created) == 0 {
		return nil
	}
	sum, err := t.Validate(raw)
	if err != nil {
		return nil
	}
	var names []string
	for _, id := range sum.Profiles {
		if name, ok := created[id]; ok && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// profilePending counts one matched profile's pending changes per data
// type and returns their total, and how many of the ids its data names this
// server lacks but a restore would try to match by provider id.
func (u *userPlanner) profilePending(ctx context.Context, plan *accountPlan, pp *profilePlan, profiles map[string]string, known *itemResolver) (total, unmatched int, err error) {
	p := u.pending
	p.profiles++
	started := time.Now()
	profile, err := pgstore.ProfileInTransaction(ctx, p.tx, plan.localID, pp.localID)
	if err != nil {
		return 0, 0, err
	}
	if profile == nil {
		return 0, 0, fmt.Errorf("profile %s is gone", pp.localID)
	}
	libraries, restricted := access.ProfileLibraries(plan.effective.LibraryIDs, profile)
	if !restricted {
		libraries = nil
	}
	p.time("profiles", started)
	items := &pendingItems{tx: p.tx, filter: contentFilter{libraries: libraries, limits: access.ProfileMaturity(profile, p.allowUnrated)},
		visible: map[string]bool{}, p: p, known: known}
	target := userdata.Target{
		Owner:      userdata.Owner{UserID: plan.localID, ProfileID: pp.localID},
		SameServer: u.sameServer, Items: items, Now: p.now.UTC(),
		Library: mapIn(u.libMap),
		Profile: func(id string) (string, bool) { local, ok := profiles[id]; return local, ok },
	}
	target.Collections = newCollectionMap(p.planner, p.tx, u.libMap, u.sameServer, p.adminCollections, plan.doc).previewLookup(target)
	for j := range pp.preview.DataTypes {
		dt := &pp.preview.DataTypes[j]
		t, ok := userdata.Find(userDataTypes, dt.ID)
		if !ok {
			continue
		}
		started := time.Now()
		var res userdata.Result
		decoded, ahead := p.decodes.take(pp.entry, dt.ID)
		err := decoded.err
		switch {
		case !ahead:
			res, err = t.Pending(ctx, p.tx, target, pp.entry.Data[dt.ID])
		case err == nil:
			res, err = decoded.compare(ctx, p.tx, target)
		}
		p.time(dt.ID, started)
		if err != nil {
			return 0, 0, fmt.Errorf("comparing %s of profile %s: %w", dt.ID, strings.TrimSpace(pp.entry.Name), err)
		}
		n := res.Changes()
		dt.Pending = &n
		total += n
	}
	return total, items.unmatched, nil
}
