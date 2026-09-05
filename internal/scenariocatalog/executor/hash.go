package executor

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/Silo-Server/silo-server/internal/invitations"
)

// hashInvite mirrors invitations.HashToken so seeded rows match raw tokens.
func hashInvite(token string) string { return invitations.HashToken(token) }

// hashDevice mirrors the device-login secret digest (sha256 hex) so seeded
// rows resolve from the raw codes the catalogs send.
func hashDevice(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
