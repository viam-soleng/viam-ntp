//go:build linux

package viamntp

import (
	"time"

	"golang.org/x/sys/unix"
)

// setSystemTime steps CLOCK_REALTIME. Requires CAP_SYS_TIME (in practice,
// running viam-server as root).
func setSystemTime(t time.Time) error {
	ts := unix.NsecToTimespec(t.UnixNano())
	return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
}
