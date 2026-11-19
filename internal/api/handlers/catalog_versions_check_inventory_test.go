package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// inventoryRowStub serves one media file row that a preprobe may replace, so a
// test can model the durable evidence write the synchronous probe commits.
type inventoryRowStub struct {
	row *models.MediaFile
}

func (s *inventoryRowStub) GetByID(context.Context, int) (*models.MediaFile, error) {
	if s.row == nil {
		return nil, nil
	}
	clone := *s.row
	return &clone, nil
}

func (s *inventoryRowStub) set(row *models.MediaFile) { s.row = row }

// recordingPreprober answers a fixed status and optionally rewrites the row the
// handler re-reads, exactly as a committed probe would.
type recordingPreprober struct {
	status VirtualPreprobeStatus
	calls  []int
	after  func()
}

func (p *recordingPreprober) PreprobeSelectedFile(_ context.Context, _ string, fileID int, _ catalog.AccessFilter) VirtualPreprobeStatus {
	p.calls = append(p.calls, fileID)
	if p.after != nil {
		p.after()
	}
	return p.status
}

func inventoryCheckHandler(preprober VirtualSelectionPreprober, row *models.MediaFile) (*CatalogResourceHandler, *inventoryRowStub) {
	stub := &inventoryRowStub{row: row}
	items := &ItemsHandler{}
	items.SetVirtualSelectionPreprober(preprober)
	return &CatalogResourceHandler{FileResolver: stub, ItemAccess: versionCheckAccessStub{}, items: items}, stub
}

func unprobedVirtualRow(id int) *models.MediaFile {
	return &models.MediaFile{
		ID:                         id,
		ContentID:                  "movie:inventory",
		FilePath:                   "virtual://movie/inventory?result=cand-1",
		VirtualOwnerInstallationID: 7,
		VideoTracks:                []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}},
		AudioTracks:                []models.AudioTrack{{Index: 1, Codec: "eac3", Language: "eng"}},
	}
}

// TestCheckVersionsWithInventoryEnumeratesTheSingleRow pins the mode's two
// halves: the liveness answer is unchanged, and the one requested row carries an
// inventory with the provenance its row and the probe established.
func TestCheckVersionsWithInventoryEnumeratesTheSingleRow(t *testing.T) {
	row := unprobedVirtualRow(41)
	probedAt := time.Now().Add(-time.Minute)
	prober := &recordingPreprober{status: VirtualPreprobeVerified}
	h, stub := inventoryCheckHandler(prober, row)
	prober.after = func() {
		// A committed evidence write stamps the row and replaces its declared
		// hints with the enumerated ones.
		probed := *row
		probed.ProbeUpdatedAt = &probedAt
		probed.AudioTracks = []models.AudioTrack{{Index: 1, Codec: "truehd", Language: "eng", Channels: 8}}
		stub.set(&probed)
	}

	results := h.CheckVersionsWithInventory(t.Context(), []int{row.ID})
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one", results)
	}
	result := results[0]
	if !result.Available || result.Failed {
		t.Fatalf("liveness changed in the inventory mode: %+v", result)
	}
	if len(prober.calls) != 1 || prober.calls[0] != row.ID {
		t.Fatalf("preprobe calls = %v, want the requested row", prober.calls)
	}
	if result.Inventory == nil {
		t.Fatal("single-file inventory mode returned no inventory")
	}
	if result.Inventory.Provenance != VersionInventoryVerified {
		t.Fatalf("provenance = %q, want verified", result.Inventory.Provenance)
	}
	if result.Inventory.ProbedAt == nil || !result.Inventory.ProbedAt.Equal(probedAt) {
		t.Fatalf("probed_at = %v, want %v", result.Inventory.ProbedAt, probedAt)
	}
	if len(result.Inventory.AudioTracks) != 1 || result.Inventory.AudioTracks[0].Codec != "truehd" {
		t.Fatalf("inventory tracks = %+v, want the probed audio track", result.Inventory.AudioTracks)
	}
}

// TestCheckVersionsWithInventoryReportsPendingProbe pins the timeout path: a
// probe that ran without committing evidence leaves the row on declared
// metadata and says so, and the read still answers normally.
func TestCheckVersionsWithInventoryReportsPendingProbe(t *testing.T) {
	row := unprobedVirtualRow(42)
	prober := &recordingPreprober{status: VirtualPreprobePending}
	h, _ := inventoryCheckHandler(prober, row)

	results := h.CheckVersionsWithInventory(t.Context(), []int{row.ID})
	if len(results) != 1 || results[0].Inventory == nil {
		t.Fatalf("results = %#v, want one with inventory", results)
	}
	inventory := results[0].Inventory
	if inventory.Provenance != VersionInventoryPending {
		t.Fatalf("provenance = %q, want pending", inventory.Provenance)
	}
	if inventory.ProbedAt != nil {
		t.Fatalf("probed_at = %v, want none for an uncommitted probe", inventory.ProbedAt)
	}
	if len(inventory.AudioTracks) != 1 || inventory.AudioTracks[0].Codec != "eac3" {
		t.Fatalf("pending inventory = %+v, want the declared tracks", inventory.AudioTracks)
	}
}

