//go:build !windows

package coding

import (
	"os"
	"syscall"
)

// exitSignalNumber reports the number of the signal that terminated the
// process, standing in for the signal name Node passes to a child's "exit"
// event alongside a null exit code. Its only caller is exitCode.
//
// It lives in a build-tagged file because syscall.WaitStatus is a different
// type per platform: on Unix it is a bitfield with Signaled/Signal, on Windows
// a struct with neither, so naming those methods at all breaks the Windows
// build.
func exitSignalNumber(state *os.ProcessState) (int, bool) {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, false
	}
	return int(status.Signal()), true
}
