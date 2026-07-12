package nfo

import (
	"github.com/Silo-Server/silo-server/internal/metadata"
)

// The NFO provider is a built-in host provider: it is represented in the
// database by the reserved 'silo.builtin' installation's 'nfo' capability and
// resolved in-process through the builtin registry. Registration lives here
// (not in the metadata package) because this package implements the metadata
// package's interfaces; cmd/silo blank-imports this package to activate it.
func init() {
	metadata.RegisterBuiltinProvider("nfo", func() metadata.Provider { return NewProvider() })
}
