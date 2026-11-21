//go:build !darwin

package nodeinstall

import (
	"errors"
	"io"
)

var errNodeInstallMacOnly = errors.New("the node installer is for macOS. On Linux, run the Silo image with MODE=transcode as a transcode node")

func installNode([]string, io.Reader, io.Writer) error { return errNodeInstallMacOnly }

func uninstallNode(io.Writer) error { return errNodeInstallMacOnly }
