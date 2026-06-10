package push

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordedOutcome struct {
	kind string // sent|skipped|failed|dead
	id   int64
	next time.Time
	msg  string
}

type fakeOutcomes struct {
	rec       []recordedOutcome
	committed bool
}

func (f *fakeOutcomes) Sent(id int64) {
	f.rec = append(f.rec, recordedOutcome{"sent", id, time.Time{}, ""})
}
func (f *fakeOutcomes) Skipped(id int64, r string) {
	f.rec = append(f.rec, recordedOutcome{"skipped", id, time.Time{}, r})
}
func (f *fakeOutcomes) Failed(id int64, n time.Time, m string) {
	f.rec = append(f.rec, recordedOutcome{"failed", id, n, m})
}
func (f *fakeOutcomes) Dead(id int64, _ int, _, m string) {
	f.rec = append(f.rec, recordedOutcome{"dead", id, time.Time{}, m})
}
func (f *fakeOutcomes) Commit(context.Context) error { f.committed = true; return nil }
func (f *fakeOutcomes) Rollback(context.Context)     {}

type fakeQueue struct {
	items []claimedDelivery
	out   *fakeOutcomes
}

func (q *fakeQueue) Claim(context.Context, time.Time, int) ([]claimedDelivery, Outcomes, error) {
	return q.items, q.out, nil
}

type fakeTransport struct {
	name       string
	configured bool
	res        SendResult
	retryAfter time.Duration
	err        error
	calls      int
}

func (t *fakeTransport) Name() string    { return t.name }
func (t *fakeTransport) Configured() bool { return t.configured }
func (t *fakeTransport) Send(context.Context, string, Payload) (SendResult, time.Duration, error) {
	t.calls++
	return t.res, t.retryAfter, t.err
}

type fakePresence struct{ connected map[int]bool }

func (p fakePresence) Connected(_ context.Context, u int) bool { return p.connected[u] }

func deliveries(d ...claimedDelivery) []claimedDelivery { return d }

func cd(id int64, userID, attempts int, transport string) claimedDelivery {
	return claimedDelivery{
		Delivery: Delivery{ID: id, UserID: userID, Attempts: attempts, Transport: transport},
		Token:    "tok",
	}
}

func TestWorker_SentOnSuccess(t *testing.T) {
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{
		&fakeTransport{name: "apns", configured: true, res: ResultSent},
	}, func() time.Time { return time.Unix(100, 0) })
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(out.rec) != 1 || out.rec[0].kind != "sent" || !out.committed {
		t.Fatalf("got %+v committed=%v", out.rec, out.committed)
	}
}

func TestWorker_SkipsPresentUser(t *testing.T) {
	tr := &fakeTransport{name: "apns", configured: true, res: ResultSent}
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{connected: map[int]bool{7: true}}, []Transport{tr}, nil)
	w.RunOnce(context.Background())
	if out.rec[0].kind != "skipped" || tr.calls != 0 {
		t.Fatalf("present user must skip without send; got %+v calls=%d", out.rec, tr.calls)
	}
}

func TestWorker_SkipsUnconfiguredTransport(t *testing.T) {
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "fcm")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{
		&fakeTransport{name: "fcm", configured: false},
	}, nil)
	w.RunOnce(context.Background())
	if out.rec[0].kind != "skipped" {
		t.Fatalf("unconfigured → skipped; got %+v", out.rec)
	}
}

func TestWorker_SoftFailBackoffThenDead(t *testing.T) {
	now := time.Unix(1000, 0)
	// attempts=0 → failed at +1m
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{
		&fakeTransport{name: "apns", configured: true, res: ResultSoftFail, err: errors.New("503")},
	}, func() time.Time { return now })
	w.RunOnce(context.Background())
	if out.rec[0].kind != "failed" || !out.rec[0].next.Equal(now.Add(time.Minute)) {
		t.Fatalf("attempt0 → failed +1m; got %+v", out.rec[0])
	}
	// attempts=3 (exhausted) → dead
	out2 := &fakeOutcomes{}
	q2 := &fakeQueue{items: deliveries(cd(2, 7, 3, "apns")), out: out2}
	w2 := NewWorker(q2, fakePresence{}, []Transport{
		&fakeTransport{name: "apns", configured: true, res: ResultSoftFail, err: errors.New("503")},
	}, func() time.Time { return now })
	w2.RunOnce(context.Background())
	if out2.rec[0].kind != "dead" {
		t.Fatalf("exhausted → dead; got %+v", out2.rec[0])
	}
}

