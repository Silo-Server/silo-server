package auth

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestProviderRecheckSkipsLocalSessions: a session without an identity, or
// a service without a re-check, never reaches the provider.
func TestProviderRecheckSkipsLocalSessions(t *testing.T) {
	var nilRecheck *ProviderRecheck
	verdict, err := nilRecheck.check(context.Background(), &models.AuthSession{})
	if verdict != verdictSlide || err != nil {
		t.Fatalf("nil re-check = %v, %v", verdict, err)
	}
	// A resolver whose pool fails every query: reaching it would fail.
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	checker := &fakeChecker{respond: answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, "")}
	recheck := NewProviderRecheck(NewAccountResolver(pool, nil, nil), fakeCheckerSource{checker: checker})
	verdict, err = recheck.check(context.Background(), &models.AuthSession{UserID: 1})
	if verdict != verdictSlide || err != nil || checker.callCount() != 0 {
		t.Fatalf("local session = %v, %v, calls %d", verdict, err, checker.callCount())
	}
}
