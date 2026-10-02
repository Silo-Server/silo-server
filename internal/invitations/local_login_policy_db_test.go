package invitations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type pausedPolicyInvitationRepo struct {
	*Repository
	checked chan struct{}
	resume  chan struct{}
}

func (r *pausedPolicyInvitationRepo) AcceptAs(ctx context.Context, hash string, linkAddress func() (string, error), provision func(*models.Invitation, pgx.Tx) (*models.User, error)) (*models.User, error) {
	close(r.checked)
	select {
	case <-r.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return r.Repository.AcceptAs(ctx, hash, linkAddress, provision)
}

func TestAcceptRechecksLocalPolicyBeforeClaimDB(t *testing.T) {
	f := atomicInvitationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, "CREATE TABLE "+pgx.Identifier{f.schema}.Sanitize()+".server_settings (LIKE public.server_settings INCLUDING ALL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO server_settings(key,value) VALUES ($1,'true')`, config.AuthLocalPasswordLoginSettingKey); err != nil {
		t.Fatal(err)
	}
	inv := f.invite(t, "policy-change")
	users, sessions := auth.NewUserRepository(f.pool), auth.NewSessionRepository(f.pool)
	paused := &pausedPolicyInvitationRepo{Repository: f.repo, checked: make(chan struct{}), resume: make(chan struct{})}
	svc := &Service{
		repo:     paused,
		accounts: auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)),
		sessions: auth.NewService(auth.NewLocalProvider(users, sessions), nil, sessions, users, nil, nil, nil),
	}
	type acceptResult struct {
		pair *auth.TokenPair
		user *models.User
		err  error
	}
	result := make(chan acceptResult, 1)
	go func() {
		pair, user, err := svc.Accept(ctx, "policy-change", "", "correct horse battery", "test", "")
		result <- acceptResult{pair: pair, user: user, err: err}
	}()
	select {
	case <-paused.checked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, changeErr := f.pool.Exec(ctx, `UPDATE server_settings SET value = 'false' WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey)
	close(paused.resume)
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	select {
	case accepted := <-result:
		if !errors.Is(accepted.err, auth.ErrLocalLoginDisabled) || accepted.pair != nil || accepted.user != nil {
			t.Fatalf("accept pair=%v user=%v error=%v, want disabled local sign-in before creation", accepted.pair != nil, accepted.user != nil, accepted.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.counts(t, inv, 0, 0, 0)
}
