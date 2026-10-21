package nodepool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthPersistsEncodingCapacityWithoutResourceSample(t *testing.T) {
	url := newStatsHealthNode(t, `{"status":"ok","active_jobs":9,"encoding_capacity":{"max_jobs":4,"active_jobs":2,"available_jobs":2}}`)
	healthy, jobs, _, _, stats, _ := CheckNode(context.Background(), &Node{URL: url})
	if !healthy || jobs != 9 {
		t.Fatalf("health = %v, jobs = %d", healthy, jobs)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(stats, &decoded); err != nil {
		t.Fatalf("capacity report was not persisted: %v (%s)", err, stats)
	}
	if string(decoded["encoding_capacity"]) != `{"max_jobs":4,"active_jobs":2,"available_jobs":2}` {
		t.Fatalf("encoding capacity changed: %s", stats)
	}
}

func TestReportedEncodingCeilingFitsDatabaseInteger(t *testing.T) {
	if capacity := reportedEncodingCapacity(&Node{LastStats: []byte(`{"encoding_capacity":{"max_jobs":2147483647,"active_jobs":0}}`)}); capacity == nil {
		t.Fatal("maximum valid ceiling rejected")
	}
	if capacity := reportedEncodingCapacity(&Node{LastStats: []byte(`{"encoding_capacity":{"max_jobs":2147483648,"active_jobs":0}}`)}); capacity != nil {
		t.Fatal("ceiling that overflows Postgres integer accepted")
	}
}

func TestPlannerHonorsWorkerEncodingCeiling(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stats       string
		jobs        int
		customerCap *int
		wantNode    bool
	}{
		{"full worker with unlimited customer cap", `{"encoding_capacity":{"max_jobs":2,"active_jobs":2,"available_jobs":0}}`, 0, nil, false},
		{"worker ceiling lower than customer cap", `{"encoding_capacity":{"max_jobs":2,"active_jobs":2,"available_jobs":0}}`, 0, intPtr(20), false},
		{"customer ceiling lower than worker", `{"encoding_capacity":{"max_jobs":4,"active_jobs":1,"available_jobs":3}}`, 1, intPtr(1), false},
		{"customer ceiling uses live encoders instead of retained sessions", `{"encoding_capacity":{"max_jobs":4,"active_jobs":0,"available_jobs":4}}`, 9, intPtr(1), true},
		{"customer ceiling counts encoders without playback sessions", `{"encoding_capacity":{"max_jobs":4,"active_jobs":1,"available_jobs":3}}`, 0, intPtr(1), false},
		{"finished encoders release worker capacity", `{"encoding_capacity":{"max_jobs":2,"active_jobs":0,"available_jobs":2}}`, 8, nil, true},
		{"worker unlimited", `{"encoding_capacity":{"max_jobs":0,"active_jobs":8,"available_jobs":null}}`, 8, nil, true},
		{"unlimited worker preserves customer session ceiling", `{"encoding_capacity":{"max_jobs":0,"active_jobs":0,"available_jobs":null}}`, 2, intPtr(2), false},
		{"unlimited worker uses session count for customer ceiling", `{"encoding_capacity":{"max_jobs":0,"active_jobs":8,"available_jobs":null}}`, 0, intPtr(1), true},
		{"zero customer ceiling", "", 0, intPtr(0), false},
		{"old worker", "", 8, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := transcodeNode(1, "http://tc-1", nil, tc.jobs)
			node.LastStats = json.RawMessage(tc.stats)
			node.MaxJobs = tc.customerCap
			f := newFixture(nil, []*Node{node})
			got := f.planner.PlanSession("s1", "", true, 0).TranscodeNode
			if (got != nil) != tc.wantNode {
				t.Fatalf("selected node = %v, want node = %v", got, tc.wantNode)
			}
		})
	}
}

func TestPlannerLowerCustomerCeilingIncludesWorkerReservations(t *testing.T) {
	node := transcodeNode(1, "http://tc-1", nil, 9)
	node.MaxJobs = intPtr(1)
	node.LastStats = json.RawMessage(`{"encoding_capacity":{"max_jobs":4,"active_jobs":0,"available_jobs":4}}`)
	f := newFixture(nil, []*Node{node})
	if f.planner.PlanSession("s1", "", true, 0).TranscodeNode == nil {
		t.Fatal("retained sessions blocked an idle encoder below the customer ceiling")
	}
	if f.planner.PlanSession("s2", "", true, 0).TranscodeNode != nil {
		t.Fatal("pending encoder reservation bypassed the lower customer ceiling")
	}
	f.planner.ReleaseSession("s1")
	if f.planner.PlanSession("s3", "", true, 0).TranscodeNode == nil {
		t.Fatal("released encoder reservation still consumes customer capacity")
	}
}

