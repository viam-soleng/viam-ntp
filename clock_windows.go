//go:build windows

package viamntp

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sys/windows does not export the privilege-name constants, so
// SE_SYSTEMTIME_NAME is spelled out here.
const seSystemtimePrivilege = "SeSystemtimePrivilege"

// x/sys/windows wraps no SetSystemTime, so bind it from kernel32 directly.
var procSetSystemTime = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetSystemTime")

// setSystemTime steps the wall clock. Two differences from the Unix builds:
// SetSystemTime resolves only to the millisecond, so the correction is coarser
// than ClockSettime gives, and the process needs SeSystemtimePrivilege, which
// in practice means running viam-server as Administrator.
func setSystemTime(t time.Time) error {
	if err := enableSystemTimePrivilege(); err != nil {
		return err
	}

	// SetSystemTime takes UTC, not local time.
	utc := t.UTC()
	st := windows.Systemtime{
		Year:         uint16(utc.Year()),
		Month:        uint16(utc.Month()),
		DayOfWeek:    uint16(utc.Weekday()),
		Day:          uint16(utc.Day()),
		Hour:         uint16(utc.Hour()),
		Minute:       uint16(utc.Minute()),
		Second:       uint16(utc.Second()),
		Milliseconds: uint16(utc.Nanosecond() / int(time.Millisecond)),
	}

	if r, _, err := procSetSystemTime.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return fmt.Errorf("SetSystemTime: %w", err)
	}
	return nil
}

func canSetClock() bool {
	return enableSystemTimePrivilege() == nil
}

// enableSystemTimePrivilege turns on SeSystemtimePrivilege for this process.
// An elevated token holds it but leaves it disabled; an unelevated token does
// not hold it at all.
func enableSystemTimePrivilege() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("open process token: %w", err)
	}
	defer func() { _ = token.Close() }()

	name, err := windows.UTF16PtrFromString(seSystemtimePrivilege)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return fmt.Errorf("look up %s: %w", seSystemtimePrivilege, err)
	}

	privs := windows.Tokenprivileges{PrivilegeCount: 1}
	privs.Privileges[0] = windows.LUIDAndAttributes{
		Luid:       luid,
		Attributes: windows.SE_PRIVILEGE_ENABLED,
	}

	if err := windows.AdjustTokenPrivileges(token, false, &privs, uint32(unsafe.Sizeof(privs)), nil, nil); err != nil {
		return fmt.Errorf("enable %s: %w", seSystemtimePrivilege, err)
	}
	// AdjustTokenPrivileges reports success even when it assigned nothing, so
	// this is the check that actually catches an unprivileged process.
	if err := windows.GetLastError(); err == windows.ERROR_NOT_ALL_ASSIGNED {
		return fmt.Errorf("%s is not held by this process; run viam-server as Administrator",
			seSystemtimePrivilege)
	}
	return nil
}
