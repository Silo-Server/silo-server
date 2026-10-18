package auth

import (
	"strings"
	"testing"
)

func TestPGOAuthStore_SealsBoundToTheCode(t *testing.T) {
	st := NewPGOAuthStore(nil, []byte("test-secret"))
	plaintext := "synthetic-provider-answer"
	codeHash := oauthCompletionCodeHash("completion-code")

	ciphertext, err := st.seal([]byte(plaintext), pendingLinkAAD(codeHash))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(ciphertext, plaintext) {
		t.Fatalf("ciphertext includes the plaintext: %q", ciphertext)
	}
	out, err := st.open(ciphertext, pendingLinkAAD(codeHash))
	if err != nil || string(out) != plaintext {
		t.Fatalf("open = %q, %v", out, err)
	}
	if _, err := st.open(ciphertext, pendingLinkAAD(oauthCompletionCodeHash("another-code"))); err == nil {
		t.Fatal("a sealed answer opened under another code")
	}
}
