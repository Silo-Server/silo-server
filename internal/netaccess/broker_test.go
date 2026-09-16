package netaccess

import "testing"

func TestBrokerRejectsReportsFromRevokedAndReplacedProcesses(t *testing.T) {
	b := NewBroker()
	old, err := b.Issue(7, "stub")
	if err != nil {
		t.Fatal(err)
	}
	stale := Status{InstallationID: 7, Provider: "stub", State: StateConnected, Origin: "https://old.example.test"}
	b.ReportFor(7, old, stale)
	b.Revoke(7, old)
	b.ReportFor(7, old, stale)
	if _, ok := b.Status.Get(7); ok {
		t.Fatal("late report restored a revoked process")
	}
	fresh, err := b.Issue(7, "stub")
	if err != nil {
		t.Fatal(err)
	}
	b.ReportFor(7, old, stale)
	if _, ok := b.Status.Get(7); ok {
		t.Fatal("old process reported under its replacement's token")
	}
	current := Status{InstallationID: 7, Provider: "stub", State: StateDisconnected}
	b.ReportFor(7, fresh, current)
	b.ReportFor(7, old, stale)
	if got, ok := b.Status.Get(7); !ok || got.State != StateDisconnected {
		t.Fatalf("late report overwrote replacement: %+v %v", got, ok)
	}
	if _, err := b.Issue(7, "stub"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Status.Get(7); ok {
		t.Fatal("replacement inherited the previous process status")
	}
}
