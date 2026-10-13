//go:build !linux

package workersource

import "os"

func openSourceFile(path string) (*os.File, error) { return os.Open(path) }
