package downloads

import "testing"

func TestReusableManagedStatus(t *testing.T) {
	for _, status := range []string{StatusReady, StatusPreparing, StatusDownloading, StatusCompleted} {
		if !reusableManagedStatus(status) {
			t.Fatalf("status %q should be reusable", status)
		}
	}
	for _, status := range []string{StatusCancelled, StatusFailed, StatusRevoked} {
		if reusableManagedStatus(status) {
			t.Fatalf("status %q should force replacement", status)
		}
	}
}

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
