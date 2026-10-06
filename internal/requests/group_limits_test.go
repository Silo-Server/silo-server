package requests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/models"
)

func intPtr(v int) *int { return &v }

// groupedService is a service whose test account belongs to access group 1.
func groupedService(store *fakeStore, user *models.User) *Service {
	svc := newTestService(store)
	if user != nil {
		svc.SetUserRepository(requestUserRepo{user: user})
	}
	svc.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: true}})
	return svc
}

func TestEffectivePolicyLayersAccountGroupServer(t *testing.T) {
	store := newFakeStore()
	store.settings.GlobalAutoApprovalEnabled = true
	store.groupLimits = map[int64]*GroupLimit{1: {GroupID: 1, LimitMode: LimitModeCustom, MaxRequests: intPtr(10), WindowDays: intPtr(30), ApprovalMode: ApprovalModeManual}}
	svc := groupedService(store, nil)

	policy, err := svc.EffectivePolicy(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MaxRequests != 10 || policy.WindowDays != 30 || policy.AutoApprove || policy.Blocked {
		t.Fatalf("group layer: %+v, want 10 per 30 days, manual", policy)
	}

	// The account's own settings win where they are set, and only there.
	store.limit = &UserLimit{UserID: 1, LimitMode: LimitModeInherit, ApprovalMode: ApprovalModeAuto}
	if policy, _ = svc.EffectivePolicy(context.Background(), 1); policy.MaxRequests != 10 || !policy.AutoApprove {
		t.Fatalf("account approval: %+v, want the group's limit and automatic approval", policy)
	}
	store.limit = &UserLimit{UserID: 1, LimitMode: LimitModeUnlimited, ApprovalMode: ApprovalModeInherit}
	if policy, _ = svc.EffectivePolicy(context.Background(), 1); !policy.Unlimited || policy.AutoApprove {
		t.Fatalf("account limit: %+v, want unlimited with the group's manual approval", policy)
	}

	// A group that inherits leaves the server's settings in place.
	store.limit = nil
	store.groupLimits[1] = &GroupLimit{GroupID: 1, LimitMode: LimitModeInherit, ApprovalMode: ApprovalModeInherit}
	if policy, _ = svc.EffectivePolicy(context.Background(), 1); policy.MaxRequests != 5 || policy.WindowDays != 7 || !policy.AutoApprove {
		t.Fatalf("inheriting group: %+v, want the server's 5 per 7 days, automatic", policy)
	}
}

func TestAdminsIgnoreTheirGroupLimits(t *testing.T) {
	store := newFakeStore()
	store.groupLimits = map[int64]*GroupLimit{1: {GroupID: 1, LimitMode: LimitModeCustom, MaxRequests: intPtr(1), WindowDays: intPtr(1), ApprovalMode: ApprovalModeManual}}
	groupID := int64(1)
	svc := groupedService(store, &models.User{ID: 1, Role: models.RoleAdmin, AccessGroupID: &groupID})

	policy, err := svc.EffectivePolicy(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MaxRequests != 5 {
		t.Fatalf("admin policy = %+v, want the server's limit: admins are never capped by a group", policy)
	}
}

// Every way to block shows up the same way: a detail page that says blocked.
func TestAccountSwitchBlocksRequestState(t *testing.T) {
	store := newFakeStore()
	groupID := int64(1)
	off := false
	svc := groupedService(store, &models.User{ID: 1, AccessGroupID: &groupID, RequestsAllowed: &off})

	policy, err := svc.EffectivePolicy(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Blocked {
		t.Fatalf("policy = %+v, want blocked by the account's requests switch", policy)
	}
	if state := requestStateFor(testViewer(1), policy, false, nil); state.Requestable || state.Reason != "blocked" {
		t.Fatalf("state = %+v, want not requestable: blocked", state)
	}
}

func TestGroupLimitAdministration(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	admin := Viewer{UserID: 1, ProfileID: "p", IsAdmin: true}

	if _, err := svc.GetGroupLimit(context.Background(), testViewer(1), 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.GetGroupLimit(context.Background(), admin, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group: err = %v, want ErrNotFound", err)
	}
	fresh, err := svc.GetGroupLimit(context.Background(), admin, 1)
	if err != nil || fresh.LimitMode != LimitModeInherit || fresh.ApprovalMode != ApprovalModeInherit || fresh.Revision != 0 {
		t.Fatalf("unsaved group limit = %+v, %v; want inherit at revision zero", fresh, err)
	}

	var verr *ValidationError
	if _, err := svc.UpsertGroupLimitConditional(context.Background(), admin, GroupLimit{GroupID: 1, LimitMode: LimitModeCustom}, 0); !errors.As(err, &verr) {
		t.Fatalf("custom without numbers: err = %v, want a field error", err)
	}
	for _, blocked := range []GroupLimit{{GroupID: 1, LimitMode: LimitModeBlocked}, {GroupID: 1, ApprovalMode: ApprovalModeBlocked}} {
		if _, err := svc.UpsertGroupLimitConditional(context.Background(), admin, blocked, 0); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%+v: err = %v, want ErrInvalidInput: groups block with their requests switch", blocked, err)
		}
	}
	saved, err := svc.UpsertGroupLimitConditional(context.Background(), admin, GroupLimit{GroupID: 1, LimitMode: LimitModeUnlimited, MaxRequests: intPtr(3), ApprovalMode: ApprovalModeAuto}, 0)
	if err != nil || saved.MaxRequests != nil || saved.ApprovalMode != ApprovalModeAuto {
		t.Fatalf("saved = %+v, %v; want unlimited with no count, automatic", saved, err)
	}
	if _, err := svc.UpsertGroupLimitConditional(context.Background(), admin, GroupLimit{GroupID: 1}, 0); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale save: err = %v, want ErrStaleRevision", err)
	}
}

// The migration moves the old "blocked" limit and approval modes onto the
// account's requests switch, and group limits save with revisions.
func TestRequestAccessMigrationDatabase(t *testing.T) {
	matches, err := filepath.Glob("../../migrations/sql/*_request_group_limits.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("find migration: %v %v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	up = up[strings.Index(up, "-- +goose Up"):strings.Index(up, "-- +goose Down")]

	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"users", "access_groups", "request_user_limits"} {
		exec(`CREATE TABLE ` + table + ` (LIKE public.` + table + ` INCLUDING ALL)`)
	}
	exec(`INSERT INTO users (id, role, username) VALUES (1, 'user', 'a'), (2, 'user', 'b'), (3, 'user', 'c')`)
	exec(`INSERT INTO request_user_limits (user_id, limit_mode, approval_mode) VALUES (1, 'blocked', 'auto'), (2, 'custom', 'blocked'), (3, 'unlimited', 'manual')`)
	exec(`UPDATE request_user_limits SET max_requests = 4, window_days = 2 WHERE user_id = 2`)
	exec(up)

	rows, err := pool.Query(ctx, `SELECT u.id, coalesce(u.requests_allowed, true), l.limit_mode, l.approval_mode
		FROM users u JOIN request_user_limits l ON l.user_id = u.id ORDER BY u.id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id int
		var allowed bool
		var limitMode, approvalMode string
		if err := rows.Scan(&id, &allowed, &limitMode, &approvalMode); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{limitMode, approvalMode, map[bool]string{true: "allowed", false: "blocked"}[allowed]}, "/"))
	}
	rows.Close()
	want := []string{"inherit/auto/blocked", "custom/inherit/blocked", "unlimited/manual/allowed"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("after migration: %v, want %v", got, want)
	}

	var groupID int64
	if err := pool.QueryRow(ctx, `INSERT INTO access_groups (name, configuration_revision) VALUES ('Kids', 1) RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertGroupLimitConditional(ctx, GroupLimit{GroupID: groupID + 100, LimitMode: LimitModeInherit, ApprovalMode: ApprovalModeInherit}, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group: err = %v, want ErrNotFound", err)
	}
	saved, err := repo.UpsertGroupLimitConditional(ctx, GroupLimit{GroupID: groupID, LimitMode: LimitModeCustom, MaxRequests: intPtr(2), WindowDays: intPtr(14), ApprovalMode: ApprovalModeManual}, 0)
	if err != nil || saved.Revision == 0 || *saved.MaxRequests != 2 {
		t.Fatalf("first save = %+v, %v", saved, err)
	}
	if _, err := repo.UpsertGroupLimitConditional(ctx, GroupLimit{GroupID: groupID, LimitMode: LimitModeInherit, ApprovalMode: ApprovalModeInherit}, 0); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("second first-save: err = %v, want ErrStaleRevision", err)
	}
	again, err := repo.UpsertGroupLimitConditional(ctx, GroupLimit{GroupID: groupID, LimitMode: LimitModeUnlimited, ApprovalMode: ApprovalModeAuto}, saved.Revision)
	if err != nil || again.Revision == saved.Revision || again.LimitMode != LimitModeUnlimited {
		t.Fatalf("second save = %+v, %v", again, err)
	}
	if read, err := repo.GetGroupLimit(ctx, groupID); err != nil || read.Revision != again.Revision {
		t.Fatalf("read = %+v, %v", read, err)
	}
}

// A decline or a failure gives the request's quota slot back; a
// cancellation does not, so a request-and-withdraw loop cannot repeat forever.
func TestQuotaRefundsDeclinedAndFailedRequestsDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	for i, outcome := range []Outcome{OutcomeActive, OutcomeDeclined, OutcomeCancelled, OutcomeFailed, OutcomeActive} {
		id := "q" + string(rune('a'+i))
		insertLifecycleRequest(t, repo, id, 7, 300+i, StatusPending)
		if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = $2 WHERE id = $1`, id, outcome); err != nil {
			t.Fatal(err)
		}
	}
	used, err := repo.CountUserRequestsSince(ctx, 7, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if used != 3 {
		t.Fatalf("used = %d, want 3: two active and one withdrawn", used)
	}
	create := func(id string, tmdbID, limit int) error {
		_, err := repo.CreateRequest(ctx, CreateRequestRecord{
			ID: id, Input: CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: tmdbID, Title: "New"},
			Status: StatusPending, Outcome: OutcomeActive, Requester: Viewer{UserID: 7, ProfileID: "profile"},
			Quota: &QuotaCheck{UserID: 7, WindowStart: time.Now().Add(-time.Hour), MaxRequests: limit},
		})
		return err
	}
	if err := create("q-at-limit", 398, 3); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("at the limit: err = %v, want ErrQuotaExceeded", err)
	}
	if err := create("q-new", 399, 4); err != nil {
		t.Fatalf("under the limit: %v", err)
	}
}

// Closing a failed request from the admin queue keeps the refund its failure
// gave: cleaning up the failed view must not use up the requester's quota. A
// request the owner withdraws while it backs off after a failed attempt was
// never failed, so it keeps counting.
func TestQuotaKeepsRefundWhenAdminClosesFailedRequestDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	admin := Viewer{UserID: 1, ProfileID: "admin", IsAdmin: true}
	insertLifecycleRequest(t, repo, "closed", 7, 410, StatusApproved)
	if _, err := repo.SetOutcome(ctx, "closed", StateGuard{Statuses: []Status{StatusApproved}, Outcomes: []Outcome{OutcomeActive}}, OutcomeFailed, Viewer{}, "Radarr rejected the movie"); err != nil {
		t.Fatal(err)
	}
	insertLifecycleRequest(t, repo, "withdrawn", 7, 411, StatusPending)
	if _, err := repo.SetOutcome(ctx, "withdrawn", guardWithdrawable, OutcomeCancelled, Viewer{UserID: 7, ProfileID: "profile"}, ""); err != nil {
		t.Fatal(err)
	}
	insertLifecycleRequest(t, repo, "deferred", 7, 412, StatusApproved)
	claimed, ok, err := repo.ClaimSubmission(ctx, "deferred", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim deferred: ok=%v err=%v", ok, err)
	}
	if _, err := repo.DeferSubmission(ctx, "deferred", *claimed.SubmitLeaseUntil, time.Hour, "router unreachable"); err != nil {
		t.Fatal(err)
	}
	deferredWithdrawn, err := repo.SetOutcome(ctx, "deferred", guardWithdrawable, OutcomeCancelled, Viewer{UserID: 7, ProfileID: "profile"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if deferredWithdrawn.LastError != "" {
		t.Fatalf("withdrawn deferred request last_error = %q, want it cleared", deferredWithdrawn.LastError)
	}
	since := time.Now().Add(-time.Hour)
	before, err := repo.CountUserRequestsSince(ctx, 7, since)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := repo.SetOutcome(ctx, "closed", guardFailed, OutcomeCancelled, admin, "not retrying")
	if err != nil {
		t.Fatal(err)
	}
	if closed.Outcome != OutcomeCancelled {
		t.Fatalf("closed outcome = %q, want %q", closed.Outcome, OutcomeCancelled)
	}
	after, err := repo.CountUserRequestsSince(ctx, 7, since)
	if err != nil {
		t.Fatal(err)
	}
	if before != 2 || after != 2 {
		t.Fatalf("used before/after closing = %d/%d, want 2/2: only the two withdrawals count", before, after)
	}
}

func TestGroupSwitchAndLegacyBlockBothBlock(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	svc.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: false}})
	if policy, err := svc.EffectivePolicy(context.Background(), 1); err != nil || !policy.Blocked {
		t.Fatalf("group switch off: %+v, %v; want blocked", policy, err)
	}

	store.groupLimits = map[int64]*GroupLimit{1: {GroupID: 1, LimitMode: LimitModeUnlimited, ApprovalMode: ApprovalModeAuto}}
	store.limit = &UserLimit{UserID: 1, LimitMode: LimitModeBlocked, ApprovalMode: ApprovalModeInherit}
	svc = groupedService(store, nil)
	if policy, err := svc.EffectivePolicy(context.Background(), 1); err != nil || !policy.Blocked {
		t.Fatalf("legacy blocked account over an unlimited group: %+v, %v; want blocked", policy, err)
	}
}

// A detail page and its recommendations resolve the viewer's policy once.
func TestDetailResolvesThePolicyOnce(t *testing.T) {
	store := newFakeStore()
	detail := &tmdb.MediaDetail{MediaType: "movie", ID: 550, Title: "Fight Club",
		Recommendations: []tmdb.MediaResult{{ID: 551, MediaType: "movie", Title: "Other"}}}
	svc := NewService(store, &fakeTMDBClient{detail: detail}, &fakePresence{})
	svc.SetUserRepository(requestUserRepo{})

	if _, err := svc.GetDetail(context.Background(), testViewer(1), MediaTypeMovie, 550); err != nil {
		t.Fatal(err)
	}
	if store.userLimitReads != 1 {
		t.Fatalf("policy resolutions = %d, want 1 for the detail and its recommendations", store.userLimitReads)
	}
}
