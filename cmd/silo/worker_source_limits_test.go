package main

import (
	"context"
	"strings"
	"testing"
)

func TestWorkerSourceReaderEnvironment(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		invalid bool
	}{{"", 32, false}, {"33", 33, false}, {"4096", 4096, false}, {"0", 0, true}, {"-1", 0, true}, {"4097", 0, true}, {"many", 0, true}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("SILO_WORKER_SOURCE_MAX_READERS", tc.raw)
			limits, err := workerSourceLimitsFromEnvironment()
			if (err != nil) != tc.invalid || !tc.invalid && limits.MaxReaders != tc.want {
				t.Fatalf("source reader limit %q: got %d error=%v", tc.raw, limits.MaxReaders, err)
			}
		})
	}
}

func TestStandaloneRejectsInvalidSourceLimitsBeforeBinding(t *testing.T) {
	for _, args := range [][]string{{"--max-source-readers=0"}, {"--max-source-readers=-1"}, {"--max-source-readers=4097"}, {"--max-inputs=0"}, {"--max-inputs=-1"}, {"--max-inputs=65537"}} {
		err := runStandaloneWorker(context.Background(), append([]string{"--binding=missing", "--scratch=/tmp"}, args...))
		if err == nil || !strings.Contains(err.Error(), "source") {
			t.Fatalf("invalid source limit must fail before binding load: args=%v err=%v", args, err)
		}
	}
}
