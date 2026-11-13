package database

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/migrations"
)

// Pool connections move vectors in binary, every way Silo's queries use them,
// still connect to a database without the vector extension, and pick the
// codecs up once the migrations create it. The test role needs CREATEDB.
func TestNewPoolMovesVectorsInBinaryPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := NewPoolForRole(ctx, config.DatabaseConfig{URL: dsn, MaxConnections: 1}, "application")
	if err != nil {
		t.Fatal(err)
	}
	defer ClosePool(pool)
	want := pgvector.NewVector([]float32{0.5, -0.25, 0.001})

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE vector_codec_test (id int PRIMARY KEY, embedding vector(3) NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	// Writes send pgvector.Vector values, as the embedding writers and the
	// catalog seed import do.
	if _, err := conn.Exec(ctx, `INSERT INTO vector_codec_test VALUES (1, $1), (2, $1)`, want); err != nil {
		t.Fatal(err)
	}

	rows, err := conn.Query(ctx, `SELECT embedding FROM vector_codec_test ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	if format := rows.FieldDescriptions()[0].Format; format != pgx.BinaryFormatCode {
		rows.Close()
		t.Fatalf("vector column format = %d, want binary", format)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[pgvector.Vector])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !slices.Equal(got[0].Slice(), want.Slice()) || !slices.Equal(got[1].Slice(), want.Slice()) {
		t.Fatalf("read back %v, want %v twice", got, want)
	}

	// Similarity queries pass the query embedding as a pgvector.HalfVector for
	// their halfvec parameter, which goes in binary: 2 bytes a dimension.
	halfvec, ok := conn.Conn().TypeMap().TypeForName("halfvec")
	if !ok {
		t.Fatal("halfvec is not registered")
	}
	encoded, err := conn.Conn().TypeMap().Encode(halfvec.OID, pgx.BinaryFormatCode, pgvector.NewHalfVector(want.Slice()), nil)
	if err != nil || len(encoded) != 4+2*len(want.Slice()) {
		t.Fatalf("binary halfvec parameter = %d bytes, %v; want %d", len(encoded), err, 4+2*len(want.Slice()))
	}
	var distance float64
	if err := conn.QueryRow(ctx,
		`SELECT embedding::halfvec(3) <=> $1::halfvec(3) FROM vector_codec_test WHERE id = 1`,
		pgvector.NewHalfVector(want.Slice()),
	).Scan(&distance); err != nil {
		t.Fatal(err)
	}
	if distance > 1e-3 {
		t.Fatalf("distance to itself = %v", distance)
	}
	half := pgvector.NewHalfVector([]float32{0.5, -0.25, 0.125})
	var gotHalf pgvector.HalfVector
	if err := conn.QueryRow(ctx, `SELECT $1::halfvec(3)`, half).Scan(&gotHalf); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotHalf.Slice(), half.Slice()) {
		t.Fatalf("halfvec read back %v, want %v", gotHalf, half)
	}

	// Taste profiles and clusters can hold a NULL embedding. Reading one into
	// a vector is an error, as it was over text, not a panic in the decoder.
	var null pgvector.Vector
	if err := conn.QueryRow(ctx, `SELECT NULL::vector(3)`).Scan(&null); err == nil {
		t.Fatal("scanning NULL into a vector succeeded")
	}
	var nullHalf pgvector.HalfVector
	if err := conn.QueryRow(ctx, `SELECT NULL::halfvec(3)`).Scan(&nullHalf); err == nil {
		t.Fatal("scanning NULL into a halfvec succeeded")
	}
	var optional *pgvector.Vector
	if err := conn.QueryRow(ctx, `SELECT NULL::vector(3)`).Scan(&optional); err != nil || optional != nil {
		t.Fatalf("scanning NULL into *pgvector.Vector = %v, %v; want nil", optional, err)
	}

	checkVectorCodecsOnNewDatabase(t, dsn)
}

// checkVectorCodecsOnNewDatabase checks that a pool connects to a new
// database, which has no vector extension until its first migration, and that
// every connection has the codecs once the migrations create it.
func checkVectorCodecsOnNewDatabase(t *testing.T, dsn string) {
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("silo_vector_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	empty, err := NewPoolForRole(ctx, config.DatabaseConfig{URL: u.String(), MaxConnections: 4}, "application")
	if err != nil {
		t.Fatalf("pool for a database without the vector extension: %v", err)
	}
	defer ClosePool(empty)
	var installed bool
	if err := empty.QueryRow(ctx, `SELECT to_regtype('vector') IS NOT NULL`).Scan(&installed); err != nil || installed {
		t.Fatalf("new database has the vector type: %v, %v", installed, err)
	}

	// The first migration creates the extension. Every connection afterwards,
	// including the one opened above, moves vectors in binary.
	if err := RunMigrations(ctx, empty, migrations.FS, "sql"); err != nil {
		t.Fatal(err)
	}
	var conns []*pgxpool.Conn
	defer func() {
		for _, c := range conns {
			c.Release()
		}
	}()
	for range empty.Config().MaxConns {
		c, err := empty.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		if _, ok := c.Conn().TypeMap().TypeForName("vector"); !ok {
			t.Fatalf("connection %d has no vector codec after migrating", len(conns))
		}
	}
}
