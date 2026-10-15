package queryattr

import (
	"encoding/json"
	"math"
	"reflect"

	"github.com/jackc/pgx/v5"
)

// Sizes contains value-independent maxima; -1 means unavailable.
type Sizes struct{ Arguments, Array, List, Batch int32 }

func boundedSize(n int) int32 { return int32(min(n, math.MaxInt32)) }
func argumentSizes(args []any) Sizes {
	s := Sizes{}
	options := true
	for _, a := range args {
		switch a.(type) {
		case pgx.QueryExecMode, pgx.QueryResultFormats, pgx.QueryResultFormatsByOID:
			if options {
				continue
			}
		case pgx.QueryRewriter:
			return Sizes{Arguments: -1, Array: -1}
		}
		options = false
		s.Arguments++
		switch a.(type) {
		case []byte, json.RawMessage:
			continue
		}
		v := reflect.ValueOf(a)
		if !v.IsValid() {
			continue
		}
		typ := v.Type()
		if typ.PkgPath() == "github.com/jackc/pgx/v5/pgtype" {
			s.Array = -1
			continue
		}
		if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
			if typ.Elem().Kind() == reflect.Slice || typ.Elem().Kind() == reflect.Array {
				s.Array = -1
				continue
			}
			if s.Array >= 0 {
				s.Array = max(s.Array, boundedSize(v.Len()))
			}
		}
	}
	return s
}
