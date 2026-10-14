package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// verifyWorkerOutputToken is the core-only customer authority boundary. Workers
// receive an opaque viewer ID, never the login or API-key identity used here.
func verifyWorkerOutputToken(ctx context.Context, pool *pgxpool.Pool, content *workerContentAuthority, token, secret string) (*streamtoken.Claims, error) {
	results, err := verifyWorkerOutputTokens(ctx, pool, content, []string{token}, secret)
	if err != nil {
		return nil, err
	}
	return results[0].Claims, results[0].Err
}
