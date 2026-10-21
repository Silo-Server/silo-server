//go:build !unix

package scanner

import "os/exec"

// killProcessGroupOnCancel leaves cmd's default cancellation, which kills
// only cmd, on platforms without Unix process groups.
func killProcessGroupOnCancel(*exec.Cmd) {}
