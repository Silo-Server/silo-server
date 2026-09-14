package opslog

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"
)

func TestAttrValueError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("s3 PutObject failed: AccessDenied"), "s3 PutObject failed: AccessDenied"},
		{"wrapped", fmt.Errorf("upload chapter-images/1/0/original.webp: %w", errors.New("AccessDenied")), "upload chapter-images/1/0/original.webp: AccessDenied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attrValue(slog.AnyValue(tc.err))
			if got != tc.want {
				t.Fatalf("attrValue(%v) = %#v, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestAttrValueNonError(t *testing.T) {
	type payload struct{ X int }
	got := attrValue(slog.AnyValue(payload{X: 1}))
	if got != (payload{X: 1}) {
		t.Fatalf("attrValue(non-error) = %#v, want %#v", got, payload{X: 1})
	}
}
