package queryattr

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type panicRewriter struct{}

func (panicRewriter) RewriteQuery(_ctx context.Context, _conn *pgx.Conn, _sql string, _args []any) (string, []any, error) {
	panic("rewriter invoked")
}

type panicValue struct{}

func (panicValue) String() string { panic("String invoked") }

func TestValueIndependentSizes(t *testing.T) {
	for _, tc := range []struct {
		args []any
		want Sizes
	}{
		{nil, Sizes{Arguments: 0, Array: 0}},
		{[]any{1, pgx.QueryExecModeCacheDescribe}, Sizes{Arguments: 2, Array: 0}},
		{[]any{pgx.QueryExecModeCacheDescribe, []int{1, 2, 3}, "private"}, Sizes{Arguments: 2, Array: 3}},
		{[]any{[]byte("private"), json.RawMessage(`[]`), panicValue{}}, Sizes{Arguments: 3, Array: 0}},
		{[]any{[4]string{}}, Sizes{Arguments: 1, Array: 4}},
		{[]any{[][]int{{1}}}, Sizes{Arguments: 1, Array: -1}},
		{[]any{pgtype.Array[int]{}}, Sizes{Arguments: 1, Array: -1}},
		{[]any{pgtype.FlatArray[int]{1, 2}}, Sizes{Arguments: 1, Array: -1}},
		{[]any{pgx.NamedArgs{"private": "private"}}, Sizes{Arguments: -1, Array: -1}},
		{[]any{pgx.StrictNamedArgs{"private": "private"}}, Sizes{Arguments: -1, Array: -1}},
		{[]any{panicRewriter{}}, Sizes{Arguments: -1, Array: -1}},
	} {
		got := argumentSizes(tc.args)
		if got != tc.want {
			t.Errorf("sizes=%+v want=%+v", got, tc.want)
		}
	}
}
