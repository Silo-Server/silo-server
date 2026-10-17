package handlers

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/planstore"
)

// The proxy withholds an actual UPDATE acknowledgement after PostgreSQL has
// accepted the command. Authentication/startup frames and later connections
// pass through; no database credentials or protocol payloads are logged.
func rehomeAcknowledgementProxy(t *testing.T, config *pgxpool.Config) (string, *atomic.Bool, <-chan struct{}) {
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

type replanBetweenRejoinStore struct {
	*planstore.Postgres
	replacement *planstore.Postgres
	writer      *pgxpool.Pool
	armed       *atomic.Bool
	changed     atomic.Bool
}

type replanBetweenRejoinSession struct {
	playback.RouteSessionV3
	store     *replanBetweenRejoinStore
	sessionID string
}

func (s *replanBetweenRejoinStore) AcquireRouteSession(ctx context.Context, id string) (playback.RouteSessionV3, error) {
	owned, err := s.Postgres.AcquireRouteSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &replanBetweenRejoinSession{RouteSessionV3: owned, store: s, sessionID: id}, nil
}

func (s *replanBetweenRejoinSession) CompareAndSwap(ctx context.Context, expected playback.AttemptRecordV3, next playback.TranscodeRoute) (bool, error) {
	s.store.armed.Store(true)
	writeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	moved, err := s.RouteSessionV3.CompareAndSwap(writeCtx, expected, next)
	cancel()
	if err == nil {
		return moved, errors.New("actual lost acknowledgement control failed")
	}
	// The discarded writer has lost G. A real independent owner takes G and
	// changes this same transport's plan before the original owner can rejoin.
	joinCtx, cancelJoin := context.WithTimeout(ctx, 2*time.Second)
	defer cancelJoin()
	other, e := s.store.replacement.AcquireRouteSession(joinCtx, s.sessionID)
	if e != nil {
		return false, e
	}
	defer other.Close()
	settled, e := other.GetAttempt(joinCtx)
	if e != nil || settled.FrozenRecipe.TranscodeNodeURL != next.NodeURL {
		return false, errors.Join(e, errors.New("withheld UPDATE was not committed"))
	}
	tag, e := s.store.writer.Exec(joinCtx, `UPDATE playback_v3_attempts SET current_plan_id='successor-plan', current_replan_request_id='successor-replan', current_plan=jsonb_set(current_plan,'{plan_id}','"successor-plan"'::jsonb) WHERE session_id=$1::uuid`, s.sessionID)
	if e != nil || tag.RowsAffected() != 1 {
		return false, errors.Join(e, errors.New("independent owner did not replace plan"))
	}
	s.store.changed.Store(true)
	return false, err
}

func TestPostgresRelayRejoinDoesNotAdoptSupersededCandidate(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	base, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "rehome_rejoin_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		base.Close()
	})
	for _, name := range []string{"playback_v3_attempts", "playback_v3_replans"} {
		if _, err = base.Exec(t.Context(), "CREATE TABLE "+quoted+"."+pgx.Identifier{name}.Sanitize()+" (LIKE public."+pgx.Identifier{name}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	pgConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pgConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	writer, err := pgxpool.NewWithConfig(t.Context(), pgConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	address, armed, _ := rehomeAcknowledgementProxy(t, pgConfig)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	proxyConfig := pgConfig.Copy()
	proxyConfig.ConnConfig.Host, proxyConfig.ConnConfig.Port = host, uint16(port)
	proxyConfig.ConnConfig.TLSConfig, proxyConfig.ConnConfig.Fallbacks = nil, nil
	proxyConfig.MaxConns, proxyConfig.MinConns = 1, 0
	proxyPool, err := pgxpool.NewWithConfig(t.Context(), proxyConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxyPool.Close)
	healthy := newFakeRehomeNode(t, false)
	other := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, healthy.server.URL), pooledNode(3, other.server.URL)}, "ffmpeg")
	cfg := f.handler.playbackConfig()
	cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	native := planstore.NewPostgres(writer)
	record := playback.AttemptRecordV3{PlaybackAttemptID: uuid.NewString(), SessionID: f.session.ID, UserID: 1, ProfileID: "profile-1", EffectiveMediaFileID: 42, CurrentPlanID: "initial-plan", CurrentPlan: playback.PlanV3{PlanID: "initial-plan"}, FrozenRecipe: playback.ExecutableRecipeV3{TranscodeNodeURL: f.deadURL, TranscodeTransportID: rehomeTestTransport}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := native.SaveAttempt(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	store := &replanBetweenRejoinStore{Postgres: planstore.NewPostgres(proxyPool), replacement: native, writer: writer, armed: armed}
	f.handler.PlanStoreV3 = store
	rr := f.segment("seg_00003.ts")
	live, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := native.GetAttempt(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !store.changed.Load() || healthy.starts.Load()+other.starts.Load() != 1 {
		t.Fatal("real lost acknowledgement/replacement control did not execute")
	}
	if rr.Code != http.StatusServiceUnavailable || live.TranscodeNodeURL != f.deadURL || healthy.stops.Load()+other.stops.Load() != 0 || durable.CurrentPlanID != "successor-plan" || durable.CurrentReplanRequestID != "successor-replan" || durable.FrozenRecipe.TranscodeNodeURL == f.deadURL {
		t.Fatalf("superseded candidate was adopted or deleted: status%d liveOld%v stops%d plan%s", rr.Code, live.TranscodeNodeURL == f.deadURL, healthy.stops.Load()+other.stops.Load(), durable.CurrentPlanID)
	}
	t.Log("actual lost UPDATE acknowledgement and independent same-G plan mutation refused stale adoption without DELETE")
}