func BenchmarkPlannerUnderCap(b *testing.B) {
	for _, tc := range []struct {
		name        string
		stats       string
		customerCap *int
	}{
		{"unlimited", "", nil},
		{"unlimited_worker", `{"encoding_capacity":{"max_jobs":0,"active_jobs":9}}`, nil},
		{"customer_ceiling", "", intPtr(20_000)},
		{"worker_ceiling", `{"encoding_capacity":{"max_jobs":20000,"active_jobs":2}}`, intPtr(20_000)},
	} {
		for _, count := range []int{0, 10_000} {
			b.Run(tc.name+"/reservations="+strconv.Itoa(count), func(b *testing.B) {
				node := transcodeNode(1, "http://tc-1", nil, 9)
				node.MaxJobs = tc.customerCap
				node.LastStats = json.RawMessage(tc.stats)
				f := newFixture(nil, []*Node{node})
				for i := range count {
					url := node.URL
					if i%2 == 0 {
						url = "http://tc-other"
					}
					f.planner.reserved[strconv.Itoa(i)] = &reservation{transcodeURL: url, createdAt: f.now}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if !f.planner.underCap(node, f.now) {
						b.Fatal("node below its ceilings was refused")
					}
				}
			})
		}
	}
}

func TestPlannerWorkerCeilingIncludesProvisionalReservations(t *testing.T) {
	node := transcodeNode(1, "http://tc-1", nil, 0)
	node.LastStats = json.RawMessage(`{"encoding_capacity":{"max_jobs":1,"active_jobs":0,"available_jobs":1}}`)
	f := newFixture(nil, []*Node{node})
	if f.planner.PlanSession("s1", "", true, 0).TranscodeNode == nil {
		t.Fatal("first encoder was refused")
	}
	if f.planner.PlanSession("s2", "", true, 0).TranscodeNode != nil {
		t.Fatal("second encoder bypassed the worker ceiling through a stale health report")
	}
	f.planner.ReleaseSession("s1")
	if f.planner.PlanSession("s3", "", true, 0).TranscodeNode == nil {
		t.Fatal("released reservation still consumes capacity")
	}
	// A report newer than a reservation replaces its provisional accounting.
	checked := time.Now().Add(time.Second)
	node.LastHealthCheck = &checked
	node.LastStats = json.RawMessage(`{"encoding_capacity":{"max_jobs":1,"active_jobs":1,"available_jobs":0}}`)
	if f.planner.PlanSession("s4", "", true, 0).TranscodeNode != nil {
		t.Fatal("new health report ignored the running encoder")
	}
	node.LastStats = json.RawMessage(`{"encoding_capacity":{"max_jobs":1,"active_jobs":0,"available_jobs":1}}`)
	if f.planner.PlanSession("s5", "", true, 0).TranscodeNode == nil {
		t.Fatal("new idle report double counted an old reservation")
	}
}

func TestPoolLowersCustomerCapToReportedWorkerCeiling(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured *int
		worker     int
		want       *int
	}{
		{"higher", intPtr(20), 4, intPtr(4)}, {"unlimited", nil, 4, intPtr(4)},
		{"lower", intPtr(2), 4, intPtr(2)}, {"default remains unlimited", nil, 0, nil},
		{"default preserves existing finite cap", intPtr(20), 0, intPtr(20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := transcodeNode(1, "http://tc-1", nil, 0)
			node.MaxJobs = tc.configured
			f := newFixture(nil, []*Node{node})
			stats, _ := json.Marshal(map[string]any{"encoding_capacity": map[string]any{"max_jobs": tc.worker, "active_jobs": 0}})
			f.transcodes.ApplyHealth(node.ID, node.URL, true, 0, 0, "", stats, nil, time.Now())
			got := f.transcodes.Nodes()[0].MaxJobs
			if got == nil && tc.want != nil || got != nil && (tc.want == nil || *got != *tc.want) {
				t.Fatalf("core cap = %v, want %v", got, tc.want)
			}
			if node.MaxJobs != tc.configured {
				t.Fatal("health mutated a published node")
			}
		})
	}
}

func TestReconciledWorkerCeilingUsesLiveEncoders(t *testing.T) {
	node := transcodeNode(1, "http://tc-1", nil, 9)
	f := newFixture(nil, []*Node{node})
	f.transcodes.ApplyHealth(node.ID, node.URL, true, 9, 0, "", []byte(`{"encoding_capacity":{"max_jobs":4,"active_jobs":0,"available_jobs":4}}`), nil, time.Now())
	if f.planner.PlanSession("new-encode", "", true, 0).TranscodeNode == nil {
		t.Fatal("completed encoders retained capacity after the stored cap was reconciled")
	}
}

func TestReportedEncodingCeilingPreservesDrainState(t *testing.T) {
	node := transcodeNode(1, "http://tc-1", nil, 0)
	node.MaxJobs = intPtr(20)
	f := newFixture(nil, []*Node{node})
	f.transcodes.ApplyHealthReport(node.ID, node.URL, HealthReport{
		Healthy: true, DrainState: DrainStateDraining,
		LastStats: []byte(`{"encoding_capacity":{"max_jobs":4,"active_jobs":0}}`),
	}, time.Now())
	got := f.transcodes.Nodes()[0]
	if got.DrainState != DrainStateDraining || got.MaxJobs == nil || *got.MaxJobs != 4 {
		t.Fatalf("draining capacity report lost drain state or ceiling: %+v", got)
	}
	if f.planner.PlanSession("new-encode", "", true, 0).TranscodeNode != nil {
		t.Fatal("available encoding capacity admitted work onto a draining node")
	}
}

