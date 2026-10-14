package planstore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresRouteSessionCASPreservesAttempt(t *testing.T) {
	f := newPlanstoreFixture(t)
	store := NewPostgres(f.pool)
	capability, ok := any(store).(playback.RouteSessionStoreV3)
	if !ok {
		t.Fatal("PostgreSQL plan store cannot own a route session")
	}
	for _, change := range []string{"current", "local", "attempt", "plan", "replan", "node", "transport", "owner", "profile", "file", "expired", "stopped"} {
		t.Run(change, func(t *testing.T) {
			record := f.attemptRecord(uuid.NewString(), uuid.NewString(), "digest")
			record.FrozenRecipe.TranscodeNodeURL, record.FrozenRecipe.TranscodeTransportID = "https://old", "transport"
			if err := store.SaveAttempt(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApplyProgress(t.Context(), record.SessionID, playback.ProgressSampleV3{Sequence: 11, Position: 123, IsPaused: true}); err != nil {
				t.Fatal(err)
			}
			if change == "expired" {
				f.expireAttempt(t, record.PlaybackAttemptID)
			}
			if change == "stopped" {
				if _, _, err := store.StopAttempt(t.Context(), record.SessionID, uuid.NewString(), nil); err != nil {
					t.Fatal(err)
				}
			}
			owned, err := capability.AcquireRouteSession(t.Context(), record.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.Close()
			fresh, err := owned.GetAttempt(t.Context())
			if err != nil || fresh == nil {
				t.Fatalf("raw presence must include expired/stopped: %v", err)
			}
			expected := *fresh
			switch change {
			case "attempt":
				expected.PlaybackAttemptID = uuid.NewString()
			case "plan":
				expected.CurrentPlanID = "other"
			case "replan":
				expected.CurrentReplanRequestID = "other"
			case "node":
				expected.FrozenRecipe.TranscodeNodeURL = "https://other"
			case "transport":
				expected.FrozenRecipe.TranscodeTransportID = "other"
			case "owner":
				expected.UserID++
			case "profile":
				expected.ProfileID = "other"
			case "file":
				expected.EffectiveMediaFileID++
			}
			next := playback.TranscodeRoute{NodeURL: "https://new", TransportID: "transport"}
			if change == "local" {
				next.NodeURL = ""
			}
			moved, err := owned.CompareAndSwap(t.Context(), expected, next)
			if err != nil {
				t.Fatal(err)
			}
			wantMove := change == "current" || change == "local"
			if moved != wantMove {
				t.Fatalf("moved=%v want=%v", moved, wantMove)
			}
			after, err := owned.GetAttempt(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := *fresh
			if wantMove {
				want.FrozenRecipe.TranscodeNodeURL, want.FrozenRecipe.TranscodeTransportID = next.NodeURL, next.TransportID
			}
			beforeJSON, _ := json.Marshal(want)
			afterJSON, _ := json.Marshal(after)
			if string(beforeJSON) != string(afterJSON) {
				t.Fatalf("tuple CAS changed other fields:\nwant%s\ngot%s", beforeJSON, afterJSON)
			}
		})
	}
}

// The proxy withholds an actual UPDATE acknowledgement after PostgreSQL has
// accepted the command. Authentication/startup frames and later connections
// pass through; no database credentials or protocol payloads are logged.
func routeAcknowledgementProxy(t *testing.T, config *pgxpool.Config) (string, *atomic.Bool, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	targetAddress := net.JoinHostPort(config.ConnConfig.Host, strconv.Itoa(int(config.ConnConfig.Port)))
	var armed atomic.Bool
	committed := make(chan struct{})
	var active sync.Map
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			front, err := listener.Accept()
			if err != nil {
				return
			}
			back, err := net.Dial("tcp", targetAddress)
			if err != nil {
				_ = front.Close()
				continue
			}
			active.Store(front, back)
			clientDone := make(chan struct{})
			wg.Go(func() {
				defer close(clientDone)
				defer func() { _ = front.Close() }()
				defer func() { _ = back.Close() }()
				_, _ = io.Copy(back, front)
			})
			wg.Go(func() {
				defer active.Delete(front)
				defer func() { _ = front.Close() }()
				defer func() { _ = back.Close() }()
				for {
					var header [5]byte
					if _, err := io.ReadFull(back, header[:]); err != nil {
						return
					}
					size := int(binary.BigEndian.Uint32(header[1:])) - 4
					if size < 0 || size > 1<<20 {
						return
					}
					body := make([]byte, size)
					if _, err := io.ReadFull(back, body); err != nil {
						return
					}
					if header[0] == 'C' && strings.HasPrefix(string(body), "UPDATE 1") && armed.CompareAndSwap(true, false) {
						close(committed)
						<-clientDone
						return
					}
					if _, err := front.Write(append(header[:], body...)); err != nil {
						return
					}
				}
			})
		}
	})
	t.Cleanup(func() {
		_ = listener.Close()
		active.Range(func(front, back any) bool { _ = front.(net.Conn).Close(); _ = back.(net.Conn).Close(); return true })
		wg.Wait()
	})
	return listener.Addr().String(), &armed, committed
}

