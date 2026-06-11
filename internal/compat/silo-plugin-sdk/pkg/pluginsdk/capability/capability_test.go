package capability

import "testing"

func TestScanSourceIsKnownType(t *testing.T) {
	if ScanSource != "scan_source.v1" {
		t.Fatalf("ScanSource const = %q, want %q", ScanSource, "scan_source.v1")
	}
	found := false
	for _, k := range KnownTypes {
		if k == ScanSource {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ScanSource (%q) missing from KnownTypes %v", ScanSource, KnownTypes)
	}
}

func TestMarkerProviderIsKnownType(t *testing.T) {
	if MarkerProvider != "marker_provider.v1" {
		t.Fatalf("MarkerProvider const = %q, want %q", MarkerProvider, "marker_provider.v1")
	}
	found := false
	for _, k := range KnownTypes {
		if k == MarkerProvider {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("MarkerProvider (%q) missing from KnownTypes %v", MarkerProvider, KnownTypes)
	}
}
