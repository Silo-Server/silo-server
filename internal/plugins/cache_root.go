package plugins

import (
	"fmt"

	"github.com/Silo-Server/silo-server/internal/privatedir"
)

// EnsurePrivateCacheRoot creates the plugin cache root with mode 0700 when it
// is missing and refuses an existing one that another local account could
// control (see privatedir.Ensure). Whoever can write the cache can replace a
// plugin binary together with the manifest checksum it is checked against, and
// the host runs that binary as this process's user.
func EnsurePrivateCacheRoot(root string) error {
	if err := privatedir.Ensure(root); err != nil {
		return fmt.Errorf("plugin cache dir %w", err)
	}
	return nil
}
