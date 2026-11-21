//go:build linux

package viamntp

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// setSystemTime steps CLOCK_REALTIME. Requires CAP_SYS_TIME (in practice,
// running viam-server as root).
func setSystemTime(t time.Time) error {
	ts := unix.NsecToTimespec(t.UnixNano())
	return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
}

// setHardwareClock writes t to the RTC as UTC, like `hwclock -w --utc`.
func setHardwareClock(t time.Time) error {
	fd, err := unix.Open("/dev/rtc0", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open /dev/rtc0: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	// RTCTime is the kernel's struct rtc_time, laid out like C's struct tm:
	// months count from 0 and years from 1900.
	t = t.UTC()
	return unix.IoctlSetRTCTime(fd, &unix.RTCTime{
		Sec:  int32(t.Second()),
		Min:  int32(t.Minute()),
		Hour: int32(t.Hour()),
		Mday: int32(t.Day()),
		Mon:  int32(t.Month()) - 1,
		Year: int32(t.Year()) - 1900,
	})
}
