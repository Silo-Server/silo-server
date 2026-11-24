//go:build !unix

package main

import "os/exec"

func configureProcessGroup(*exec.Cmd) {}

func killProcessGroup(*exec.Cmd) {}
