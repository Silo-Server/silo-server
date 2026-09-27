//go:build !linux

package mediasample

import "os/exec"

// startBackground starts cmd at normal priority. Only Linux lowers the
// priority of background runs.
func startBackground(cmd *exec.Cmd) error {
	return cmd.Start()
}