func TestPostgresRouteSessionLostCommitAcknowledgementRejoinsBackend(t *testing.T) {
	f := newPlanstoreFixture(t)
	base := NewPostgres(f.pool)
	record := f.attemptRecord(uuid.NewString(), uuid.NewString(), "lost-route-ack")
	record.FrozenRecipe.TranscodeNodeURL, record.FrozenRecipe.TranscodeTransportID = "https://old", "transport"
	if err := base.SaveAttempt(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	config := f.pool.Config().Copy()
	address, armed, committed := routeAcknowledgementProxy(t, config)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Host, config.ConnConfig.Port = host, uint16(port)
	config.ConnConfig.TLSConfig, config.ConnConfig.Fallbacks = nil, nil
	config.MaxConns, config.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewPostgres(pool)
	owned, err := store.AcquireRouteSession(t.Context(), record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	before, err := owned.GetAttempt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	oldPID := owned.(*postgresRouteSession).conn.PgConn().PID()
	armed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	writeDone := make(chan error, 1)
	go func() {
		_, err := owned.CompareAndSwap(ctx, *before, playback.TranscodeRoute{NodeURL: "https://new", TransportID: "transport"})
		writeDone <- err
	}()
	select {
	case <-committed:
	case <-time.After(2 * time.Second):
		t.Fatal("real route UPDATE never reached acknowledgement proxy")
	}
	// This independent connection must observe a real committed route while
	// the writer is still waiting for its withheld acknowledgement.
	seen, err := base.GetAttempt(t.Context(), record.SessionID)
	if err != nil || seen.FrozenRecipe.TranscodeNodeURL != "https://new" {
		t.Fatalf("route did not actually commit behind lost ack: %v", err)
	}
	if err = <-writeDone; err == nil {
		t.Fatal("withheld acknowledgement unexpectedly succeeded")
	}
	joinCtx, joinCancel := context.WithTimeout(t.Context(), time.Second)
	defer joinCancel()
	settled, err := owned.GetAttempt(joinCtx)
	if err != nil || settled.FrozenRecipe.TranscodeNodeURL != "https://new" {
		t.Fatalf("rejoined authority lost committed route: %v", err)
	}
	if newPID := owned.(*postgresRouteSession).conn.PgConn().PID(); newPID == oldPID {
		t.Fatal("uncertain SQL backend was reused")
	}
	if pool.Stat().AcquiredConns() != 0 {
		t.Fatal("detached route owner retained ordinary pool slot")
	}
	t.Log("actual PostgreSQL commit survived withheld UPDATE acknowledgement and fresh same-key backend join")
}

func TestPostgresRouteSessionSharesReplanLockWithoutStarvingPool(t *testing.T) {
	f := newPlanstoreFixture(t)
	config := f.pool.Config().Copy()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewPostgres(pool)
	capability, ok := any(store).(playback.RouteSessionStoreV3)
	if !ok {
		t.Fatal("PostgreSQL plan store cannot own a route session")
	}
	id := uuid.NewString()
	owned, err := capability.AcquireRouteSession(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	if _, err = owned.GetAttempt(t.Context()); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("genuinely absent row=%v", err)
	}
	// The signed worker start may call the core's ordinary database pool.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var one int
	if err = pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("MaxConns1 pool starved by owned move: %v", err)
	}
	// Independent store wrapper models another API, bypassing the local gate.
	other := NewPostgres(f.pool)
	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel2()
	if release, err := other.AcquireSessionLock(ctx2, id); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("replan bypassed owned move: %v", err)
	}
	owned.Close()
	release, err := other.AcquireSessionLock(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
