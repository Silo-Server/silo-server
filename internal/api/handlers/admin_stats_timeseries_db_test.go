package handlers

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// timeseriesTestPool opens a pool on a fresh schema holding empty copies of the
// tables the timeseries read touches, so the test's window sees only its own
// samples on a shared migrated database.
func timeseriesTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{fmt.Sprintf("timeseries_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	for _, table := range []string{"dashboard_metric_samples", "dashboard_node_egress_samples", "stream_nodes"} {
		if _, err := admin.Exec(t.Context(), fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING ALL)", schema, table, table)); err != nil {
			t.Fatal(err)
		}
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestQueryAdminTimeseriesSplitsEgressBySourceDB: the per-node rows break down
// the node egress the shared row already carries, so they never add to the
// total. Every API process folds into one "api" source. A display
// bucket wider than a minute takes its breakdown from the minute the total
// peaked, so the parts add up to the point's egress instead of mixing peaks
// from different minutes. A minute sampled before per-node rows existed has no
// breakdown, and a node deleted since keeps a readable name.
func TestQueryAdminTimeseriesSplitsEgressBySourceDB(t *testing.T) {
	pool := timeseriesTestPool(t)
	ctx := t.Context()

	node := func(name string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO stream_nodes (name, type, url) VALUES ($1, 'proxy', 'http://' || $1) RETURNING id
		`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ams, fra, lon := node("proxy-ams"), node("proxy-fra"), node("proxy-lon")
	gone := fra + 1000

	// A five-minute bin well inside a three-hour window, on the database clock.
	var bin time.Time
	if err := pool.QueryRow(ctx, `SELECT date_bin('5 minutes', now() - interval '30 minutes', 'epoch')`).Scan(&bin); err != nil {
		t.Fatal(err)
	}
	earlier, peak, later := bin.Add(-10*time.Minute), bin.Add(time.Minute), bin.Add(2*time.Minute)
	sample := func(at time.Time, source string, kbps, downloadKbps int64) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO dashboard_metric_samples (bucket, source, egress_kbps, download_egress_kbps) VALUES ($1, $2, $3, $4)
		`, at, source, kbps, downloadKbps); err != nil {
			t.Fatal(err)
		}
	}
	nodeSample := func(at time.Time, id int, kbps int64) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO dashboard_node_egress_samples (bucket, node_id, egress_kbps) VALUES ($1, $2, $3)
		`, at, id, kbps); err != nil {
			t.Fatal(err)
		}
	}
	nodeSource := func(id int) string { return fmt.Sprintf("node:%d", id) }

	// Before per-node rows: the node egress is only in the shared total.
	sample(earlier, "shared", 600, 0)
	// The bin's peak minute: 3050 node egress + 800 API egress = 3850.
	sample(peak, "shared", 3050, 0)
	nodeSample(peak, ams, 2000)
	nodeSample(peak, fra, 1000)
	nodeSample(peak, gone, 50)
	sample(peak, "proc:replica-a", 500, 100)
	sample(peak, "proc:replica-b", 300, 0)
	// A quieter minute in the same bin whose API egress alone is higher, and
	// the only minute proxy-lon served anything.
	sample(later, "shared", 1300, 0)
	nodeSample(later, ams, 200)
	nodeSample(later, fra, 800)
	nodeSample(later, lon, 300)
	sample(later, "proc:replica-a", 2000, 0)

	series, err := queryAdminTimeseries(ctx, pool, 3)
	if err != nil {
		t.Fatal(err)
	}

	type point struct {
		Egress, Download int64
		BySource         map[string]int64
	}
	got := map[time.Time]point{}
	for _, p := range series.Points {
		got[p.T] = point{p.EgressKbps, p.DownloadEgressKbps, p.EgressBySource}
	}
	want := map[time.Time]point{
		earlier.UTC(): {Egress: 600, BySource: map[string]int64{}},
		bin.UTC(): {Egress: 3850, Download: 100, BySource: map[string]int64{
			nodeSource(ams): 2000, nodeSource(fra): 1000, nodeSource(gone): 50, "api": 800,
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("points = %+v\nwant     %+v", got, want)
	}

	// Every source that served anything in the window is listed with its
	// total over every sampled minute, so a client can rank sources by what
	// they served, not by the peak minutes the points happen to show.
	wantSources := []AdminTimeseriesEgressSource{
		{Key: "api", Kind: "api", Name: "API server", EgressBytes: (800 + 2000) * bytesPerKbpsMinute},
		{Key: nodeSource(gone), Kind: "node", NodeID: gone, Name: fmt.Sprintf("Removed node %d", gone), EgressBytes: 50 * bytesPerKbpsMinute},
		{Key: nodeSource(ams), Kind: "node", NodeID: ams, Name: "proxy-ams", EgressBytes: (2000 + 200) * bytesPerKbpsMinute},
		{Key: nodeSource(fra), Kind: "node", NodeID: fra, Name: "proxy-fra", EgressBytes: (1000 + 800) * bytesPerKbpsMinute},
		{Key: nodeSource(lon), Kind: "node", NodeID: lon, Name: "proxy-lon", EgressBytes: 300 * bytesPerKbpsMinute},
	}
	if !reflect.DeepEqual(series.EgressSources, wantSources) {
		t.Fatalf("sources = %+v\nwant      %+v", series.EgressSources, wantSources)
	}

	// Every sampled minute counts once: 600 + 3850 + 3300 kbps-minutes.
	if want := int64(600+3850+3300) * bytesPerKbpsMinute; series.EgressBytes != want {
		t.Fatalf("egress bytes = %d, want %d", series.EgressBytes, want)
	}
}
