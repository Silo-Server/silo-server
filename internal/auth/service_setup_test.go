package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type setupCountTraceKey struct{}

// setupCountBarrier holds both setup attempts after PostgreSQL has answered
// their initial zero-user query. Releasing them together deterministically
// exercises the check-to-create interleaving that the setup lock must close.
type setupCountBarrier struct {
	mu      sync.Mutex
	reached int
	release chan struct{}
}

func newSetupCountBarrier() *setupCountBarrier {
	return &setupCountBarrier{release: make(chan struct{})}
}

func (b *setupCountBarrier) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "SELECT COUNT(*) FROM users") {
		return context.WithValue(ctx, setupCountTraceKey{}, true)
	}
	return ctx
}

func (b *setupCountBarrier) TraceQueryEnd(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryEndData,
) {
	if traced, _ := ctx.Value(setupCountTraceKey{}).(bool); !traced {
		return
	}

	b.mu.Lock()
	b.reached++
	if b.reached == 2 {
		close(b.release)
	}
	release := b.release
	b.mu.Unlock()

	select {
	case <-release:
	case <-ctx.Done():
	}
}

func TestSetupInitialUserConcurrentAcrossPoolsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()

	controlPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect control pool: %v", err)
	}
	t.Cleanup(controlPool.Close)

	var existingUsers int
	if err := controlPool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&existingUsers); err != nil {
		t.Fatalf("count existing users: %v", err)
	}
	if existingUsers != 0 {
		t.Skipf("setup concurrency test requires an empty dedicated database; found %d users", existingUsers)
	}

	prefix := fmt.Sprintf("setup-race-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = controlPool.Exec(cleanupCtx, "DELETE FROM users WHERE username LIKE $1", prefix+"%")
	})

	barrier := newSetupCountBarrier()
	newTracedPool := func() *pgxpool.Pool {
		config, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse test database URL: %v", err)
		}
		config.ConnConfig.Tracer = barrier
		config.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatalf("connect traced pool: %v", err)
		}
		t.Cleanup(pool.Close)
		return pool
	}

	newSetupService := func(pool *pgxpool.Pool) *Service {
		users := NewUserRepository(pool)
		sessions := NewSessionRepository(pool)
		provider := NewLocalProvider(users, sessions)
		jwt := NewJWTService("setup-race-test-secret", time.Minute, time.Hour)
		return NewService(provider, jwt, sessions, users, nil, nil, nil)
	}

	type setupAttempt struct {
		service  *Service
		username string
		email    string
	}
	type setupResult struct {
		pair *TokenPair
		err  error
	}

	attempts := []setupAttempt{
		{service: newSetupService(newTracedPool()), username: prefix + "-a", email: prefix + "-a@example.invalid"},
		{service: newSetupService(newTracedPool()), username: prefix + "-b", email: prefix + "-b@example.invalid"},
	}
	results := make([]setupResult, len(attempts))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			<-start
			attempt := attempts[i]
			pair, _, err := attempt.service.SetupInitialUser(
				ctx,
				attempt.username,
				attempt.email,
				"synthetic-test-password",
				false,
				"",
				"setup-race-test",
				"127.0.0.1",
			)
			results[i] = setupResult{pair: pair, err: err}
		})
	}
	close(start)
	wg.Wait()

	var successes, setupComplete int
	for _, result := range results {
		switch {
		case result.err == nil && result.pair != nil:
			successes++
		case errors.Is(result.err, ErrSetupAlreadyComplete) && result.pair == nil:
			setupComplete++
		default:
			t.Fatalf("unexpected setup result: pair nil = %v, error = %v", result.pair == nil, result.err)
		}
	}
	if successes != 1 || setupComplete != 1 {
		t.Fatalf("setup results: successes=%d setup_complete=%d, want 1 and 1", successes, setupComplete)
	}

	var users, admins, sessions int
	if err := controlPool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE role = 'admin')
		FROM users
		WHERE username LIKE $1`, prefix+"%").Scan(&users, &admins); err != nil {
		t.Fatalf("count setup users: %v", err)
	}
	if err := controlPool.QueryRow(ctx, `
		SELECT count(*)
		FROM auth_sessions AS sessions
		JOIN users ON users.id = sessions.user_id
		WHERE users.username LIKE $1`, prefix+"%").Scan(&sessions); err != nil {
		t.Fatalf("count setup sessions: %v", err)
	}
	if users != 1 || admins != 1 || sessions != 1 {
		t.Fatalf("persisted setup state: users=%d admins=%d sessions=%d, want 1 each", users, admins, sessions)
	}

	pair, user, err := attempts[0].service.SetupInitialUser(
		ctx,
		prefix+"-later",
		prefix+"-later@example.invalid",
		"synthetic-test-password",
		false,
		"",
		"setup-race-test",
		"127.0.0.1",
	)
	if !errors.Is(err, ErrSetupAlreadyComplete) || pair != nil || user != nil {
		t.Fatalf("later setup = (%v, %v, %v), want setup already complete", pair, user, err)
	}
}
