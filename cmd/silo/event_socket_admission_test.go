package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestEventSocketStartupLimitsFallBackWithoutLoggingInput(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"", 64}, {"  ", 64}, {" 3 ", 3}, {"+2", 2}, {"0", 64}, {"-1", 64}, {"private-rejected-value", 64}, {"999999999999999999999999999999999", 64},
	} {
		if got := eventSocketLimit(tc.value, eventSocketsPerAccountEnv, 64); got != tc.want {
			t.Errorf("normalization result=%d want=%d", got, tc.want)
		}
	}
	if strings.Contains(logs.String(), "private-rejected-value") || strings.Contains(logs.String(), "999999999999999999999999999999999") {
		t.Fatal("rejected environment value leaked to logs")
	}
	if !strings.Contains(logs.String(), eventSocketsPerAccountEnv) {
		t.Fatal("warning omitted variable name")
	}
}

func TestEventSocketStartupRemainsObservationOnly(t *testing.T) {
	t.Setenv(eventSocketsPerAccountEnv, "1")
	t.Setenv(eventSocketsPerProcessEnv, "1")
	a, err := newEventSocketAdmission()
	if err != nil {
		t.Fatal(err)
	}
	first, refused := a.Reserve(7, false)
	if refused != "" {
		t.Fatal(refused)
	}
	defer first.Release()
	second, refused := a.Reserve(7, false)
	if refused != "" {
		t.Fatal("startup enforced unobserved quota")
	}
	defer second.Release()
	if a.Occupancy() != 2 {
		t.Fatal("observation omitted reservation")
	}
}
