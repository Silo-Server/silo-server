package scanner

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errChapterLockDecode = errors.New("chapter lock fixture cannot decode bool")

type chapterLockFailingBoolCodec struct {
	pgtype.BoolCodec
	granted *atomic.Bool
}

func (c chapterLockFailingBoolCodec) PlanScan(_ *pgtype.Map, _ uint32, _ int16, _ any) pgtype.ScanPlan {
	return chapterLockFailingBoolScan{granted: c.granted}
}

type chapterLockFailingBoolScan struct{ granted *atomic.Bool }

func (s chapterLockFailingBoolScan) Scan(src []byte, _ any) error {
	if len(src) == 1 && (src[0] == 1 || src[0] == 't') {
		s.granted.Store(true)
	}
	return errChapterLockDecode
}

func TestChapterLockDiscardsUndecodableAcquisitionDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	newPool := func() *pgxpool.Pool {
		t.Helper()
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns, cfg.MinConns = 2, 0
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	pool := newPool()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initialPID := conn.Conn().PgConn().PID()
	var granted atomic.Bool
	// Fail the result read after PostgreSQL really grants the lock, leaving
	// the underlying session healthy and its lock outcome unknown to Go.
	conn.Conn().TypeMap().RegisterType(&pgtype.Type{
		Name: "bool", OID: pgtype.BoolOID,
		Codec: chapterLockFailingBoolCodec{granted: &granted},
	})
	conn.Release()
	fileID := int(time.Now().UnixNano())
	_, release, acquired, err := NewFileRepository(pool).TryLockChapterThumbnails(ctx, fileID)
	if release != nil {
		release()
	}
	if !errors.Is(err, errChapterLockDecode) || acquired || !granted.Load() {
		t.Fatalf("uncertain acquisition: acquired=%v granted=%v err=%v", acquired, granted.Load(), err)
	}
	// Another replica must acquire that same real advisory lock immediately.
	_, release, acquired, err = NewFileRepository(newPool()).TryLockChapterThumbnails(ctx, fileID)
	if err != nil || !acquired {
		t.Fatalf("failed decode retained the server lock: acquired=%v err=%v", acquired, err)
	}
	release()
	conn, err = pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if pid := conn.Conn().PgConn().PID(); pid == initialPID {
		t.Fatal("uncertain acquisition returned the damaged session to the pool")
	}
	var healthy bool
	if err := conn.QueryRow(ctx, `SELECT true`).Scan(&healthy); err != nil || !healthy {
		t.Fatalf("replacement session did not restore normal decoding: healthy=%v err=%v", healthy, err)
	}
}
