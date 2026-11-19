package tonemap

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps probe tests from depending on the GPU of the machine that
// runs them: without a fixture, no DRM device reports a vendor.
func TestMain(m *testing.M) {
	sysfsRoot = filepath.Join(os.TempDir(), "silo-tonemap-test-no-sysfs")
	os.Exit(m.Run())
}
