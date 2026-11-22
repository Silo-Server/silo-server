package nodedeclare

import (
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

func TestFromEnv(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}
	t.Run("unset and blank variables declare nothing", func(t *testing.T) {
		in, err := FromEnv(nodepool.NodeTypeTranscode, env(map[string]string{"SILO_NODE_GROUP": "  "}))
		if err != nil || in.SetFields() != nil {
			t.Fatalf("declared %v, err %v; want nothing", in.SetFields(), err)
		}
	})
	t.Run("values follow the update body", func(t *testing.T) {
		in, err := FromEnv(nodepool.NodeTypeTranscode, env(map[string]string{
			"SILO_NODE_MAX_TRANSCODES": "0", "SILO_NODE_HW_ACCEL": "VideoToolbox", "SILO_NODE_SOURCE_ACCESS": "core",
			"SILO_NODE_URL": "http://gpu1:8082", "SILO_NODE_DOWNLOAD_ARTIFACT_MAX_BYTES": "0",
			// A proxy variable means nothing to a transcode node.
			"SILO_NODE_MAX_STREAMS": "5",
		}))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{nodepool.FieldURL, nodepool.FieldMaxJobs, nodepool.FieldSourceAccess, nodepool.FieldHWAccelOverride, nodepool.FieldDownloadArtifactMaxBytesOverride}
		if got := in.SetFields(); !slices.Equal(got, want) {
			t.Fatalf("declared %v, want %v", got, want)
		}
		if *in.MaxJobs != 0 || *in.HWAccelOverride != "videotoolbox" || *in.DownloadArtifactMaxBytesOverride != 0 {
			t.Fatalf("max_jobs %d, hw_accel %q, max_bytes %d", *in.MaxJobs, *in.HWAccelOverride, *in.DownloadArtifactMaxBytesOverride)
		}
	})
	// Each bad value is refused naming its variable: a parse failure here, a
	// store rule through nodepool.ValidateDeclaration.
	for _, tc := range []struct {
		nodeType, variable, value string
	}{
		{nodepool.NodeTypeProxy, "SILO_NODE_MAX_STREAMS", "-1"},
		{nodepool.NodeTypeProxy, "SILO_NODE_PUBLIC_URL", "proxy.example.com"},
		{nodepool.NodeTypeTranscode, "SILO_NODE_HW_ACCEL", "cuda"},
		{nodepool.NodeTypeTranscode, "SILO_NODE_DOWNLOAD_ARTIFACT_DIR", "relative/dir"},
		{nodepool.NodeTypeTranscode, "SILO_NODE_SOURCE_ACCESS", "nfs"},
	} {
		t.Run(tc.variable, func(t *testing.T) {
			_, err := FromEnv(tc.nodeType, env(map[string]string{tc.variable: tc.value}))
			declErr, ok := errors.AsType[*Error](err)
			if !ok || declErr.Variable != tc.variable {
				t.Fatalf("err %v, want an error naming %s", err, tc.variable)
			}
		})
	}
}
