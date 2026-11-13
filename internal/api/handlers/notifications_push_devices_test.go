package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func handlerPushCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return cipher
}
