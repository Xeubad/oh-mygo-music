//go:build !windows

package automix

import "os/exec"

func quietEngineCommand(command *exec.Cmd) {}