// The repository tests below run in CI through scripts/ci/db-contracts.txt.
func newCapacityTestRepo(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewRepository(pool)
}

func createCapacityTestNode(t *testing.T, repo *Repository) *Node {
	t.Helper()
	ctx := context.Background()
	unique := time.Now().UnixNano()
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("capacity-test-%d", unique),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://capacity-test-%d", unique),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	return node
}

func TestRepositoryEncodingCeilingPreservesDrainState(t *testing.T) {
	repo := newCapacityTestRepo(t)
	ctx := context.Background()
	node := createCapacityTestNode(t, repo)
	if _, err := repo.Update(ctx, node.ID, UpdateNodeInput{MaxJobs: intPtr(20)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateHealthReport(ctx, node.ID, node.URL, HealthReport{
		Healthy: true, DrainState: DrainStateDraining,
		LastStats: []byte(`{"encoding_capacity":{"max_jobs":4,"active_jobs":0}}`),
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, node.ID)
	if err != nil || stored == nil {
		t.Fatal("cannot read draining capacity report", err)
	}
	if stored.DrainState != DrainStateDraining || stored.MaxJobs == nil || *stored.MaxJobs != 4 {
		t.Fatalf("persisting capacity lost drain state or ceiling: %+v", stored)
	}
}

func TestFiniteWorkerCeilingKeepsCurrentDrainingSession(t *testing.T) {
	for _, withProxy := range []bool{false, true} {
		name := "local egress"
		var proxies []*Node
		if withProxy {
			name = "proxy egress"
			proxies = []*Node{proxyNode(1, "http://proxy", nil)}
		}
		t.Run(name, func(t *testing.T) {
			node := drainingNode(2, "http://tc-draining/", 1)
			f := newFixture(proxies, []*Node{node})
			f.transcodes.ApplyHealthReport(node.ID, node.URL, HealthReport{
				Healthy: true, DrainState: DrainStateDraining, ActiveJobs: 1,
				LastStats: []byte(`{"encoding_capacity":{"max_jobs":1,"active_jobs":1}}`),
			}, time.Now())
			plan := f.planner.PlanSession("existing", node.URL, true, 0)
			if plan.TranscodeNode == nil || !sameNodeURL(plan.TranscodeNode.URL, node.URL) {
				t.Fatalf("provider ceiling evicted existing draining session: %+v", plan.TranscodeNode)
			}
			if plan := f.planner.PlanSession("fresh", "", true, 0); plan.TranscodeNode != nil {
				t.Fatal("full draining node admitted a new session")
			}
			f.transcodes.ApplyHealthReport(node.ID, node.URL, HealthReport{
				Healthy: true, DrainState: DrainStateDrained,
			}, time.Now())
			if plan := f.planner.PlanSession("existing", node.URL, true, 0); plan.TranscodeNode != nil {
				t.Fatal("drained node retained a session")
			}
		})
	}
}

func TestRepositoryHealthReconcilesWorkerCeilingAndRevision(t *testing.T) {
	repo := newCapacityTestRepo(t)
	ctx := context.Background()
	node := createCapacityTestNode(t, repo)
	if _, err := repo.Update(ctx, node.ID, UpdateNodeInput{MaxJobs: intPtr(20)}); err != nil {
		t.Fatal(err)
	}
	// Default, older and invalid reports must leave existing customer limits alone.
	for _, stats := range []string{`{"encoding_capacity":{"max_jobs":0,"active_jobs":0}}`, `{}`, `{"encoding_capacity":{"max_jobs":2147483648,"active_jobs":0}}`} {
		if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 0, 0, []byte(stats), nil); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.GetByID(ctx, node.ID)
		if err != nil || stored.MaxJobs == nil || *stored.MaxJobs != 20 {
			t.Fatalf("non-finite report changed existing cap: %+v, %v", stored, err)
		}
	}
	var before, after int64
	if err := repo.pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id=$1`, node.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	stats := []byte(`{"encoding_capacity":{"max_jobs":4,"active_jobs":0,"available_jobs":4}}`)
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 0, 0, stats, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MaxJobs == nil || *stored.MaxJobs != 4 {
		t.Fatalf("stored core cap = %v, want 4", stored.MaxJobs)
	}
	if err := repo.pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id=$1`, node.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatal("automatic configuration change did not invalidate the admin revision")
	}
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 0, 0, stats, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id=$1`, node.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("unchanged report invalidated the admin revision again")
	}
	for _, tc := range []struct {
		requested int
		want      int
	}{{20, 4}, {2, 2}, {0, 4}} {
		updated, err := repo.Update(ctx, node.ID, UpdateNodeInput{MaxJobs: intPtr(tc.requested)})
		if err != nil {
			t.Fatal(err)
		}
		if updated.MaxJobs == nil || *updated.MaxJobs != tc.want {
			t.Fatalf("request %d stored %v, want %d", tc.requested, updated.MaxJobs, tc.want)
		}
	}
}
