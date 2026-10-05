//go:build windows

package coding

import "os"

// exitSignalNumber always reports "no signal" on Windows: a process there ends
// with an exit code and syscall.WaitStatus carries no signal to read. This is
// the same asymmetry that makes upstream skip its signal regression test on
// win32 (c2d3dc55b). Its only caller is exitCode.
func exitSignalNumber(*os.ProcessState) (int, bool) { return 0, false }
