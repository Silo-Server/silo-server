package connectionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

func TestConnectionIdentity(t *testing.T) {
	key := func(ip, secret string) string {
		k, _ := Key(clientip.SetContext(context.Background(), ip), secret)
		return k
	}
	if key("192.168.1.1", "secret") != "local" || key("::1", "secret") != "local" {
		t.Fatal("private address must be local")
	}
	a := key("2001:db8:1234:abcd::1", "secret")
	if a != key("2001:db8:1234:abcd::2", "secret") {
		t.Fatal("IPv6 rotations must share the /64 key")
	}
	if a == key("2001:db8:1234:abce::1", "secret") || a == key("2001:db8:1234:abcd::1", "other") {
		t.Fatal("prefix and secret must isolate keys")
	}
	if strings.Contains(a, "2001") || !strings.HasPrefix(a, "remote:") || len(a) != 71 {
		t.Fatalf("unexpected opaque key %q", a)
	}
	if key("invalid", "secret") != "" || key("8.8.8.8", "") != "" {
		t.Fatal("unknown identity must not share a key")
	}
}

func TestStoreLifecycle(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("connection-test-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) }()
	s := &Store{Pool: pool}
	if err := s.Observe(ctx, user, "device", "local", 44100); err != nil {
		t.Fatal(err)
	}
	limit, err := s.Get(ctx, user, "device", "local")
	if err != nil || limit == nil || limit.Kbps != 44100 || limit.ExpiresAt.Sub(limit.ObservedAt) != TTL {
		t.Fatalf("read: %+v %v", limit, err)
	}
	other, err := s.Get(ctx, user, "other", "local")
	if err != nil || other != nil {
		t.Fatal("device isolation failed")
	}
	if err := s.Observe(ctx, user, "device", "local", 22120); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET observed_at=now()-interval '8 days' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	limit, err = s.Get(ctx, user, "device", "local")
	if err != nil || limit != nil {
		t.Fatal("expired limit applied")
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET updated_at=now()-interval '31 days' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM playback_connections WHERE user_id=$1`, user).Scan(&count); err != nil || count != 0 {
		t.Fatal("retention failed", err)
	}
	if err := s.Observe(ctx, user, "device", "local", 44100); err != nil {
		t.Fatal(err)
	}
	checkResetPreservesObservations(t, s, user)
	checkManualTest(t, s, user)
	if err := s.Observe(ctx, user, "device", "local", 44100); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(ctx, user, "other", "local", 44100); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevices(ctx, user, []string{"device"}); err != nil {
		t.Fatal(err)
	}
	limit, err = s.Get(ctx, user, "device", "local")
	if err != nil || limit != nil {
		t.Fatal("device deletion retained limit")
	}
	if other, err := s.Get(ctx, user, "other", "local"); err != nil || other == nil {
		t.Fatal("device deletion removed another device's limit", err)
	}
}

func TestBudgetVectors(t *testing.T) {
	data, err := os.ReadFile("../../../docs/design/schemas/buffering-quality/v1/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Cases []struct {
			Name   string `json:"name"`
			Probe  int    `json:"probe_kbps"`
			Sample int    `json:"playback_sample_kbps"`
			Limit  int    `json:"learned_limit_kbps"`
			Cap    int    `json:"admin_cap_kbps"`
			Want   *int   `json:"expected_kbps"`
		} `json:"connection_budget_cases"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("no connection vectors")
	}
	for _, v := range vectors.Cases {
		t.Run(v.Name, func(t *testing.T) {
			var p *Probe
			var sample *Sample
			var l *Limit
			if v.Probe > 0 {
				p = &Probe{Kbps: v.Probe}
			}
			if v.Sample > 0 {
				sample = &Sample{Kbps: v.Sample}
			}
			if v.Limit > 0 {
				l = &Limit{Kbps: v.Limit}
			}
			got := Budget(p, sample, l, v.Cap)
			if (got == nil) != (v.Want == nil) || (got != nil && *got != *v.Want) {
				t.Fatalf("budget=%v want=%v", got, v.Want)
			}
		})
	}
}

func checkResetPreservesObservations(t *testing.T, s *Store, user int) {
	t.Helper()
	ctx := t.Context()
	if err := s.ReportProbe(ctx, user, "device", "local", ProbeResult{Kbps: 48000, Bytes: 12000000, DurationMS: 2000, Transport: "wifi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportSummary(ctx, user, "device", "local", PlanSummary{PlanID: "plan-1", ReportedAt: time.Now(), Diagnostics: map[string]string{"stall_count": "2"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, user, "device")
	if err != nil || len(rows) != 1 || rows[0].LearnedLimit == nil || rows[0].LearnedLimit.Kbps != 44100 || rows[0].LearnedLimit.ExpiresAt.Sub(rows[0].LearnedLimit.ObservedAt) != TTL {
		t.Fatalf("admin view lost the limit: %+v %v", rows, err)
	}
	if err := s.Clear(ctx, user, "device", "local"); err != nil {
		t.Fatal(err)
	}
	limit, err := s.Get(ctx, user, "device", "local")
	if err != nil || limit != nil {
		t.Fatal("reset failed")
	}
	rows, err = s.List(ctx, user, "device")
	if err != nil || len(rows) != 1 || rows[0].LearnedLimit != nil || rows[0].Probe == nil || rows[0].Probe.Kbps != 48000 || rows[0].PlanSummary == nil {
		t.Fatalf("reset lost observations: %+v %v", rows, err)
	}
}

// checkManualTest: a manual test clears the playback sample, and the learned
// limit when it is faster than the limit; an automatic test clears neither.
func checkManualTest(t *testing.T, s *Store, user int) {
	t.Helper()
	ctx := t.Context()
	if err := s.Observe(ctx, user, "device", "local", 44100); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		kbps         int
		manual, kept bool
	}{
		{kbps: 60000, kept: true},
		{kbps: 44100, manual: true, kept: true},
		{kbps: 60000, manual: true},
	} {
		if err := s.ReportSample(ctx, user, "device", "local", 90000, 7*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := s.ReportProbe(ctx, user, "device", "local", ProbeResult{Kbps: step.kbps, Bytes: 1, DurationMS: 1, Transport: "wifi", Manual: step.manual}); err != nil {
			t.Fatal(err)
		}
		if limit, err := s.Get(ctx, user, "device", "local"); err != nil || (limit != nil) != step.kept {
			t.Fatalf("after a %d kbps test (manual %v): limit %+v (%v), want kept %v", step.kbps, step.manual, limit, err, step.kept)
		}
		if c, err := s.Connection(ctx, user, "device", "local"); err != nil || c == nil || (c.PlaybackSample != nil) == step.manual {
			t.Fatalf("after a %d kbps test (manual %v): row %+v (%v), want the sample kept only after an automatic test", step.kbps, step.manual, c, err)
		}
	}
}

// TestReadingsDB: a probe keeps whether it was a lower bound, and a playback
// sample is replaced only by a higher one, or by any once the kept sample is
// older than the reading lifetime.
func TestReadingsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("connection-readings-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) }()
	s := &Store{Pool: pool}
	const lifetime = 7 * 24 * time.Hour

	if err := s.ReportProbe(ctx, user, "device", "local", ProbeResult{Kbps: 115000, Bytes: 7187500, DurationMS: 500, Transport: "ethernet", LowerBound: true}); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Connection(ctx, user, "device", "local"); err != nil || c == nil || c.Probe == nil || c.Probe.Kbps != 115000 || !c.Probe.LowerBound {
		t.Fatalf("lower-bound probe read back as %+v (%v)", c, err)
	}
	if c, err := s.Connection(ctx, user, "device", "remote:other"); err != nil || c != nil {
		t.Fatalf("another connection's row read back as %+v (%v)", c, err)
	}
	for _, step := range []struct {
		report, want int
		age          string
	}{
		{report: 30000, want: 30000},
		{report: 20000, want: 30000},
		{report: 40000, want: 40000},
		{age: "8 days", report: 10000, want: 10000},
	} {
		if step.age != "" {
			if _, err := pool.Exec(ctx, `UPDATE playback_connections SET sample_at = now() - $2::interval WHERE user_id = $1`, user, step.age); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.ReportSample(ctx, user, "device", "local", step.report, lifetime); err != nil {
			t.Fatal(err)
		}
		if c, err := s.Connection(ctx, user, "device", "local"); err != nil || c == nil || c.PlaybackSample == nil || c.PlaybackSample.Kbps != step.want {
			t.Fatalf("after a %d sample (kept one aged %q): %+v (%v), want %d", step.report, step.age, c, err, step.want)
		}
	}
	rows, err := s.List(ctx, user, "device")
	if err != nil || len(rows) != 1 || rows[0].Probe == nil || !rows[0].Probe.LowerBound || rows[0].PlaybackSample == nil || rows[0].PlaybackSample.Kbps != 10000 {
		t.Fatalf("admin view: %+v (%v)", rows, err)
	}
}
