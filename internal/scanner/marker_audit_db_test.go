package scanner

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

// Marker history reports the editor's address alone, without the netmask
// suffix (/32, /128) that a text cast of the inet column carries.
func TestListMarkerEditAuditClientIPHasNoNetmaskDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Marker audit client address")

	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, file_size)
		VALUES ($1, $2, 1024)
		RETURNING id`, folderID, fmt.Sprintf("/media/marker-audit-client-ip-%d.mkv", folderID)).Scan(&fileID); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	// The folder's cleanup deletes the file, and its audit rows cascade.
	if _, err := pool.Exec(ctx, `
		INSERT INTO marker_edit_audit (media_file_id, segment_kind, action, client_ip, created_at)
		VALUES ($1, 'intro', 'set', '192.0.2.10', now() - interval '1 minute'),
		       ($1, 'intro', 'clear', '2001:db8:85a3:8d3:1319:8a2e:370:7348', now())`, fileID); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	rows, err := NewFileRepository(pool).ListMarkerEditAudit(ctx, []int{fileID}, 10)
	if err != nil {
		t.Fatalf("ListMarkerEditAudit: %v", err)
	}
	var got []string
	for _, row := range rows {
		if row.ClientIP == nil {
			t.Fatalf("row %d has no client_ip", row.ID)
		}
		got = append(got, *row.ClientIP)
	}
	want := []string{"2001:db8:85a3:8d3:1319:8a2e:370:7348", "192.0.2.10"}
	if !slices.Equal(got, want) {
		t.Fatalf("client_ip = %q, want %q", got, want)
	}
}
