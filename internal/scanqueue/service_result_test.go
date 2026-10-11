package scanqueue

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

func TestScanResultFromIngestCountsUnsupportedFiles(t *testing.T) {
	got := scanResultFromIngest(&libraryingest.Result{ScanResult: &scanner.ScanResult{
		New: 1,
		UnsupportedFiles: []scanner.UnsupportedFile{
			{Path: "/movies/Ronin (1998)/VIDEO_TS/VTS_01_1.VOB", Reason: scanner.UnsupportedReasonDVDVOB},
			{Path: "/movies/Manhunter (1986)/Manhunter (1986).rmvb", Reason: scanner.UnsupportedReasonRealMedia},
		},
	}})
	if got.New != 1 || got.UnsupportedFiles != 2 {
		t.Fatalf("result = %+v, want New 1 and UnsupportedFiles 2", got)
	}
}
