package watchstate

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newTestReconciler(
	episodesByID map[string]*models.Episode,
	episodesByKey map[string]*models.Episode,
	providerIDs map[string][]*models.MediaItemProviderID,
) *HistoryReconciler {
	resolver := NewStableIdentityResolver(
		nil,
		testEpisodeRepo{episodes: episodesByID, byKey: episodesByKey},
		testProviderIDRepo{ids: providerIDs},
	)
	return &HistoryReconciler{resolver: resolver}
}

type fakeHistoryDB struct {
	rows       [][]any
	execs      []fakeHistoryExec
	execTag    pgconn.CommandTag
	execTagSet bool
}

type fakeHistoryExec struct {
	sql  string
	args []any
}

func (db *fakeHistoryDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &fakeHistoryRows{rows: db.rows, pos: -1}, nil
}

func (db *fakeHistoryDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	db.execs = append(db.execs, fakeHistoryExec{sql: sql, args: args})
	if db.execTagSet {
		return db.execTag, nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

type fakeHistoryRows struct {
	rows   [][]any
	pos    int
	closed bool
}

func (r *fakeHistoryRows) Close() {
	r.closed = true
}

func (r *fakeHistoryRows) Err() error {
	return nil
}

func (r *fakeHistoryRows) CommandTag() pgconn.CommandTag {
	return pgconn.NewCommandTag("SELECT")
}

func (r *fakeHistoryRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakeHistoryRows) Next() bool {
	if r.closed {
		return false
	}
	r.pos++
	if r.pos >= len(r.rows) {
		r.closed = true
		return false
	}
	return true
}

func (r *fakeHistoryRows) Scan(dest ...any) error {
	if r.pos < 0 || r.pos >= len(r.rows) {
		return fmt.Errorf("Scan called without current row")
	}
	row := r.rows[r.pos]
	if len(dest) != len(row) {
		return fmt.Errorf("Scan destination count = %d, want %d", len(dest), len(row))
	}
	for i, value := range row {
		switch target := dest[i].(type) {
		case *int:
			typed, ok := value.(int)
			if !ok {
				return fmt.Errorf("column %d has type %T, want int", i, value)
			}
			*target = typed
		case *string:
			typed, ok := value.(string)
			if !ok {
				return fmt.Errorf("column %d has type %T, want string", i, value)
			}
			*target = typed
		default:
			return fmt.Errorf("unsupported scan target %T", target)
		}
	}
	return nil
}

func (r *fakeHistoryRows) Values() ([]any, error) {
	if r.pos < 0 || r.pos >= len(r.rows) {
		return nil, fmt.Errorf("Values called without current row")
	}
	return r.rows[r.pos], nil
}

func (r *fakeHistoryRows) RawValues() [][]byte {
	return nil
}

func (r *fakeHistoryRows) Conn() *pgx.Conn {
	return nil
}

func TestReconcilerResolve(t *testing.T) {
	seriesTVDB := map[string][]*models.MediaItemProviderID{
		"series-1": {{Provider: "tvdb", ProviderID: "81189", ItemType: "series"}},
	}
	for _, tc := range []struct {
		name          string
		episodesByKey map[string]*models.Episode
		providerIDs   map[string][]*models.MediaItemProviderID
		identity      userstore.WatchIdentity
		want          string
	}{
		{
			name: "movie",
			providerIDs: map[string][]*models.MediaItemProviderID{
				"movie-new": {{Provider: "tmdb", ProviderID: "550", ItemType: "movie"}},
			},
			identity: userstore.WatchIdentity{StableType: "movie", ProviderIDs: map[string]string{"tmdb": "550"}},
			want:     "movie-new",
		},
		{
			name: "episode legacy provider fallback",
			episodesByKey: map[string]*models.Episode{
				"series-1": {ContentID: "ep-1", SeriesID: "series-1", SeasonNumber: 1, EpisodeNumber: 3},
			},
			providerIDs: seriesTVDB,
			identity: userstore.WatchIdentity{
				StableType:  "episode",
				ProviderIDs: map[string]string{"tvdb": "81189"},
				Season:      new(1),
				Episode:     new(3),
			},
			want: "ep-1",
		},
		{
			name: "episode season zero",
			episodesByKey: map[string]*models.Episode{
				"series-1": {ContentID: "special-1", SeriesID: "series-1", SeasonNumber: 0, EpisodeNumber: 1},
			},
			providerIDs: seriesTVDB,
			identity: userstore.WatchIdentity{
				StableType:        "episode",
				SeriesProviderIDs: map[string]string{"tvdb": "81189"},
				Season:            new(0),
				Episode:           new(1),
			},
			want: "special-1",
		},
		{
			// Season and Episode intentionally nil.
			name:        "episode missing season and episode",
			providerIDs: seriesTVDB,
			identity:    userstore.WatchIdentity{StableType: "episode", SeriesProviderIDs: map[string]string{"tvdb": "81189"}},
			want:        "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newTestReconciler(nil, tc.episodesByKey, tc.providerIDs).resolve(context.Background(), tc.identity)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("resolve = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHistoryReconcilerRunRebindsMissingEpisodeWithUserScopedUpdate(t *testing.T) {
	db := &fakeHistoryDB{rows: [][]any{
		{
			42,
			"history-1",
			"episode-old",
			`{"stable_type":"episode","series_provider_ids":{"tvdb":"81189"},"season":1,"episode":3}`,
		},
	}}
	r := &HistoryReconciler{
		pool: db,
		resolver: newTestReconciler(
			nil,
			map[string]*models.Episode{
				"series-1": {ContentID: "episode-new", SeriesID: "series-1", SeasonNumber: 1, EpisodeNumber: 3},
			},
			map[string][]*models.MediaItemProviderID{
				"series-1": {{Provider: "tvdb", ProviderID: "81189", ItemType: "series"}},
			},
		).resolver,
	}

	stats, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrphansFound != 1 || stats.Resolved != 1 || stats.Unresolvable != 0 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want one resolved orphan", stats)
	}
	if len(db.execs) != 1 {
		t.Fatalf("exec count = %d, want 1", len(db.execs))
	}
	exec := db.execs[0]
	if !strings.Contains(exec.sql, "user_id = $2") ||
		!strings.Contains(exec.sql, "id = $3") ||
		!strings.Contains(exec.sql, "media_item_id = $4") {
		t.Fatalf("update query is not scoped by user, history id, and old media item:\n%s", exec.sql)
	}
	wantArgs := []any{"episode-new", 42, "history-1", "episode-old"}
	if !reflect.DeepEqual(exec.args, wantArgs) {
		t.Fatalf("exec args = %#v, want %#v", exec.args, wantArgs)
	}
}

func TestHistoryReconcilerRunLeavesUnresolvedIdentityUnchanged(t *testing.T) {
	db := &fakeHistoryDB{rows: [][]any{
		{
			42,
			"history-1",
			"episode-old",
			`{"stable_type":"episode","series_provider_ids":{"tvdb":"missing"},"season":1,"episode":3}`,
		},
	}}
	r := &HistoryReconciler{
		pool: db,
		resolver: newTestReconciler(
			nil,
			map[string]*models.Episode{
				"series-1": {ContentID: "episode-new", SeriesID: "series-1", SeasonNumber: 1, EpisodeNumber: 3},
			},
			map[string][]*models.MediaItemProviderID{
				"series-1": {{Provider: "tvdb", ProviderID: "81189", ItemType: "series"}},
			},
		).resolver,
	}

	stats, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrphansFound != 1 || stats.Resolved != 0 || stats.Unresolvable != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want one unresolved orphan", stats)
	}
	if len(db.execs) != 0 {
		t.Fatalf("exec count = %d, want 0", len(db.execs))
	}
}
