package downloads

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// TestDownloadClientAddressesDB pins where a managed download's addresses
// come from: the registering request sets both, an accepted status report
// moves the last one, and a file request moves it only once the recorded one
// is a minute old, so range requests cannot write per request on any server.
func TestDownloadClientAddressesDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	from := func(ip string) context.Context { return clientip.SetContext(ctx, ip) }
	addresses := func(t *testing.T, id string) (created, last string) {
		t.Helper()
		d, err := f.repo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.CreatedClientIP, d.LastClientIP
	}

	d := f.fenceDownload("addr-managed", f.fileID, f.contentID, true)
	if err := f.repo.Create(from("::ffff:203.0.113.7"), d); err != nil {
		t.Fatal(err)
	}
	if created, last := addresses(t, d.ID); created != "203.0.113.7" || last != "203.0.113.7" {
		t.Fatalf("after registration: created %q last %q, want both 203.0.113.7", created, last)
	}

	if _, err := f.repo.ReportStatus(from("198.51.100.2"), f.userID, f.profileA, f.deviceA, d.ID,
		StatusEvent{Status: StatusDownloading, UpdatedAt: time.Now(), Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if created, last := addresses(t, d.ID); created != "203.0.113.7" || last != "198.51.100.2" {
		t.Fatalf("after a status report: created %q last %q", created, last)
	}

	if err := f.repo.RecordLastClientIP(ctx, d.ID, "192.0.2.9"); err != nil {
		t.Fatal(err)
	}
	if _, last := addresses(t, d.ID); last != "198.51.100.2" {
		t.Fatalf("a file request within a minute of the last write replaced it with %q", last)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE downloads SET last_client_ip_at = now() - interval '2 minutes' WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.RecordLastClientIP(ctx, d.ID, "192.0.2.9"); err != nil {
		t.Fatal(err)
	}
	if _, last := addresses(t, d.ID); last != "192.0.2.9" {
		t.Fatalf("a file request after the interval left %q", last)
	}

	quiet := f.fenceDownload("addr-quiet", f.fileID, f.contentID, false)
	if err := f.repo.Create(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if created, last := addresses(t, quiet.ID); created != "" || last != "" {
		t.Fatalf("a row registered without an address: created %q last %q, want both empty", created, last)
	}

	web := f.fenceDownload("addr-web", f.fileID, f.contentID, false)
	if err := f.repo.Create(from("203.0.113.8"), web); err != nil {
		t.Fatal(err)
	}
	if created, last := addresses(t, web.ID); created != "203.0.113.8" || last != "" {
		t.Fatalf("an ephemeral web row: created %q last %q, want only created", created, last)
	}
}
