package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCheckStatusOf(t *testing.T) {
	for value, want := range map[pluginv1.CheckAccountStatus]string{
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE:        CheckStatusActive,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND:     CheckStatusNotFound,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED:      CheckStatusDisabled,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED: CheckStatusNotPermitted,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED:   CheckStatusUnsupported,
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNAVAILABLE:   CheckStatusUnavailable,
		// Not an answer, and values this build does not know.
		pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSPECIFIED: CheckStatusUnavailable,
		pluginv1.CheckAccountStatus(42):                              CheckStatusUnavailable,
	} {
		if got := checkStatusOf(value); got != want {
			t.Errorf("checkStatusOf(%v) = %q, want %q", value, got, want)
		}
	}
}

func TestVerdictFor(t *testing.T) {
	for _, tc := range []struct {
		status     string
		failClosed bool
		verdict    recheckVerdict
		err        error
	}{
		{CheckStatusActive, false, verdictSlide, nil},
		{"", false, verdictSlide, nil},
		{CheckStatusUnsupported, false, verdictAbsoluteAge, nil},
		{CheckStatusNotFound, false, verdictSlide, ErrSessionRevoked},
		{CheckStatusDisabled, true, verdictSlide, ErrSessionRevoked},
		{CheckStatusNotPermitted, false, verdictSlide, ErrSessionRevoked},
		{CheckStatusUnavailable, false, verdictSlide, nil},
		{CheckStatusUnavailable, true, verdictSlide, ErrProviderUnavailable},
	} {
		verdict, err := verdictFor(tc.status, tc.failClosed)
		if verdict != tc.verdict || !errors.Is(err, tc.err) {
			t.Errorf("verdictFor(%q, %v) = %v, %v; want %v, %v", tc.status, tc.failClosed, verdict, err, tc.verdict, tc.err)
		}
	}
}

func TestRecheckDue(t *testing.T) {
	now := time.Now()
	at := func(ago time.Duration) *time.Time { v := now.Add(-ago); return &v }
	r := NewProviderRecheck(nil, nil)
	for _, tc := range []struct {
		name     string
		identity LinkedIdentity
		want     bool
	}{
		{"never checked", LinkedIdentity{}, true},
		{"fresh", LinkedIdentity{LastCheckedAt: at(time.Hour), LastCheckStatus: CheckStatusActive}, false},
		{"stale", LinkedIdentity{LastCheckedAt: at(13 * time.Hour), LastCheckStatus: CheckStatusActive}, true},
		{"fresh unsupported", LinkedIdentity{LastCheckedAt: at(time.Hour), LastCheckStatus: CheckStatusUnsupported}, false},
		{"unavailable backs off", LinkedIdentity{LastCheckedAt: at(time.Minute), LastCheckStatus: CheckStatusUnavailable}, false},
		{"unavailable retries after the wait", LinkedIdentity{LastCheckedAt: at(providerRecheckRetryWait), LastCheckStatus: CheckStatusUnavailable}, true},
		{"admin link", LinkedIdentity{LastCheckStatus: ""}, true},
	} {
		if got := r.recheckDue(&tc.identity, 12*time.Hour, now); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

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

func TestExternalIdentityCarriesRefreshState(t *testing.T) {
	state, err := structpb.NewStruct(map[string]any{"refresh_token": "rt"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := externalIdentityFromResponse(context.Background(), 1, &pluginv1.AuthenticateResponse{ExternalSubject: "iss|sub", RefreshState: state})
	if err != nil || identity.RefreshState.GetFields()["refresh_token"].GetStringValue() != "rt" {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
	identity, err = externalIdentityFromResponse(context.Background(), 1, &pluginv1.AuthenticateResponse{ExternalSubject: "iss|sub"})
	if err != nil || identity.RefreshState != nil {
		t.Fatalf("unset refresh_state = %+v, %v", identity.RefreshState, err)
	}
}
