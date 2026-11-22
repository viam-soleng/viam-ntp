//go:build darwin

package viamntp

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// setSystemTime steps the wall clock. Requires root. Note that macOS will
// fight back if its own time daemon is enabled; disable it with
// `sudo systemsetup -setusingnetworktime off` before relying on this.
func setSystemTime(t time.Time) error {
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.Settimeofday(&tv)
}

// setHardwareClock is only implemented on Linux.
func setHardwareClock(time.Time) error {
	return errors.New("hw_sync is only supported on Linux")
}
