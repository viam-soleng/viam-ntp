//go:build linux || darwin

package viamntp

import "os"

// canSetClock reports whether this process can step the clock. On Unix that is
// root, or any process holding CAP_SYS_TIME on Linux — the capability case is
// not detected here, so a non-root process with it reports false and finds out
// for certain when it tries.
func canSetClock() bool {
	return os.Geteuid() == 0
}
