package downloads

import "testing"

func TestSameManagedTargetIgnoresBatch(t *testing.T) {
	a := &Download{
		MediaFileID:       1,
		BatchID:           "old",
		Format:            FormatTranscode,
		Quality:           Quality5Mbps,
		EffectiveQuality:  Quality5Mbps,
		TargetBitrateKbps: 5000,
		ArtifactID:        "artifact",
		FileSize:          123,
	}
	b := *a
	b.BatchID = "new"

	if !sameManagedTarget(a, &b) {
		t.Fatal("batch changes should not force media replacement")
	}
	b.TargetBitrateKbps = 2000
	if sameManagedTarget(a, &b) {
		t.Fatal("bitrate changes should force media replacement")
	}
}
