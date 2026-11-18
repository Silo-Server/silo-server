package mediasample

import (
	"context"
	"io"
)

// fakeSamplesExec answers a Samples run: the probe (the first process) with
// header, and the decode with decode, recording its arguments and stdin.
type fakeSamplesExec struct {
	header, decode string
	calls          [][]string
	stdin          []byte
}

func (f *fakeSamplesExec) exec(_ context.Context, _ string, args []string, in io.Reader, _, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	if len(f.calls) == 1 {
		_, err := io.WriteString(stderr, f.header)
		return err
	}
	if in != nil {
		var err error
		if f.stdin, err = io.ReadAll(in); err != nil {
			return err
		}
	}
	_, err := io.WriteString(stderr, f.decode)
	return err
}
