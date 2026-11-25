package directconn

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"

	"github.com/darthShadow/shunpike/engine"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func apiNodeKey(masterKey []byte) (engine.NodePrivate, error) {
	if len(masterKey) < secret.MinMasterKeyLen {
		return engine.NodePrivate{}, fmt.Errorf("direct node key requires at least %d secret bytes", secret.MinMasterKeyLen)
	}
	raw, err := hkdf.Key(sha256.New, masterKey, nil, "silo/direct-node/v1|api", 32)
	if err != nil {
		return engine.NodePrivate{}, fmt.Errorf("derive API direct node key: %w", err)
	}
	return engine.NodePrivateFromBytes([32]byte(raw)), nil
}