// TestCheckVersionsWithInventoryKeepsTheBatchShape: the mode is single-file, so a
// batch answer is exactly the liveness answer with no inventory and no probe.
func TestCheckVersionsWithInventoryKeepsTheBatchShape(t *testing.T) {
	row := unprobedVirtualRow(43)
	prober := &recordingPreprober{status: VirtualPreprobeVerified}
	h, _ := inventoryCheckHandler(prober, row)

	results := h.CheckVersionsWithInventory(t.Context(), []int{row.ID, row.ID + 1})
	if len(results) != 2 {
		t.Fatalf("results = %#v, want two", results)
	}
	for _, result := range results {
		if result.Inventory != nil {
			t.Fatalf("batch result carried inventory: %+v", result)
		}
	}
	if len(prober.calls) != 0 {
		t.Fatalf("batch ran %d preprobes, want none", len(prober.calls))
	}
}

// TestCheckVersionsWithInventoryDeclaresWithoutASeam: an unwired preprobe or an
// inaccessible row still answers the row's declared metadata and never probes.
func TestCheckVersionsWithInventoryDeclaresWithoutASeam(t *testing.T) {
	row := unprobedVirtualRow(44)
	unwired, _ := inventoryCheckHandler(nil, row)
	results := unwired.CheckVersionsWithInventory(t.Context(), []int{row.ID})
	if len(results) != 1 || results[0].Inventory == nil {
		t.Fatalf("results = %#v, want one with declared inventory", results)
	}
	if results[0].Inventory.Provenance != VersionInventoryDeclared {
		t.Fatalf("provenance = %q, want declared", results[0].Inventory.Provenance)
	}

	prober := &recordingPreprober{status: VirtualPreprobeVerified}
	denied, _ := inventoryCheckHandler(prober, row)
	denied.ItemAccess = versionCheckAccessStub{err: context.Canceled}
	results = denied.CheckVersionsWithInventory(t.Context(), []int{row.ID})
	if len(results) != 1 || results[0].Inventory != nil {
		t.Fatalf("denied row answered inventory: %#v", results)
	}
	if len(prober.calls) != 0 {
		t.Fatalf("denied row ran %d preprobes, want none", len(prober.calls))
	}
}

// TestCheckVersionsWithInventorySkipsAProbedRow: a row that already carries
// probe evidence needs no provider read, and reports that evidence.
func TestCheckVersionsWithInventorySkipsAProbedRow(t *testing.T) {
	row := unprobedVirtualRow(45)
	probedAt := time.Now().Add(-time.Hour)
	row.ProbeUpdatedAt = &probedAt
	prober := &recordingPreprober{status: VirtualPreprobeVerified}
	h, _ := inventoryCheckHandler(prober, row)

	results := h.CheckVersionsWithInventory(t.Context(), []int{row.ID})
	if len(results) != 1 || results[0].Inventory == nil {
		t.Fatalf("results = %#v, want one with inventory", results)
	}
	if results[0].Inventory.Provenance != VersionInventoryVerified {
		t.Fatalf("provenance = %q, want verified", results[0].Inventory.Provenance)
	}
	if len(prober.calls) != 0 {
		t.Fatalf("stamped row ran %d preprobes, want none", len(prober.calls))
	}
}

// TestCheckVersionsWithInventoryRepairsAStampedEmptyRow: a probe stamp with no
// tracks is not an answer. An evidence write that landed no inventory (or whose
// inventory was invalidated later) leaves exactly that shape, and the pre-play
// bar's version row is the surface a viewer asks through — the repair must fire
// there, not only on a play.
func TestCheckVersionsWithInventoryRepairsAStampedEmptyRow(t *testing.T) {
	row := &models.MediaFile{
		ID:                         46,
		ContentID:                  "movie:inventory",
		FilePath:                   "virtual://movie/inventory?result=cand-1",
		VirtualOwnerInstallationID: 7,
	}
	probedAt := time.Now().Add(-time.Hour)
	row.ProbeUpdatedAt = &probedAt
	prober := &recordingPreprober{status: VirtualPreprobeVerified}
	h, stub := inventoryCheckHandler(prober, row)
	repairedAt := time.Now()
	prober.after = func() {
		repaired := *row
		repaired.ProbeUpdatedAt = &repairedAt
		repaired.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
		repaired.AudioTracks = []models.AudioTrack{{Index: 1, Codec: "truehd", Language: "kor", Channels: 8}}
		stub.set(&repaired)
	}

	results := h.CheckVersionsWithInventory(t.Context(), []int{row.ID})
	if len(prober.calls) != 1 || prober.calls[0] != row.ID {
		t.Fatalf("preprobe calls = %v, want the stamped-empty row re-probed", prober.calls)
	}
	if len(results) != 1 || results[0].Inventory == nil {
		t.Fatalf("results = %#v, want one with inventory", results)
	}
	if got := results[0].Inventory.AudioTracks; len(got) != 1 || got[0].Codec != "truehd" {
		t.Fatalf("inventory tracks = %+v, want the repaired audio track", got)
	}
}