func TestWorker_DeadTokenOnHardFail(t *testing.T) {
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "apns")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{
		&fakeTransport{name: "apns", configured: true, res: ResultDead, err: errors.New("Unregistered")},
	}, nil)
	w.RunOnce(context.Background())
	if out.rec[0].kind != "dead" {
		t.Fatalf("hard fail → dead; got %+v", out.rec)
	}
}

// TestWorker_SkipsUnconfiguredTransport_NoCalls asserts that a delivery whose
// transport is registered but not configured is skipped and Send is never called.
func TestWorker_SkipsUnconfiguredTransport_NoCalls(t *testing.T) {
	tr := &fakeTransport{name: "fcm", configured: false}
	out := &fakeOutcomes{}
	q := &fakeQueue{items: deliveries(cd(1, 7, 0, "fcm")), out: out}
	w := NewWorker(q, fakePresence{}, []Transport{tr}, nil)
	w.RunOnce(context.Background())
	if out.rec[0].kind != "skipped" {
		t.Fatalf("unconfigured → skipped; got %+v", out.rec)
	}
	if tr.calls != 0 {
		t.Fatalf("Send must not be called for unconfigured transport; calls=%d", tr.calls)
	}
}

// TestWorker_ParkedTransportFails verifies that after a SoftFail with
// retryAfter=2m the transport is parked, and a second delivery in the SAME
// RunOnce is failed via the parked branch without a second Send call.
func TestWorker_ParkedTransportFails(t *testing.T) {
	now := time.Unix(1000, 0)
	retryAfter := 2 * time.Minute
	tr := &fakeTransport{
		name:       "apns",
		configured: true,
		res:        ResultSoftFail,
		retryAfter: retryAfter,
		err:        errors.New("rate-limited"),
	}
	out := &fakeOutcomes{}
	q := &fakeQueue{
		items: deliveries(
			cd(1, 7, 0, "apns"),
			cd(2, 8, 0, "apns"),
		),
		out: out,
	}
	w := NewWorker(q, fakePresence{}, []Transport{tr}, func() time.Time { return now })
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Only one Send call: the first delivery hits the transport; the second is
	// short-circuited by the parked check.
	if tr.calls != 1 {
		t.Fatalf("expected exactly 1 Send call; got %d", tr.calls)
	}
	if len(out.rec) != 2 {
		t.Fatalf("expected 2 outcomes; got %d", len(out.rec))
	}
	wantNext := now.Add(retryAfter) // max(1m, 2m) = 2m for attempts=0
	for i, rec := range out.rec {
		if rec.kind != "failed" {
			t.Errorf("outcome[%d]: want failed, got %s", i, rec.kind)
		}
		if !rec.next.Equal(wantNext) {
			t.Errorf("outcome[%d]: want next=%v, got %v", i, wantNext, rec.next)
		}
	}
}

// TestWorker_IntermediateBackoff checks that attempts=1 produces a 5m backoff
// and attempts=2 produces a 30m backoff (no retryAfter, so schedule wins).
func TestWorker_IntermediateBackoff(t *testing.T) {
	now := time.Unix(1000, 0)

	cases := []struct {
		attempts int
		wantAdd  time.Duration
	}{
		{1, 5 * time.Minute},
		{2, 30 * time.Minute},
	}
	for _, tc := range cases {
		out := &fakeOutcomes{}
		q := &fakeQueue{items: deliveries(cd(1, 7, tc.attempts, "apns")), out: out}
		w := NewWorker(q, fakePresence{}, []Transport{
			&fakeTransport{name: "apns", configured: true, res: ResultSoftFail},
		}, func() time.Time { return now })
		if _, err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		wantNext := now.Add(tc.wantAdd)
		rec := out.rec[0]
		if rec.kind != "failed" {
			t.Errorf("attempts=%d: want failed, got %s", tc.attempts, rec.kind)
		}
		if !rec.next.Equal(wantNext) {
			t.Errorf("attempts=%d: want next=%v, got %v", tc.attempts, wantNext, rec.next)
		}
	}
}
