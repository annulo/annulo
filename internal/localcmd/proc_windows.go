//go:build windows

package localcmd

import "os/exec"

func SetProcGroup(cmd *exec.Cmd) {}
