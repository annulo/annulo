//go:build windows

package agent

import "os/exec"

func setProcGroup(cmd *exec.Cmd) {}
