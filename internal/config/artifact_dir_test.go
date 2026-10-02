package config

import "testing"

func TestEffectiveDownloadArtifactDir(t *testing.T) {
	cases := []struct {
		name         string
		artifactDir  string
		transcodeDir string
		want         string
	}{
		{"explicit artifact dir wins", "/mnt/downloads", "/mnt/fast/transcode", "/mnt/downloads"},
		{"both blank uses the default transcode dir", "", "", "/tmp/silo-download-artifacts"},
		{"sibling of a custom transcode dir", "", "/mnt/fast/transcode", "/mnt/fast/silo-download-artifacts"},
		// A trailing slash must not nest the artifact root inside the
		// transcode dir: the orphaned-transcode sweep deletes non-active
		// subdirectories of the transcode root, so nesting is data loss.
		{"trailing slash still yields the sibling", "", "/mnt/fast/transcode/", "/mnt/fast/silo-download-artifacts"},
		{"root transcode dir", "", "/", "/silo-download-artifacts"},
		{"repeated separators are cleaned", "", "/mnt//fast/transcode", "/mnt/fast/silo-download-artifacts"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveDownloadArtifactDir(tc.artifactDir, tc.transcodeDir); got != tc.want {
				t.Fatalf("EffectiveDownloadArtifactDir(%q, %q) = %q, want %q",
					tc.artifactDir, tc.transcodeDir, got, tc.want)
			}
		})
	}
}
