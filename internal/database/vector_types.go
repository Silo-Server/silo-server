package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

// registerVectorTypes registers pgvector's binary codecs for vector and
// halfvec, so vectors cross the wire in binary instead of decimal text that
// Postgres formats and Go parses. A 3072-dimension embedding is about 12 KB in
// binary and 39 KB as text. A halfvec parameter needs a pgvector.HalfVector to
// go in binary. A database without the extension yet, such as a new one
// before its first migration, keeps the text form.
func registerVectorTypes(ctx context.Context, conn *pgx.Conn) error {
	var vectorOID, halfvecOID *uint32
	if err := conn.QueryRow(ctx,
		`SELECT to_regtype('vector')::oid, to_regtype('halfvec')::oid`,
		pgx.QueryExecModeSimpleProtocol,
	).Scan(&vectorOID, &halfvecOID); err != nil {
		return fmt.Errorf("looking up the vector types: %w", err)
	}
	types := conn.TypeMap()
	if vectorOID != nil {
		types.RegisterType(&pgtype.Type{Name: "vector", OID: *vectorOID, Codec: nullSafeVectorCodec{pgxvec.VectorCodec{}}})
	}
	if halfvecOID != nil {
		types.RegisterType(&pgtype.Type{Name: "halfvec", OID: *halfvecOID, Codec: nullSafeVectorCodec{pgxvec.HalfVectorCodec{}}})
	}
	return nil
}

// nullSafeVectorCodec turns a NULL scanned into a pgvector value into an error.
// pgvector-go's binary decoder reads the dimension header without checking the
// length and panics on NULL; the text form it replaces returned an error.
type nullSafeVectorCodec struct {
	pgtype.Codec
}

func (c nullSafeVectorCodec) PlanScan(m *pgtype.Map, oid uint32, format int16, target any) pgtype.ScanPlan {
	plan := c.Codec.PlanScan(m, oid, format, target)
	if plan == nil {
		return nil
	}
	return nullSafeScanPlan{plan}
}

type nullSafeScanPlan struct {
	pgtype.ScanPlan
}

func (p nullSafeScanPlan) Scan(src []byte, dst any) error {
	if src == nil {
		return fmt.Errorf("cannot scan NULL into %T", dst)
	}
	return p.ScanPlan.Scan(src, dst)
}
