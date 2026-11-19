package notifications

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/secret"
)

type emailVerificationServiceStore struct {
	calls   int
	intent  EmailVerificationIntent
	receipt EmailVerificationReceipt
	err     error
}

func (f *emailVerificationServiceStore) QueueVerification(_ context.Context, in EmailVerificationIntent, _ *secret.Cipher) (EmailVerificationReceipt, error) {
	f.calls++
	f.intent = in
	return f.receipt, f.err
}
